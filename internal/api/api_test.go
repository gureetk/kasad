package api_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gureetk/kasad/internal/api"
	"github.com/gureetk/kasad/internal/mockplug"
	"github.com/gureetk/kasad/plug"
	"github.com/gureetk/kasad/schedule"
)

// testNow is the moment the tests pretend it is: a Saturday, 04:05:06 UTC.
var testNow = time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)

const everyDay = `["mon", "tue", "wed", "thu", "fri", "sat", "sun"]`

const twoRules = `{"rules": [
  {"id": "morning-on", "name": "Morning", "days": ` + everyDay + `, "time": "06:30", "action": "on"},
  {"id": "morning-off", "days": ` + everyDay + `, "time": "08:00", "action": "off"}
]}`

// fixture is an API server wired to a mock plug and a schedule in a
// temporary directory.
type fixture struct {
	t      *testing.T
	mock   *mockplug.Plug
	store  *schedule.Store
	path   string
	server *api.Server
	http   *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	mock, err := mockplug.New()
	if err != nil {
		t.Fatalf("start mock plug: %v", err)
	}
	t.Cleanup(func() { mock.Close() })
	mock.SetClock(func() time.Time { return testNow })

	path := filepath.Join(t.TempDir(), "schedule.json")
	store, err := schedule.OpenStore(path)
	if err != nil {
		t.Fatalf("open schedule store: %v", err)
	}

	f := &fixture{t: t, mock: mock, store: store, path: path}
	f.server = &api.Server{
		Plug:  plug.New(mock.Addr()),
		Store: store,
		Now:   func() time.Time { return testNow },
	}
	// The handler is looked up on each request, so that a test can change
	// the server's settings after the fixture is built.
	f.http = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.server.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(f.http.Close)
	return f
}

// reply is an HTTP response with its body read.
type reply struct {
	status int
	header http.Header
	body   string
}

// fields decodes the body as a JSON object.
func (r reply) fields(t *testing.T) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(r.body), &fields); err != nil {
		t.Fatalf("response is not a JSON object: %v\n%s", err, r.body)
	}
	return fields
}

// do sends a request. A non-empty body is sent as JSON unless headers says
// otherwise.
func (f *fixture) do(method, path, body string, headers map[string]string) reply {
	f.t.Helper()
	request, err := http.NewRequest(method, f.http.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := f.http.Client().Do(request)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		f.t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return reply{status: response.StatusCode, header: response.Header, body: string(data)}
}

func (f *fixture) setSchedule(content string) {
	f.t.Helper()
	parsed, err := schedule.Parse([]byte(content))
	if err != nil {
		f.t.Fatalf("test schedule is invalid: %v", err)
	}
	if err := f.store.Replace(parsed); err != nil {
		f.t.Fatalf("set schedule: %v", err)
	}
	<-f.store.Changes()
}

func wantStatus(t *testing.T, r reply, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("status = %d, want %d. Body:\n%s", r.status, want, r.body)
	}
}

func TestStatus(t *testing.T) {
	f := newFixture(t)
	f.setSchedule(twoRules)
	// The plug has been on for 90 seconds and its clock is 40 seconds slow.
	f.mock.SetClock(func() time.Time { return testNow.Add(-130 * time.Second) })
	f.mock.SetRelay(true)
	f.mock.SetClock(func() time.Time { return testNow.Add(-40 * time.Second) })

	r := f.do("GET", "/api/status", "", nil)
	wantStatus(t, r, http.StatusOK)

	var got struct {
		Time time.Time `json:"time"`
		Plug struct {
			Reachable          bool   `json:"reachable"`
			Error              string `json:"error"`
			Alias              string `json:"alias"`
			Model              string `json:"model"`
			On                 bool   `json:"on"`
			OnSeconds          int    `json:"on_seconds"`
			RSSI               int    `json:"rssi"`
			Clock              string `json:"clock"`
			ClockOffsetSeconds int    `json:"clock_offset_seconds"`
		} `json:"plug"`
		Next *struct {
			Rule   string    `json:"rule"`
			Name   string    `json:"name"`
			Action string    `json:"action"`
			At     time.Time `json:"at"`
		} `json:"next"`
	}
	if err := json.Unmarshal([]byte(r.body), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, r.body)
	}

	if !got.Time.Equal(testNow) {
		t.Errorf("time = %v, want %v", got.Time, testNow)
	}
	p := got.Plug
	if !p.Reachable || p.Error != "" {
		t.Errorf("plug reachable = %v, error = %q", p.Reachable, p.Error)
	}
	if p.Alias != "Mock Plug" || p.Model != "HS103(US)" || p.RSSI != -50 {
		t.Errorf("plug details = %+v", p)
	}
	if !p.On || p.OnSeconds != 90 {
		t.Errorf("on = %v for %d seconds, want on for 90", p.On, p.OnSeconds)
	}
	if p.Clock != "2001-02-03T04:04:26" || p.ClockOffsetSeconds != -40 {
		t.Errorf("clock = %q, offset = %d, want 2001-02-03T04:04:26 and -40", p.Clock, p.ClockOffsetSeconds)
	}

	if got.Next == nil {
		t.Fatalf("next is null, want the 06:30 rule:\n%s", r.body)
	}
	wantAt := time.Date(2001, time.February, 3, 6, 30, 0, 0, time.UTC)
	if got.Next.Rule != "morning-on" || got.Next.Name != "Morning" || got.Next.Action != "on" || !got.Next.At.Equal(wantAt) {
		t.Errorf("next = %+v, want morning-on at %v", *got.Next, wantAt)
	}
}

