package api_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
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

// Three made-up rules on the plug: on at 07:30 on weekdays; off at sunset at
// weekends, switched off; and one that runs once, on at 21:05 and off again.
var plugRules = []mockplug.Rule{
	{
		ID: "00000000000000000000000000000001", Name: "morning", Enable: 1, Repeat: 1,
		Weekdays: [7]int{0, 1, 1, 1, 1, 1, 0}, StartOption: 0, StartMinute: 450, StartAction: 1, EndAction: -1,
	},
	{
		ID: "00000000000000000000000000000002", Name: "dusk", Enable: 0, Repeat: 1,
		Weekdays: [7]int{1, 0, 0, 0, 0, 0, 1}, StartOption: 2, StartMinute: 0, StartAction: 0, EndAction: -1,
	},
	{
		ID: "00000000000000000000000000000003", Enable: 1, Repeat: 0,
		Weekdays: [7]int{0, 0, 0, 1, 0, 0, 0}, StartOption: 0, StartMinute: 1265, StartAction: 1, EndAction: 0,
	},
}

func TestGetPlugRules(t *testing.T) {
	f := newFixture(t)
	f.mock.SetRules(plugRules...)

	r := f.do("GET", "/api/plug/rules", "", nil)
	wantStatus(t, r, http.StatusOK)

	var want any
	err := json.Unmarshal([]byte(`{
	  "enabled": true,
	  "rules": [
	    {"id": "00000000000000000000000000000001", "name": "morning", "enabled": true, "weekly": true,
	     "repeats": true, "days": ["mon", "tue", "wed", "thu", "fri"], "at": "time", "time": "07:30", "action": "on"},
	    {"id": "00000000000000000000000000000002", "name": "dusk", "enabled": false, "weekly": false,
	     "repeats": true, "days": ["sat", "sun"], "at": "sunset", "action": "off"},
	    {"id": "00000000000000000000000000000003", "enabled": true, "weekly": false,
	     "repeats": false, "days": ["wed"], "at": "time", "time": "21:05", "action": "on",
	     "end": {"at": "unknown", "action": "off"}}
	  ]
	}`), &want)
	if err != nil {
		t.Fatal(err)
	}
	var got any
	if err := json.Unmarshal([]byte(r.body), &got); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, r.body)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GET /api/plug/rules returned:\n%s", r.body)
	}
}

func TestGetPlugRulesWhenThereAreNone(t *testing.T) {
	f := newFixture(t)
	f.mock.SetScheduleEnabled(false)

	r := f.do("GET", "/api/plug/rules", "", nil)
	wantStatus(t, r, http.StatusOK)
	fields := r.fields(t)
	if rules, ok := fields["rules"].([]any); !ok || len(rules) != 0 {
		t.Errorf(`"rules" is %v, want an empty list`, fields["rules"])
	}
	if fields["enabled"] != false {
		t.Errorf(`"enabled" is %v, want false`, fields["enabled"])
	}
}

func TestGetPlugRulesReportsAnUnreachablePlug(t *testing.T) {
	f := newFixture(t)
	f.mock.Close()

	r := f.do("GET", "/api/plug/rules", "", nil)
	wantStatus(t, r, http.StatusBadGateway)
	if message, _ := r.fields(t)["error"].(string); message == "" {
		t.Errorf("no error message in:\n%s", r.body)
	}
}

func TestDeletePlugRule(t *testing.T) {
	f := newFixture(t)
	f.mock.SetRules(plugRules...)

	// A DELETE needs no body and no Content-Type.
	r := f.do("DELETE", "/api/plug/rules/00000000000000000000000000000002", "", nil)
	wantStatus(t, r, http.StatusOK)
	if got := r.fields(t)["deleted"]; got != "00000000000000000000000000000002" {
		t.Errorf(`"deleted" = %v`, got)
	}
	left := f.mock.Rules()
	if len(left) != 2 || left[0].ID != plugRules[0].ID || left[1].ID != plugRules[2].ID {
		t.Errorf("rules left on the plug: %+v, want the first and third", left)
	}

	// The schedule kasad runs is a different thing, and is not touched.
	if rules := f.store.Schedule().Rules; len(rules) != 0 {
		t.Errorf("deleting a rule on the plug changed kasad's schedule: %+v", rules)
	}
}

func TestDeletePlugRuleThatIsNotThere(t *testing.T) {
	f := newFixture(t)
	f.mock.SetRules(plugRules...)

	// The second id is a rule of kasad's own schedule, which is not the
	// plug's to delete.
	f.setSchedule(twoRules)
	for _, id := range []string{"no-such-rule", "morning-on", "%20", "a%2Fb"} {
		r := f.do("DELETE", "/api/plug/rules/"+id, "", nil)
		wantStatus(t, r, http.StatusNotFound)
		if message, _ := r.fields(t)["error"].(string); !strings.Contains(message, "no rule") {
			t.Errorf("DELETE %s: error message %q does not say there is no such rule", id, message)
		}
	}
	if left := f.mock.Rules(); len(left) != len(plugRules) {
		t.Errorf("%d rules left on the plug, want all %d", len(left), len(plugRules))
	}
	if rules := f.store.Schedule().Rules; len(rules) != 2 {
		t.Errorf("kasad's schedule has %d rules, want 2", len(rules))
	}
}

