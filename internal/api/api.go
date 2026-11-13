// Package api serves kasad's JSON API over HTTP.
//
// The API has four operations, all under /api/:
//
//	GET  /api/status    the plug's state and the next scheduled event
//	POST /api/relay     switch the plug on or off
//	GET  /api/schedule  the schedule
//	PUT  /api/schedule  replace the schedule
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

// routes lists the API's paths and the methods each one allows.
var routes = map[string]string{
	"/api/status":   "GET, HEAD",
	"/api/relay":    "POST",
	"/api/schedule": "GET, HEAD, PUT",
}

// Handler returns the handler for everything under /api/.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.getStatus)
	mux.HandleFunc("POST /api/relay", s.postRelay)
	mux.HandleFunc("GET /api/schedule", s.getSchedule)
	mux.HandleFunc("PUT /api/schedule", s.putSchedule)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		if allowed, ok := routes[r.URL.Path]; ok {
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

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
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