func TestStatusWithNoRules(t *testing.T) {
	f := newFixture(t)
	r := f.do("GET", "/api/status", "", nil)
	wantStatus(t, r, http.StatusOK)

	fields := r.fields(t)
	next, present := fields["next"]
	if !present || next != nil {
		t.Errorf("next = %v (present: %v), want an explicit null", next, present)
	}
}

func TestStatusWhenThePlugCannotBeReached(t *testing.T) {
	f := newFixture(t)
	f.setSchedule(twoRules)
	f.server.Plug = unreachablePlug(t)

	r := f.do("GET", "/api/status", "", nil)
	// The API answered, so the request succeeded. The plug's trouble is
	// part of the answer.
	wantStatus(t, r, http.StatusOK)

	fields := r.fields(t)
	plugFields, _ := fields["plug"].(map[string]any)
	if plugFields["reachable"] != false {
		t.Errorf("reachable = %v, want false", plugFields["reachable"])
	}
	if message, _ := plugFields["error"].(string); message == "" {
		t.Error("no error message for an unreachable plug")
	}
	for _, key := range []string{"alias", "on", "clock"} {
		if _, present := plugFields[key]; present {
			t.Errorf("field %q is present for an unreachable plug:\n%s", key, r.body)
		}
	}
	if fields["next"] == nil {
		t.Error("the next event is missing although the schedule does not depend on the plug")
	}
}

// unreachablePlug returns a plug at an address nothing is listening on.
func unreachablePlug(t *testing.T) *plug.Plug {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	p := plug.New(addr)
	p.Client.Timeout = 2 * time.Second
	return p
}

func TestRelay(t *testing.T) {
	f := newFixture(t)

	r := f.do("POST", "/api/relay", `{"on": true}`, nil)
	wantStatus(t, r, http.StatusOK)
	if fields := r.fields(t); fields["on"] != true || fields["alias"] != "Mock Plug" {
		t.Errorf("response = %s", r.body)
	}
	if !f.mock.RelayOn() {
		t.Error("the plug is still off")
	}

	r = f.do("POST", "/api/relay", `{"on": false}`, nil)
	wantStatus(t, r, http.StatusOK)
	if fields := r.fields(t); fields["on"] != false {
		t.Errorf("response = %s", r.body)
	}
	if f.mock.RelayOn() {
		t.Error("the plug is still on")
	}
}

func TestRelayRejectsBadRequests(t *testing.T) {
	f := newFixture(t)
	cases := map[string]struct {
		body   string
		header map[string]string
		want   int
	}{
		"no on field":       {`{}`, nil, http.StatusBadRequest},
		"on is not boolean": {`{"on": "yes"}`, nil, http.StatusBadRequest},
		"on is null":        {`{"on": null}`, nil, http.StatusBadRequest},
		"unknown field":     {`{"on": true, "delay": 5}`, nil, http.StatusBadRequest},
		"not JSON":          {`on=true`, nil, http.StatusBadRequest},
		"two values":        {`{"on": true} {"on": false}`, nil, http.StatusBadRequest},
		"form encoded":      {`on=true`, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, http.StatusUnsupportedMediaType},
		"plain text":        {`{"on": true}`, map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := f.do("POST", "/api/relay", tc.body, tc.header)
			wantStatus(t, r, tc.want)
			if message, _ := r.fields(t)["error"].(string); message == "" {
				t.Errorf("no error message in:\n%s", r.body)
			}
		})
	}
	if f.mock.RelayOn() {
		t.Error("a rejected request switched the plug on")
	}
	if f.mock.Requests() != 0 {
		t.Errorf("rejected requests reached the plug %d times", f.mock.Requests())
	}
}

