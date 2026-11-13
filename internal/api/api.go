// Package api serves kasad's JSON API over HTTP.
//
// The API has six operations, all under /api/:
//
//	GET    /api/status           the plug's state and the next scheduled event
//	POST   /api/relay            switch the plug on or off
//	GET    /api/schedule         the schedule kasad runs
//	PUT    /api/schedule         replace the schedule kasad runs
//	GET    /api/plug/rules       the rules the plug keeps and runs by itself
//	DELETE /api/plug/rules/{id}  delete one of the plug's own rules
//
// Requests and responses are JSON. An error response has the form
// {"error": "..."}.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
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
	DeleteRule(ctx context.Context, id string) error
}

// Server holds what the API's handlers work with.
type Server struct {
	// Plug is the device the API reports on and switches.
	Plug Device

	// Store holds the schedule the API reads and replaces.
	Store *schedule.Store

	// Token, if not empty, must accompany every request as an
	// "Authorization: Bearer" header.
	Token string

	// Now returns the current time. It defaults to time.Now.
	Now func() time.Time

	// Logger receives a record of the changes made through the API. It
	// defaults to discarding everything.
	Logger *slog.Logger
}

// routes lists the API's fixed paths and the methods each one allows.
var routes = map[string]string{
	"/api/status":     "GET, HEAD",
	"/api/relay":      "POST",
	"/api/schedule":   "GET, HEAD, PUT",
	"/api/plug/rules": "GET, HEAD",
}

// plugRulePrefix is the path of one of the plug's own rules, less its id.
const plugRulePrefix = "/api/plug/rules/"

// allowedMethods returns the methods the API allows on path, and whether
// path is one of the API's at all.
func allowedMethods(path string) (string, bool) {
	if allowed, ok := routes[path]; ok {
		return allowed, true
	}
	if id, ok := strings.CutPrefix(path, plugRulePrefix); ok && id != "" && !strings.Contains(id, "/") {
		return "DELETE", true
	}
	return "", false
}

// Handler returns the handler for everything under /api/.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.getStatus)
	mux.HandleFunc("POST /api/relay", s.postRelay)
	mux.HandleFunc("GET /api/schedule", s.getSchedule)
	mux.HandleFunc("PUT /api/schedule", s.putSchedule)
	mux.HandleFunc("GET /api/plug/rules", s.getPlugRules)
	mux.HandleFunc("DELETE "+plugRulePrefix+"{id}", s.deletePlugRule)
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

		if s.Token != "" && !s.authorized(r) {
			header.Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "missing or wrong token")
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

// authorized reports whether r carries the server's token.
func (s *Server) authorized(r *http.Request) bool {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(s.Token)) == 1
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
