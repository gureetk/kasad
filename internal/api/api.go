// Package api serves kasad's JSON API over HTTP.
//
// The API has these operations, all under /api/:
//
//	GET    /api/status           the plug's state and the next scheduled event
//	POST   /api/relay            switch the plug on or off
//	GET    /api/schedule         the schedule kasad runs
//	PUT    /api/schedule         replace the schedule kasad runs
//	GET    /api/plug/rules       the rules the plug keeps and runs by itself
//	POST   /api/plug/rules       add a rule to the plug's own
//	PUT    /api/plug/rules/{id}  change one of the plug's own rules
//	DELETE /api/plug/rules/{id}  delete one of the plug's own rules
//	GET    /api/session          whether the caller must, and has, signed in
//	POST   /api/session          sign in with the password
//	DELETE /api/session          sign out
//
// Requests and responses are JSON. An error response has the form
// {"error": "..."}.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gureetk/kasad/plug"
	"github.com/gureetk/kasad/schedule"
)

// maxBodySize is the largest request body the API reads. A schedule is a
// short list of rules, so this is generous.
const maxBodySize = 64 << 10

// Device is the part of a plug the API uses. *plug.Plug implements it.
type Device interface {
	Status(ctx context.Context) (plug.Status, error)
	Switch(ctx context.Context, on bool) (plug.Sysinfo, error)
	Rules(ctx context.Context) (plug.Rules, error)
	AddRule(ctx context.Context, rule plug.WeeklyRule) (plug.Rule, error)
	EditRule(ctx context.Context, id string, rule plug.WeeklyRule) (plug.Rule, error)
	DeleteRule(ctx context.Context, id string) error
}

// Server holds what the API's handlers work with.
type Server struct {
	// Plug is the device the API reports on and switches.
	Plug Device

	// Store holds the schedule the API reads and replaces.
	Store *schedule.Store

	// Token, if not empty, lets a request use the API when it carries it
	// in an "Authorization: Bearer" header. It is meant for programs.
	Token string

	// Password, if not empty, is what a person gives to sign in. A
	// request then counts as theirs for as long as it carries the cookie
	// that signing in sets. It is meant for the web app.
	//
	// When Token and Password are both empty, the API is open to anyone
	// who can reach it. When either is set, every request must show one
	// of the two, except those to /api/session.
	Password string

	// SessionFile, if not empty, names a file in which the sessions of
	// people who have signed in are kept, so that they stay signed in
	// when the program is restarted. Without it, a restart signs everyone
	// out. It is used only when there is a Password.
	SessionFile string

	// Now returns the current time. It defaults to time.Now.
	Now func() time.Time

	// Logger receives a record of the changes made through the API. It
	// defaults to discarding everything.
	Logger *slog.Logger

	// prepared guards the work of the first call to Handler, which
	// readies the sessions with Password and SessionFile. Neither is to
	// be changed after that.
	prepared sync.Once
	sessions sessions
	attempts attempts
}

// routes lists the API's fixed paths and the methods each one allows.
var routes = map[string]string{
	"/api/status":     "GET, HEAD",
	"/api/relay":      "POST",
	"/api/schedule":   "GET, HEAD, PUT",
	"/api/plug/rules": "GET, HEAD, POST",
	"/api/session":    "DELETE, GET, HEAD, POST",
}

// sessionPath is where a caller signs in and out. It has to answer callers
// who have not signed in yet.
const sessionPath = "/api/session"

// plugRulePrefix is the path of one of the plug's own rules, less its id.
const plugRulePrefix = "/api/plug/rules/"

// allowedMethods returns the methods the API allows on path, and whether
// path is one of the API's at all.
func allowedMethods(path string) (string, bool) {
	if allowed, ok := routes[path]; ok {
		return allowed, true
	}
	if id, ok := strings.CutPrefix(path, plugRulePrefix); ok && id != "" && !strings.Contains(id, "/") {
		return "DELETE, PUT", true
	}
	return "", false
}