func TestRelayAcceptsAContentTypeWithParameters(t *testing.T) {
	f := newFixture(t)
	r := f.do("POST", "/api/relay", `{"on": true}`, map[string]string{"Content-Type": "application/json; charset=utf-8"})
	wantStatus(t, r, http.StatusOK)
}

func TestRelayReportsAPlugThatDoesNotSwitch(t *testing.T) {
	f := newFixture(t)
	f.mock.SetStuck(true)

	r := f.do("POST", "/api/relay", `{"on": true}`, nil)
	wantStatus(t, r, http.StatusBadGateway)
	if message, _ := r.fields(t)["error"].(string); !strings.Contains(message, "did not switch") {
		t.Errorf("error = %q", message)
	}
}

func TestRelayReportsAnUnreachablePlug(t *testing.T) {
	f := newFixture(t)
	f.server.Plug = unreachablePlug(t)

	r := f.do("POST", "/api/relay", `{"on": true}`, nil)
	wantStatus(t, r, http.StatusBadGateway)
}

func TestGetSchedule(t *testing.T) {
	f := newFixture(t)

	r := f.do("GET", "/api/schedule", "", nil)
	wantStatus(t, r, http.StatusOK)
	if rules, ok := r.fields(t)["rules"].([]any); !ok || len(rules) != 0 {
		t.Errorf("an empty schedule was returned as:\n%s", r.body)
	}

	f.setSchedule(twoRules)
	r = f.do("GET", "/api/schedule", "", nil)
	wantStatus(t, r, http.StatusOK)
	got, err := schedule.Parse([]byte(r.body))
	if err != nil {
		t.Fatalf("the response is not a schedule the parser accepts: %v\n%s", err, r.body)
	}
	if want, _ := schedule.Parse([]byte(twoRules)); got.Digest() != want.Digest() {
		t.Errorf("schedule = %s", r.body)
	}
}

func TestPutSchedule(t *testing.T) {
	f := newFixture(t)

	r := f.do("PUT", "/api/schedule", twoRules, nil)
	wantStatus(t, r, http.StatusOK)

	want, _ := schedule.Parse([]byte(twoRules))
	// The response is the saved schedule.
	if got, err := schedule.Parse([]byte(r.body)); err != nil || got.Digest() != want.Digest() {
		t.Errorf("response = %s (parse error: %v)", r.body, err)
	}
	// It is what the store now holds, and what is on disk.
	if f.store.Schedule().Digest() != want.Digest() {
		t.Error("the store does not hold the new schedule")
	}
	if saved, err := schedule.Load(f.path); err != nil || saved.Digest() != want.Digest() {
		t.Errorf("the file does not hold the new schedule (load error: %v)", err)
	}
	// And the scheduler is told.
	select {
	case <-f.store.Changes():
	default:
		t.Error("the change was not announced")
	}

	// An empty schedule is a valid replacement: it removes every rule.
	r = f.do("PUT", "/api/schedule", `{"rules": []}`, nil)
	wantStatus(t, r, http.StatusOK)
	if len(f.store.Schedule().Rules) != 0 {
		t.Error("the rules were not removed")
	}
}

func TestPutScheduleRejectsBadSchedules(t *testing.T) {
	f := newFixture(t)
	f.setSchedule(twoRules)
	before := f.store.Schedule().Digest()

	cases := map[string]struct {
		body string
		want string // text the error must contain
	}{
		"not JSON":      {`rules`, "schedule:"},
		"bad time":      {`{"rules": [{"id": "a", "days": ["mon"], "time": "7am", "action": "on"}]}`, "HH:MM"},
		"bad day":       {`{"rules": [{"id": "a", "days": ["someday"], "time": "07:00", "action": "on"}]}`, "unknown day"},
		"bad action":    {`{"rules": [{"id": "a", "days": ["mon"], "time": "07:00", "action": "blink"}]}`, "action"},
		"duplicate id":  {`{"rules": [{"id": "a", "days": ["mon"], "time": "07:00", "action": "on"}, {"id": "a", "days": ["mon"], "time": "08:00", "action": "off"}]}`, "more than one rule"},
		"unknown field": {`{"rules": [], "comment": "hello"}`, "unknown field"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := f.do("PUT", "/api/schedule", tc.body, nil)
			wantStatus(t, r, http.StatusBadRequest)
			if message, _ := r.fields(t)["error"].(string); !strings.Contains(message, tc.want) {
				t.Errorf("error %q does not mention %q", message, tc.want)
			}
		})
	}
	if f.store.Schedule().Digest() != before {
		t.Error("a rejected schedule replaced the old one")
	}
}