func TestDeletePlugRuleReportsAnUnreachablePlug(t *testing.T) {
	f := newFixture(t)
	f.mock.SetRules(plugRules...)
	f.mock.Close()

	r := f.do("DELETE", "/api/plug/rules/00000000000000000000000000000001", "", nil)
	wantStatus(t, r, http.StatusBadGateway)
}

const eveningRule = `{"name": " evening ", "days": ["mon", "fri"], "time": "18:05", "action": "on"}`

func TestPostPlugRule(t *testing.T) {
	f := newFixture(t)
	f.mock.SetRules(plugRules...)

	r := f.do("POST", "/api/plug/rules", eveningRule, nil)
	wantStatus(t, r, http.StatusCreated)
	fields := r.fields(t)
	id, _ := fields["id"].(string)
	if len(id) != 32 {
		t.Fatalf("the new rule's id is %q:\n%s", id, r.body)
	}
	for field, want := range map[string]any{
		"name": "evening", "enabled": true, "weekly": true, "repeats": true,
		"at": "time", "time": "18:05", "action": "on",
	} {
		if fields[field] != want {
			t.Errorf("%s = %v, want %v", field, fields[field], want)
		}
	}
	if days, _ := fields["days"].([]any); len(days) != 2 || days[0] != "mon" || days[1] != "fri" {
		t.Errorf("days = %v, want mon and fri", fields["days"])
	}

	rules := f.mock.Rules()
	if len(rules) != len(plugRules)+1 {
		t.Fatalf("the plug has %d rules, want %d", len(rules), len(plugRules)+1)
	}
	want := mockplug.Rule{
		ID: id, Name: "evening", Enable: 1, Repeat: 1,
		Weekdays: [7]int{0, 1, 0, 0, 0, 1, 0}, StartOption: 0, StartMinute: 1085, StartAction: 1, EndAction: -1,
	}
	if got := rules[len(rules)-1]; got != want {
		t.Errorf("the plug stored:\n got %+v\nwant %+v", got, want)
	}

	// The schedule kasad runs is a different thing, and is not touched.
	if own := f.store.Schedule().Rules; len(own) != 0 {
		t.Errorf("adding a rule to the plug changed kasad's schedule: %+v", own)
	}
}

func TestPostPlugRuleCanAddOneSwitchedOff(t *testing.T) {
	f := newFixture(t)
	r := f.do("POST", "/api/plug/rules", `{"enabled": false, "days": ["sun"], "time": "00:00", "action": "off"}`, nil)
	wantStatus(t, r, http.StatusCreated)
	rules := f.mock.Rules()
	if len(rules) != 1 || rules[0].Enable != 0 || rules[0].Weekdays != [7]int{1, 0, 0, 0, 0, 0, 0} || rules[0].StartAction != 0 || rules[0].Name != "" {
		t.Errorf("the plug stored: %+v", rules)
	}
}

func TestPlugRuleRequestsMustDescribeARule(t *testing.T) {
	f := newFixture(t)
	f.mock.SetRules(plugRules...)

	cases := map[string]string{
		"empty object":      `{}`,
		"not JSON":          `on at six`,
		"no days":           `{"time": "18:05", "action": "on"}`,
		"empty days":        `{"days": [], "time": "18:05", "action": "on"}`,
		"an unknown day":    `{"days": ["funday"], "time": "18:05", "action": "on"}`,
		"no time":           `{"days": ["mon"], "action": "on"}`,
		"a time of 25:00":   `{"days": ["mon"], "time": "25:00", "action": "on"}`,
		"no action":         `{"days": ["mon"], "time": "18:05"}`,
		"an unknown action": `{"days": ["mon"], "time": "18:05", "action": "toggle"}`,
		"a sunset rule":     `{"days": ["mon"], "at": "sunset", "action": "on"}`,
		"an id in the body": `{"id": "x", "days": ["mon"], "time": "18:05", "action": "on"}`,
	}
	for name, body := range cases {
		for _, request := range [][2]string{
			{"POST", "/api/plug/rules"},
			{"PUT", "/api/plug/rules/00000000000000000000000000000001"},
		} {
			r := f.do(request[0], request[1], body, map[string]string{"Content-Type": "application/json"})
			if r.status != http.StatusBadRequest {
				t.Errorf("%s, %s: status = %d, want 400. Body:\n%s", name, request[0], r.status, r.body)
			}
		}
	}
	rules := f.mock.Rules()
	if len(rules) != len(plugRules) || rules[0] != plugRules[0] {
		t.Errorf("a bad request changed the plug's rules: %+v", rules)
	}

	// Like every request with a body, these must be sent as JSON.
	r := f.do("POST", "/api/plug/rules", eveningRule, map[string]string{"Content-Type": "text/plain"})
	wantStatus(t, r, http.StatusUnsupportedMediaType)
}