// Handler returns the handler for everything under /api/. The first call
// also reads the sessions saved in SessionFile.
func (s *Server) Handler() http.Handler {
	s.prepared.Do(s.prepare)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.getStatus)
	mux.HandleFunc("POST /api/relay", s.postRelay)
	mux.HandleFunc("GET /api/schedule", s.getSchedule)
	mux.HandleFunc("PUT /api/schedule", s.putSchedule)
	mux.HandleFunc("GET /api/plug/rules", s.getPlugRules)
	mux.HandleFunc("POST /api/plug/rules", s.postPlugRule)
	mux.HandleFunc("PUT "+plugRulePrefix+"{id}", s.putPlugRule)
	mux.HandleFunc("DELETE "+plugRulePrefix+"{id}", s.deletePlugRule)
	mux.HandleFunc("GET "+sessionPath, s.getSession)
	mux.HandleFunc("POST "+sessionPath, s.postSession)
	mux.HandleFunc("DELETE "+sessionPath, s.deleteSession)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		if allowed, ok := allowedMethods(r.URL.Path); ok {
			w.Header().Set("Allow", allowed)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeError(w, http.StatusNotFound, "not found")
	})
	return s.guard(mux)
}

// guard applies the checks every request must pass before it reaches a
// handler.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Cache-Control", "no-store")
		header.Set("X-Content-Type-Options", "nosniff")

		changes := r.Method != http.MethodGet && r.Method != http.MethodHead
		if changes && crossSite(r) {
			writeError(w, http.StatusForbidden, "this request was sent from a page on another site")
			return
		}

		if !s.open() && r.URL.Path != sessionPath && !s.authorized(r) {
			message := "sign in first"
			if s.Token != "" {
				header.Set("WWW-Authenticate", "Bearer")
				message = "sign in first, or send the token"
			}
			writeError(w, http.StatusUnauthorized, message)
			return
		}

		switch r.Method {
		case http.MethodGet, http.MethodHead:
		case http.MethodDelete:
			// A DELETE has no body to describe. A web page on another
			// site cannot make a browser send one without asking first,
			// which this server never agrees to.
		default:
			// A web page on another site can make a browser send a form
			// or plain-text request here, but not a JSON one without
			// asking first. Insisting on JSON keeps such pages out.
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

		next.ServeHTTP(w, r)
	})
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// statusResponse is the body of GET /api/status.
type statusResponse struct {
	// Time is the server's current time.
	Time time.Time `json:"time"`

	Plug plugStatus `json:"plug"`

	// Next is the next scheduled event, or null if there is none.
	Next *eventStatus `json:"next"`
}

// plugStatus describes the plug. When the plug cannot be reached, only
// Reachable and Error are present.
type plugStatus struct {
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
	*plugDetails
}

type plugDetails struct {
	Alias string `json:"alias"`
	Model string `json:"model"`
	On    bool   `json:"on"`

	// OnSeconds is how long the plug has been on. It is zero while off.
	OnSeconds int `json:"on_seconds"`

	// RSSI is the plug's Wi-Fi signal strength in dBm.
	RSSI int `json:"rssi"`

	// Clock is the plug's own date and time, which has no UTC offset.
	Clock string `json:"clock"`

	// ClockOffsetSeconds is how far the plug's clock is from the server's,
	// taking the plug to be in the server's timezone. Positive means the
	// plug is ahead.
	ClockOffsetSeconds int `json:"clock_offset_seconds"`
}

type eventStatus struct {
	Rule   string          `json:"rule"`
	Name   string          `json:"name,omitempty"`
	Action schedule.Action `json:"action"`
	At     time.Time       `json:"at"`
}

func (s *Server) getStatus(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	response := statusResponse{Time: now}

	status, err := s.Plug.Status(r.Context())
	if err != nil {
		response.Plug = plugStatus{Reachable: false, Error: err.Error()}
	} else {
		clock := status.Clock.In(now.Location())
		response.Plug = plugStatus{
			Reachable: true,
			plugDetails: &plugDetails{
				Alias:              status.Sysinfo.Alias,
				Model:              status.Sysinfo.Model,
				On:                 status.Sysinfo.On(),
				OnSeconds:          status.Sysinfo.OnTime,
				RSSI:               status.Sysinfo.RSSI,
				Clock:              clock.Format("2006-01-02T15:04:05"),
				ClockOffsetSeconds: int(clock.Sub(now).Round(time.Second) / time.Second),
			},
		}
	}

	if next, ok := s.Store.Schedule().Next(now); ok {
		response.Next = &eventStatus{
			Rule:   next.Rule.ID,
			Name:   next.Rule.Name,
			Action: next.Rule.Action,
			At:     next.At,
		}
	}
	writeJSON(w, http.StatusOK, response)
}

// relayRequest is the body of POST /api/relay.
type relayRequest struct {
	On *bool `json:"on"`
}