func TestPutScheduleRejectsAnOversizedBody(t *testing.T) {
	f := newFixture(t)
	huge := `{"rules": [], "padding": "` + strings.Repeat("x", 100<<10) + `"}`
	r := f.do("PUT", "/api/schedule", huge, nil)
	wantStatus(t, r, http.StatusRequestEntityTooLarge)
}

func TestToken(t *testing.T) {
	f := newFixture(t)
	f.server.Token = "correct horse"

	cases := map[string]struct {
		header string
		want   int
	}{
		"no header":          {"", http.StatusUnauthorized},
		"wrong token":        {"Bearer battery staple", http.StatusUnauthorized},
		"token is a prefix":  {"Bearer correct", http.StatusUnauthorized},
		"token with a tail":  {"Bearer correct horse battery", http.StatusUnauthorized},
		"wrong scheme":       {"Basic correct horse", http.StatusUnauthorized},
		"token alone":        {"correct horse", http.StatusUnauthorized},
		"right token":        {"Bearer correct horse", http.StatusOK},
		"lower-case scheme":  {"bearer correct horse", http.StatusOK},
		"surrounding spaces": {"Bearer  correct horse ", http.StatusOK},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var headers map[string]string
			if tc.header != "" {
				headers = map[string]string{"Authorization": tc.header}
			}
			r := f.do("GET", "/api/status", "", headers)
			wantStatus(t, r, tc.want)
			if tc.want == http.StatusUnauthorized && r.header.Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want Bearer", r.header.Get("WWW-Authenticate"))
			}
		})
	}

	// Writes are covered too, and a refused one does nothing.
	r := f.do("POST", "/api/relay", `{"on": true}`, nil)
	wantStatus(t, r, http.StatusUnauthorized)
	r = f.do("PUT", "/api/schedule", twoRules, nil)
	wantStatus(t, r, http.StatusUnauthorized)
	if f.mock.RelayOn() || len(f.store.Schedule().Rules) != 0 {
		t.Error("a request without the token changed something")
	}

	r = f.do("POST", "/api/relay", `{"on": true}`, map[string]string{"Authorization": "Bearer correct horse"})
	wantStatus(t, r, http.StatusOK)
}

func TestNoTokenMeansOpen(t *testing.T) {
	f := newFixture(t)
	r := f.do("GET", "/api/status", "", map[string]string{"Authorization": "Bearer anything"})
	wantStatus(t, r, http.StatusOK)
}

func TestUnknownPathsAndMethods(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		method, path string
		want         int
		allow        string
	}{
		{"GET", "/api/nothing", http.StatusNotFound, ""},
		{"GET", "/api/", http.StatusNotFound, ""},
		{"GET", "/api/status/extra", http.StatusNotFound, ""},
		{"DELETE", "/api/status", http.StatusMethodNotAllowed, "GET, HEAD"},
		{"GET", "/api/relay", http.StatusMethodNotAllowed, "POST"},
		{"DELETE", "/api/schedule", http.StatusMethodNotAllowed, "GET, HEAD, PUT"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var headers map[string]string
			if tc.method != "GET" {
				headers = map[string]string{"Content-Type": "application/json"}
			}
			r := f.do(tc.method, tc.path, "", headers)
			wantStatus(t, r, tc.want)
			if message, _ := r.fields(t)["error"].(string); message == "" {
				t.Errorf("no JSON error message in:\n%s", r.body)
			}
			if got := r.header.Get("Allow"); got != tc.allow {
				t.Errorf("Allow = %q, want %q", got, tc.allow)
			}
		})
	}
}

func TestResponseHeaders(t *testing.T) {
	f := newFixture(t)
	for _, r := range []reply{
		f.do("GET", "/api/status", "", nil),
		f.do("GET", "/api/schedule", "", nil),
		f.do("POST", "/api/relay", `{}`, nil),
		f.do("GET", "/api/nothing", "", nil),
	} {
		if got := r.header.Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q", got)
		}
		if got := r.header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q", got)
		}
	}
}

func TestHead(t *testing.T) {
	f := newFixture(t)
	r := f.do("HEAD", "/api/status", "", nil)
	wantStatus(t, r, http.StatusOK)
	if r.body != "" {
		t.Errorf("a HEAD response carried a body: %q", r.body)
	}
}