func TestPutPlugRule(t *testing.T) {
	f := newFixture(t)
	f.mock.SetRules(plugRules...)

	r := f.do("PUT", "/api/plug/rules/00000000000000000000000000000001",
		`{"name": "later", "enabled": false, "days": ["sat", "sun"], "time": "09:40", "action": "off"}`, nil)
	wantStatus(t, r, http.StatusOK)
	fields := r.fields(t)
	if fields["id"] != "00000000000000000000000000000001" || fields["time"] != "09:40" || fields["enabled"] != false || fields["action"] != "off" {
		t.Errorf("PUT returned:\n%s", r.body)
	}

	rules := f.mock.Rules()
	want := mockplug.Rule{
		ID: "00000000000000000000000000000001", Name: "later", Enable: 0, Repeat: 1,
		Weekdays: [7]int{1, 0, 0, 0, 0, 0, 1}, StartOption: 0, StartMinute: 580, StartAction: 0, EndAction: -1,
	}
	if len(rules) != len(plugRules) || rules[0] != want || rules[1] != plugRules[1] {
		t.Errorf("the plug's rules after the change: %+v", rules)
	}
}

func TestPutPlugRuleRefusesRulesItCannotChange(t *testing.T) {
	f := newFixture(t)
	f.mock.SetRules(plugRules...)

	cases := []struct {
		name, id string
		want     int
	}{
		{"a rule that is not there", "no-such-rule", http.StatusNotFound},
		{"a sunset rule", "00000000000000000000000000000002", http.StatusConflict},
		{"a rule that runs once", "00000000000000000000000000000003", http.StatusConflict},
	}
	for _, tc := range cases {
		r := f.do("PUT", "/api/plug/rules/"+tc.id, eveningRule, nil)
		if r.status != tc.want {
			t.Errorf("%s: status = %d, want %d. Body:\n%s", tc.name, r.status, tc.want, r.body)
		}
	}
	rules := f.mock.Rules()
	for i := range plugRules {
		if rules[i] != plugRules[i] {
			t.Errorf("rule %d was changed: %+v", i, rules[i])
		}
	}
}

func TestPlugRuleWritesReportAPlugThatDoesNotStoreThem(t *testing.T) {
	f := newFixture(t)
	f.mock.SetRules(plugRules...)
	f.mock.SetForgetful(true)

	for _, request := range [][2]string{
		{"POST", "/api/plug/rules"},
		{"PUT", "/api/plug/rules/00000000000000000000000000000001"},
	} {
		r := f.do(request[0], request[1], eveningRule, nil)
		wantStatus(t, r, http.StatusBadGateway)
		if message, _ := r.fields(t)["error"].(string); !strings.Contains(message, "did not store") {
			t.Errorf("%s: error message %q does not say the rule was not stored", request[0], message)
		}
	}
}

func TestPlugRuleWritesReportAnUnreachablePlug(t *testing.T) {
	f := newFixture(t)
	f.mock.Close()

	wantStatus(t, f.do("POST", "/api/plug/rules", eveningRule, nil), http.StatusBadGateway)
	wantStatus(t, f.do("PUT", "/api/plug/rules/00000000000000000000000000000001", eveningRule, nil), http.StatusBadGateway)
}

func TestPlugRulesNeedTheToken(t *testing.T) {
	f := newFixture(t)
	f.mock.SetRules(plugRules...)
	f.server.Token = "s3cret"

	wantStatus(t, f.do("GET", "/api/plug/rules", "", nil), http.StatusUnauthorized)
	wantStatus(t, f.do("DELETE", "/api/plug/rules/00000000000000000000000000000001", "", nil), http.StatusUnauthorized)
	wantStatus(t, f.do("POST", "/api/plug/rules", eveningRule, nil), http.StatusUnauthorized)
	wantStatus(t, f.do("PUT", "/api/plug/rules/00000000000000000000000000000001", eveningRule, nil), http.StatusUnauthorized)
	if left := f.mock.Rules(); len(left) != len(plugRules) || left[0] != plugRules[0] {
		t.Fatalf("a request without the token changed the plug's rules: %+v", left)
	}

	auth := map[string]string{"Authorization": "Bearer s3cret"}
	wantStatus(t, f.do("GET", "/api/plug/rules", "", auth), http.StatusOK)
	wantStatus(t, f.do("DELETE", "/api/plug/rules/00000000000000000000000000000001", "", auth), http.StatusOK)
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
		{"DELETE", "/api/plug/rules", http.StatusMethodNotAllowed, "GET, HEAD, POST"},
		{"GET", "/api/plug/rules/some-rule", http.StatusMethodNotAllowed, "DELETE, PUT"},
		{"POST", "/api/plug/rules/some-rule", http.StatusMethodNotAllowed, "DELETE, PUT"},
		{"DELETE", "/api/plug/rules/", http.StatusNotFound, ""},
		{"DELETE", "/api/plug/rules/some-rule/more", http.StatusNotFound, ""},
		{"GET", "/api/plug", http.StatusNotFound, ""},
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