// relayResponse is the reply to POST /api/relay.
type relayResponse struct {
	Alias string `json:"alias"`
	On    bool   `json:"on"`
}

func (s *Server) postRelay(w http.ResponseWriter, r *http.Request) {
	var request relayRequest
	if err := decodeBody(r, &request); err != nil {
		writeBodyError(w, err)
		return
	}
	if request.On == nil {
		writeError(w, http.StatusBadRequest, `the request must say "on": true or "on": false`)
		return
	}

	info, err := s.Plug.Switch(r.Context(), *request.On)
	if err != nil {
		s.logger().Error("could not switch the plug for an API request", "on", *request.On, "error", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.logger().Info("switched the plug for an API request", "on", *request.On)
	writeJSON(w, http.StatusOK, relayResponse{Alias: info.Alias, On: info.On()})
}

func (s *Server) getSchedule(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.Schedule())
}

func (s *Server) putSchedule(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	next, err := schedule.Parse(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.Replace(next); err != nil {
		s.logger().Error("could not save the schedule for an API request", "error", err)
		writeError(w, http.StatusInternalServerError, "could not save the schedule")
		return
	}
	s.logger().Info("replaced the schedule for an API request", "rules", len(next.Rules))
	writeJSON(w, http.StatusOK, s.Store.Schedule())
}

// plugRulesResponse is the body of GET /api/plug/rules: the schedule the
// plug keeps and runs by itself, which is separate from the one kasad runs.
type plugRulesResponse struct {
	// Enabled reports whether the plug's schedule as a whole is switched
	// on. When it is not, none of the rules run.
	Enabled bool `json:"enabled"`

	Rules []plugRule `json:"rules"`
}

// plugRule is one of the plug's own rules.
type plugRule struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Enabled bool   `json:"enabled"`

	// Weekly reports whether this is the plain kind of rule, which
	// switches the plug at a time of day on chosen days of every week and
	// does nothing else. The schedule kasad runs is made of that kind.
	Weekly bool `json:"weekly"`

	// Repeats is false for a rule that runs once.
	Repeats bool          `json:"repeats"`
	Days    schedule.Days `json:"days"`

	plugRuleMoment

	// End is what the rule does a second time, later on. It is left out
	// when the rule has no second action.
	End *plugRuleMoment `json:"end,omitempty"`
}

// plugRuleMoment is one thing a rule on the plug does, and when.
type plugRuleMoment struct {
	// At says how the moment is timed: "time", "sunrise", "sunset" or
	// "unknown".
	At string `json:"at"`

	// Time is the time of day as HH:MM, by the plug's clock. It is present
	// only when At is "time".
	Time string `json:"time,omitempty"`

	// Action is "on", "off" or "unknown".
	Action string `json:"action"`
}

func newPlugRuleMoment(timed plug.RuleTime, minute int, action plug.RuleAction) plugRuleMoment {
	moment := plugRuleMoment{At: "unknown", Action: "unknown"}
	switch {
	case timed == plug.AtClockTime && minute >= 0 && minute < 24*60:
		moment.At = "time"
		moment.Time = schedule.TimeOfDay{Hour: minute / 60, Minute: minute % 60}.String()
	case timed == plug.AtSunrise:
		moment.At = "sunrise"
	case timed == plug.AtSunset:
		moment.At = "sunset"
	}
	switch action {
	case plug.RuleOn:
		moment.Action = "on"
	case plug.RuleOff:
		moment.Action = "off"
	}
	return moment
}

func newPlugRule(rule plug.Rule) plugRule {
	described := plugRule{
		ID:             rule.ID,
		Name:           rule.Name,
		Enabled:        rule.Enabled,
		Weekly:         rule.Weekly(),
		Repeats:        rule.Repeat,
		Days:           schedule.Days(rule.Days),
		plugRuleMoment: newPlugRuleMoment(rule.Time, rule.Minute, rule.Action),
	}
	if rule.EndAction != plug.RuleNone {
		end := newPlugRuleMoment(rule.EndTime, rule.EndMinute, rule.EndAction)
		described.End = &end
	}
	return described
}

func (s *Server) getPlugRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.Plug.Rules(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	response := plugRulesResponse{
		Enabled: rules.Enabled,
		Rules:   make([]plugRule, 0, len(rules.List)),
	}
	for _, rule := range rules.List {
		response.Rules = append(response.Rules, newPlugRule(rule))
	}
	writeJSON(w, http.StatusOK, response)
}

// plugRuleRequest is the body of POST /api/plug/rules and of
// PUT /api/plug/rules/{id}: a rule of the plain weekly kind, in the same
// terms GET /api/plug/rules describes one.
type plugRuleRequest struct {
	Name string `json:"name"`

	// Enabled says whether the rule is switched on. Left out, it is.
	Enabled *bool `json:"enabled"`

	Days   schedule.Days       `json:"days"`
	Time   *schedule.TimeOfDay `json:"time"`
	Action schedule.Action     `json:"action"`
}

// weeklyRule checks the request and turns it into a rule for the plug.
func (request plugRuleRequest) weeklyRule() (plug.WeeklyRule, error) {
	rule := plug.WeeklyRule{
		Name:    strings.TrimSpace(request.Name),
		Enabled: request.Enabled == nil || *request.Enabled,
		Days:    [7]bool(request.Days),
	}
	if request.Days.Empty() {
		return plug.WeeklyRule{}, errors.New(`the rule needs "days", with at least one day in it`)
	}
	if request.Time == nil {
		return plug.WeeklyRule{}, errors.New(`the rule needs a "time", such as "07:30"`)
	}
	rule.Minute = request.Time.Hour*60 + request.Time.Minute
	switch request.Action {
	case schedule.On:
		rule.Action = plug.RuleOn
	case schedule.Off:
		rule.Action = plug.RuleOff
	default:
		return plug.WeeklyRule{}, errors.New(`the rule needs "action": "on" or "action": "off"`)
	}
	return rule, nil
}

// readPlugRule decodes and checks the body of a request that carries a rule
// for the plug. It reports false after answering the request itself.
func readPlugRule(w http.ResponseWriter, r *http.Request) (plug.WeeklyRule, bool) {
	var request plugRuleRequest
	if err := decodeBody(r, &request); err != nil {
		writeBodyError(w, err)
		return plug.WeeklyRule{}, false
	}
	rule, err := request.weeklyRule()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return plug.WeeklyRule{}, false
	}
	return rule, true
}

// writePlugRuleError answers a request to add or change a rule on the plug
// that failed.
func (s *Server) writePlugRuleError(w http.ResponseWriter, doing, id string, err error) {
	switch {
	case errors.Is(err, plug.ErrNoSuchRule):
		writeError(w, http.StatusNotFound, "the plug has no rule with the id "+strconv.Quote(id))
	case errors.Is(err, plug.ErrNotWeekly):
		writeError(w, http.StatusConflict, "that rule is of a kind that can be deleted here, but not changed")
	default:
		s.logger().Error("could not "+doing+" for an API request", "rule", id, "error", err)
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

func (s *Server) postPlugRule(w http.ResponseWriter, r *http.Request) {
	rule, ok := readPlugRule(w, r)
	if !ok {
		return
	}
	stored, err := s.Plug.AddRule(r.Context(), rule)
	if err != nil {
		s.writePlugRuleError(w, "add a rule to the plug", stored.ID, err)
		return
	}
	s.logger().Info("added a rule to the plug for an API request", "rule", stored.ID)
	writeJSON(w, http.StatusCreated, newPlugRule(stored))
}

func (s *Server) putPlugRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rule, ok := readPlugRule(w, r)
	if !ok {
		return
	}
	stored, err := s.Plug.EditRule(r.Context(), id, rule)
	if err != nil {
		s.writePlugRuleError(w, "change a rule on the plug", id, err)
		return
	}
	s.logger().Info("changed a rule on the plug for an API request", "rule", id)
	writeJSON(w, http.StatusOK, newPlugRule(stored))
}

func (s *Server) deletePlugRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.Plug.DeleteRule(r.Context(), id)
	if errors.Is(err, plug.ErrNoSuchRule) {
		writeError(w, http.StatusNotFound, "the plug has no rule with the id "+strconv.Quote(id))
		return
	}
	if err != nil {
		s.logger().Error("could not delete a rule from the plug for an API request", "rule", id, "error", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.logger().Info("deleted a rule from the plug for an API request", "rule", id)
	writeJSON(w, http.StatusOK, map[string]string{"deleted": id})
}

// decodeBody decodes a JSON request body into v. Unknown fields and trailing
// content are errors.
func decodeBody(r *http.Request, v any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("unexpected content after the JSON value")
	}
	return nil
}

// writeBodyError reports a request body that could not be read or decoded.
func writeBodyError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		http.Error(w, `{"error": "could not encode the response"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(append(body, '\n'))
}
