package mockplug_test

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gureetk/kasad/internal/mockplug"
	"github.com/gureetk/kasad/protocol"
)

func startPlug(t *testing.T) *mockplug.Plug {
	t.Helper()
	plug, err := mockplug.New()
	if err != nil {
		t.Fatalf("start mock plug: %v", err)
	}
	t.Cleanup(func() { plug.Close() })
	return plug
}

// send performs one exchange and returns the reply's entry for each module.
func send(t *testing.T, addr, request string) map[string]json.RawMessage {
	t.Helper()
	var client protocol.Client
	raw, err := client.Send(context.Background(), addr, []byte(request))
	if err != nil {
		t.Fatalf("Send %s: %v", request, err)
	}
	var reply map[string]json.RawMessage
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatalf("decode reply %q: %v", raw, err)
	}
	return reply
}

// methods splits one module's entry into its per-method results.
func methods(t *testing.T, module json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var results map[string]json.RawMessage
	if err := json.Unmarshal(module, &results); err != nil {
		t.Fatalf("decode module entry %q: %v", module, err)
	}
	return results
}

func errCode(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var status struct {
		Code *int `json:"err_code"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatalf("decode status %q: %v", raw, err)
	}
	if status.Code == nil {
		t.Fatalf("status %q has no err_code", raw)
	}
	return *status.Code
}

func TestUnsupportedModule(t *testing.T) {
	plug := startPlug(t)
	reply := send(t, plug.Addr(), `{"emeter":{"get_realtime":{}}}`)
	if code := errCode(t, reply["emeter"]); code != -1 {
		t.Errorf("err_code = %d, want -1", code)
	}
}

func TestUnsupportedMethod(t *testing.T) {
	plug := startPlug(t)
	reply := send(t, plug.Addr(), `{"system":{"reboot":{"delay":1}}}`)
	if code := errCode(t, methods(t, reply["system"])["reboot"]); code != -2 {
		t.Errorf("err_code = %d, want -2", code)
	}
}

func TestSetRelayStateRejectsBadArguments(t *testing.T) {
	plug := startPlug(t)
	requests := []string{
		`{"system":{"set_relay_state":{}}}`,
		`{"system":{"set_relay_state":null}}`,
		`{"system":{"set_relay_state":{"state":2}}}`,
		`{"system":{"set_relay_state":{"state":"on"}}}`,
	}
	for _, request := range requests {
		reply := send(t, plug.Addr(), request)
		if code := errCode(t, methods(t, reply["system"])["set_relay_state"]); code != -3 {
			t.Errorf("%s: err_code = %d, want -3", request, code)
		}
	}
	if plug.RelayOn() {
		t.Error("relay turned on by a rejected request")
	}
}

func TestSeveralCommandsInOneRequest(t *testing.T) {
	plug := startPlug(t)
	reply := send(t, plug.Addr(),
		`{"system":{"set_relay_state":{"state":1},"get_sysinfo":{}},"time":{"get_time":{}},"emeter":{"get_realtime":{}}}`)

	if code := errCode(t, methods(t, reply["system"])["set_relay_state"]); code != 0 {
		t.Errorf("set_relay_state err_code = %d, want 0", code)
	}
	if code := errCode(t, methods(t, reply["system"])["get_sysinfo"]); code != 0 {
		t.Errorf("get_sysinfo err_code = %d, want 0", code)
	}
	if code := errCode(t, methods(t, reply["time"])["get_time"]); code != 0 {
		t.Errorf("get_time err_code = %d, want 0", code)
	}
	if code := errCode(t, reply["emeter"]); code != -1 {
		t.Errorf("unsupported module err_code = %d, want -1", code)
	}
	if !plug.RelayOn() {
		t.Error("relay is off after set_relay_state 1")
	}
	if got := plug.Requests(); got != 1 {
		t.Errorf("plug counted %d requests, want 1", got)
	}
}

func TestTimeModule(t *testing.T) {
	plug := startPlug(t)
	fixed := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
	plug.SetClock(func() time.Time { return fixed })
	plug.SetTimezoneIndex(7)

	reply := send(t, plug.Addr(), `{"time":{"get_time":{},"get_timezone":{},"set_alarm":{}}}`)
	results := methods(t, reply["time"])

	var got struct {
		Year, Month, Hour int
		Day               int `json:"mday"`
		Minute            int `json:"min"`
		Second            int `json:"sec"`
	}
	if err := json.Unmarshal(results["get_time"], &got); err != nil {
		t.Fatalf("decode get_time: %v", err)
	}
	if got.Year != 2001 || got.Month != 2 || got.Day != 3 || got.Hour != 4 || got.Minute != 5 || got.Second != 6 {
		t.Errorf("get_time = %+v, want 2001-02-03 04:05:06", got)
	}

	var zone struct {
		Index int `json:"index"`
	}
	if err := json.Unmarshal(results["get_timezone"], &zone); err != nil {
		t.Fatalf("decode get_timezone: %v", err)
	}
	if zone.Index != 7 {
		t.Errorf("timezone index = %d, want 7", zone.Index)
	}

	if code := errCode(t, results["set_alarm"]); code != -2 {
		t.Errorf("unknown time method err_code = %d, want -2", code)
	}
}

// fakeClock is a clock that only moves when told to.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestOnTimeCountsFromSwitchOn(t *testing.T) {
	plug := startPlug(t)
	clock := &fakeClock{now: time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)}
	plug.SetClock(clock.Now)

	onTime := func() int {
		t.Helper()
		reply := send(t, plug.Addr(), `{"system":{"get_sysinfo":{}}}`)
		var info struct {
			OnTime int `json:"on_time"`
		}
		if err := json.Unmarshal(methods(t, reply["system"])["get_sysinfo"], &info); err != nil {
			t.Fatalf("decode sysinfo: %v", err)
		}
		return info.OnTime
	}

	if got := onTime(); got != 0 {
		t.Errorf("on_time while off = %d, want 0", got)
	}

	plug.SetRelay(true)
	clock.Advance(90 * time.Second)
	if got := onTime(); got != 90 {
		t.Errorf("on_time 90s after switching on = %d, want 90", got)
	}

	// Switching on again must not restart the count.
	plug.SetRelay(true)
	clock.Advance(10 * time.Second)
	if got := onTime(); got != 100 {
		t.Errorf("on_time after a repeated on = %d, want 100", got)
	}

	plug.SetRelay(false)
	if got := onTime(); got != 0 {
		t.Errorf("on_time after switching off = %d, want 0", got)
	}
}

func TestSeveralRequestsOnOneConnection(t *testing.T) {
	plug := startPlug(t)
	conn, err := net.Dial("tcp", plug.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	for i := 0; i < 3; i++ {
		if err := protocol.WriteMessage(conn, []byte(`{"system":{"get_sysinfo":{}}}`)); err != nil {
			t.Fatalf("request %d: write: %v", i, err)
		}
		if _, err := protocol.ReadMessage(conn); err != nil {
			t.Fatalf("request %d: read: %v", i, err)
		}
	}
}

func TestSetRelayDirectly(t *testing.T) {
	plug := startPlug(t)
	plug.SetRelay(true)

	reply := send(t, plug.Addr(), `{"system":{"get_sysinfo":{}}}`)
	var info struct {
		RelayState int `json:"relay_state"`
	}
	if err := json.Unmarshal(methods(t, reply["system"])["get_sysinfo"], &info); err != nil {
		t.Fatalf("decode sysinfo: %v", err)
	}
	if info.RelayState != 1 {
		t.Errorf("relay_state = %d, want 1", info.RelayState)
	}
}

func TestConcurrentClients(t *testing.T) {
	plug := startPlug(t)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var client protocol.Client
			request := `{"system":{"set_relay_state":{"state":0}}}`
			if i%2 == 0 {
				request = `{"system":{"set_relay_state":{"state":1}}}`
			}
			if _, err := client.Send(context.Background(), plug.Addr(), []byte(request)); err != nil {
				t.Errorf("client %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestCloseDropsOpenConnections(t *testing.T) {
	plug, err := mockplug.New()
	if err != nil {
		t.Fatalf("start mock plug: %v", err)
	}
	conn, err := net.Dial("tcp", plug.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Make sure the plug has picked the connection up before closing it.
	if err := protocol.WriteMessage(conn, []byte(`{"system":{"get_sysinfo":{}}}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := protocol.ReadMessage(conn); err != nil {
		t.Fatalf("read: %v", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- plug.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return while a client connection was open")
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := protocol.ReadMessage(conn); err == nil {
		t.Error("read succeeded on a connection the plug should have dropped")
	}
	if err := plug.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestSetTime(t *testing.T) {
	plug := startPlug(t)
	clock := &fakeClock{now: time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)}
	plug.SetClock(clock.Now)

	reply := send(t, plug.Addr(),
		`{"time":{"set_time":{"year":2010,"month":11,"mday":12,"hour":13,"min":14,"sec":15}}}`)
	if code := errCode(t, methods(t, reply["time"])["set_time"]); code != 0 {
		t.Fatalf("set_time err_code = %d, want 0", code)
	}
	want := time.Date(2010, time.November, 12, 13, 14, 15, 0, time.UTC)
	if got := plug.Time(); !got.Equal(want) {
		t.Errorf("plug time after set_time = %v, want %v", got, want)
	}

	// The clock keeps running from the time it was set to.
	clock.Advance(45 * time.Second)
	if got, want := plug.Time(), want.Add(45*time.Second); !got.Equal(want) {
		t.Errorf("plug time 45s later = %v, want %v", got, want)
	}

	reply = send(t, plug.Addr(), `{"time":{"get_time":{}}}`)
	var got struct {
		Year   int `json:"year"`
		Minute int `json:"min"`
		Second int `json:"sec"`
	}
	if err := json.Unmarshal(methods(t, reply["time"])["get_time"], &got); err != nil {
		t.Fatalf("decode get_time: %v", err)
	}
	if got.Year != 2010 || got.Minute != 15 || got.Second != 0 {
		t.Errorf("get_time after set_time = %+v, want 2010, minute 15, second 0", got)
	}
}

func TestSetTimeRejectsBadArguments(t *testing.T) {
	plug := startPlug(t)
	start := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
	plug.SetClock(func() time.Time { return start })

	requests := []string{
		`{"time":{"set_time":{}}}`,
		`{"time":{"set_time":null}}`,
		`{"time":{"set_time":{"year":2010,"month":11,"mday":12,"hour":13,"min":14}}}`,
		`{"time":{"set_time":{"year":2010,"month":13,"mday":12,"hour":13,"min":14,"sec":15}}}`,
		`{"time":{"set_time":{"year":2010,"month":2,"mday":30,"hour":13,"min":14,"sec":15}}}`,
		`{"time":{"set_time":{"year":2010,"month":11,"mday":12,"hour":24,"min":14,"sec":15}}}`,
		`{"time":{"set_time":{"year":"2010","month":11,"mday":12,"hour":13,"min":14,"sec":15}}}`,
	}
	for _, request := range requests {
		reply := send(t, plug.Addr(), request)
		if code := errCode(t, methods(t, reply["time"])["set_time"]); code != -3 {
			t.Errorf("%s: err_code = %d, want -3", request, code)
		}
	}
	if got := plug.Time(); !got.Equal(start) {
		t.Errorf("a rejected set_time moved the clock to %v", got)
	}
}

// Two made-up rules: on at 07:30 every day, and off at sunset at weekends,
// the second one switched off.
var testRules = []mockplug.Rule{
	{
		ID: "00000000000000000000000000000001", Name: "morning", Enable: 1, Repeat: 1,
		Weekdays: [7]int{1, 1, 1, 1, 1, 1, 1}, StartOption: 0, StartMinute: 450, StartAction: 1, EndAction: -1,
	},
	{
		ID: "00000000000000000000000000000002", Name: "dusk", Enable: 0, Repeat: 1,
		Weekdays: [7]int{1, 0, 0, 0, 0, 0, 1}, StartOption: 2, StartMinute: 0, StartAction: 0, EndAction: -1,
	},
}

func TestGetRules(t *testing.T) {
	plug := startPlug(t)
	plug.SetRules(testRules...)

	reply := send(t, plug.Addr(), `{"schedule":{"get_rules":{}}}`)
	raw := methods(t, reply["schedule"])["get_rules"]
	if code := errCode(t, raw); code != 0 {
		t.Fatalf("err_code = %d, want 0", code)
	}

	// The names here are the ones real hardware uses.
	var got struct {
		Enable  int                          `json:"enable"`
		Version int                          `json:"version"`
		Rules   []map[string]json.RawMessage `json:"rule_list"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if got.Enable != 1 || got.Version != 2 || len(got.Rules) != 2 {
		t.Fatalf("enable %d, version %d, %d rules; want 1, 2 and 2 rules", got.Enable, got.Version, len(got.Rules))
	}
	want := map[string]string{
		"id": `"00000000000000000000000000000001"`, "name": `"morning"`, "enable": "1", "repeat": "1",
		"wday": "[1,1,1,1,1,1,1]", "stime_opt": "0", "smin": "450", "sact": "1", "eact": "-1",
	}
	if len(got.Rules[0]) != len(want) {
		t.Errorf("first rule has %d fields, want %d: %v", len(got.Rules[0]), len(want), got.Rules[0])
	}
	for field, value := range want {
		if string(got.Rules[0][field]) != value {
			t.Errorf("first rule: %s = %s, want %s", field, got.Rules[0][field], value)
		}
	}
}

func TestGetRulesWithNoRules(t *testing.T) {
	plug := startPlug(t)
	plug.SetScheduleEnabled(false)

	reply := send(t, plug.Addr(), `{"schedule":{"get_rules":{}}}`)
	var got struct {
		Enable *int              `json:"enable"`
		Rules  []json.RawMessage `json:"rule_list"`
	}
	raw := methods(t, reply["schedule"])["get_rules"]
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if got.Rules == nil || len(got.Rules) != 0 {
		t.Errorf("rule_list in %s, want an empty list", raw)
	}
	if got.Enable == nil || *got.Enable != 0 {
		t.Errorf("enable in %s, want 0", raw)
	}
}

func TestDeleteRule(t *testing.T) {
	plug := startPlug(t)
	plug.SetRules(testRules...)

	reply := send(t, plug.Addr(), `{"schedule":{"delete_rule":{"id":"00000000000000000000000000000001"}}}`)
	if code := errCode(t, methods(t, reply["schedule"])["delete_rule"]); code != 0 {
		t.Fatalf("err_code = %d, want 0", code)
	}
	left := plug.Rules()
	if len(left) != 1 || left[0].ID != "00000000000000000000000000000002" {
		t.Errorf("rules left: %+v, want only the second", left)
	}
}

func TestDeleteRuleRejectsWhatItCannotDelete(t *testing.T) {
	plug := startPlug(t)
	plug.SetRules(testRules...)

	for _, args := range []string{`{"id":"no-such-rule"}`, `{}`, `{"id":7}`, `[]`} {
		reply := send(t, plug.Addr(), `{"schedule":{"delete_rule":`+args+`}}`)
		if code := errCode(t, methods(t, reply["schedule"])["delete_rule"]); code != -3 {
			t.Errorf("delete_rule %s: err_code = %d, want -3", args, code)
		}
	}
	if left := plug.Rules(); len(left) != 2 {
		t.Errorf("%d rules left, want both", len(left))
	}
}

func TestScheduleMethodsTheMockLacks(t *testing.T) {
	plug := startPlug(t)
	reply := send(t, plug.Addr(), `{"schedule":{"delete_all_rules":{}}}`)
	if code := errCode(t, methods(t, reply["schedule"])["delete_all_rules"]); code != -2 {
		t.Errorf("err_code = %d, want -2", code)
	}
}

// newRule is a complete add_rule argument, as a client sends it: on at 18:05
// on Mondays and Fridays.
const newRule = `{"name":"evening","enable":1,"repeat":1,"wday":[0,1,0,0,0,1,0],` +
	`"stime_opt":0,"smin":1085,"sact":1,"etime_opt":-1,"emin":0,"eact":-1,` +
	`"year":0,"month":0,"day":0,"force":0,"longitude":0,"latitude":0}`

func TestAddRule(t *testing.T) {
	plug := startPlug(t)
	plug.SetRules(testRules...)

	reply := send(t, plug.Addr(), `{"schedule":{"add_rule":`+newRule+`}}`)
	raw := methods(t, reply["schedule"])["add_rule"]
	if code := errCode(t, raw); code != 0 {
		t.Fatalf("err_code = %d, want 0", code)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &added); err != nil || len(added.ID) != 32 {
		t.Fatalf("reply %s does not carry a 32-character id", raw)
	}

	rules := plug.Rules()
	if len(rules) != 3 {
		t.Fatalf("%d rules stored, want 3", len(rules))
	}
	want := mockplug.Rule{
		ID: added.ID, Name: "evening", Enable: 1, Repeat: 1,
		Weekdays: [7]int{0, 1, 0, 0, 0, 1, 0}, StartOption: 0, StartMinute: 1085, StartAction: 1, EndAction: -1,
	}
	if rules[2] != want {
		t.Errorf("stored rule:\n got %+v\nwant %+v", rules[2], want)
	}

	// A second rule gets an id of its own.
	reply = send(t, plug.Addr(), `{"schedule":{"add_rule":`+newRule+`}}`)
	var second struct {
		ID string `json:"id"`
	}
	json.Unmarshal(methods(t, reply["schedule"])["add_rule"], &second)
	if second.ID == "" || second.ID == added.ID {
		t.Errorf("second rule got the id %q, the first %q", second.ID, added.ID)
	}
}

func TestAddRuleRejectsBadArguments(t *testing.T) {
	plug := startPlug(t)
	for name, args := range map[string]string{
		"nothing":          `{}`,
		"not an object":    `[]`,
		"six days":         `{"enable":1,"repeat":1,"wday":[1,1,1,1,1,1],"stime_opt":0,"smin":60,"sact":1}`,
		"a day flag of 2":  `{"enable":1,"repeat":1,"wday":[2,0,0,0,0,0,0],"stime_opt":0,"smin":60,"sact":1}`,
		"no days field":    `{"enable":1,"repeat":1,"stime_opt":0,"smin":60,"sact":1}`,
		"minute too large": `{"enable":1,"repeat":1,"wday":[1,0,0,0,0,0,0],"stime_opt":0,"smin":1440,"sact":1}`,
		"no action":        `{"enable":1,"repeat":1,"wday":[1,0,0,0,0,0,0],"stime_opt":0,"smin":60}`,
		"action of 5":      `{"enable":1,"repeat":1,"wday":[1,0,0,0,0,0,0],"stime_opt":0,"smin":60,"sact":5}`,
		"enable of 3":      `{"enable":3,"repeat":1,"wday":[1,0,0,0,0,0,0],"stime_opt":0,"smin":60,"sact":1}`,
	} {
		reply := send(t, plug.Addr(), `{"schedule":{"add_rule":`+args+`}}`)
		if code := errCode(t, methods(t, reply["schedule"])["add_rule"]); code != -3 {
			t.Errorf("%s: err_code = %d, want -3", name, code)
		}
	}
	if stored := plug.Rules(); len(stored) != 0 {
		t.Errorf("%d rules stored from bad requests", len(stored))
	}
}

func TestEditRule(t *testing.T) {
	plug := startPlug(t)
	plug.SetRules(testRules...)

	change := `{"id":"00000000000000000000000000000001",` + newRule[1:]
	reply := send(t, plug.Addr(), `{"schedule":{"edit_rule":`+change+`}}`)
	if code := errCode(t, methods(t, reply["schedule"])["edit_rule"]); code != 0 {
		t.Fatalf("err_code = %d, want 0", code)
	}
	rules := plug.Rules()
	want := mockplug.Rule{
		ID: "00000000000000000000000000000001", Name: "evening", Enable: 1, Repeat: 1,
		Weekdays: [7]int{0, 1, 0, 0, 0, 1, 0}, StartOption: 0, StartMinute: 1085, StartAction: 1, EndAction: -1,
	}
	if len(rules) != 2 || rules[0] != want || rules[1] != testRules[1] {
		t.Errorf("rules after the edit: %+v", rules)
	}

	// A rule the plug does not have cannot be changed.
	for _, args := range []string{`{"id":"no-such-rule",` + newRule[1:], newRule} {
		reply := send(t, plug.Addr(), `{"schedule":{"edit_rule":`+args+`}}`)
		if code := errCode(t, methods(t, reply["schedule"])["edit_rule"]); code != -3 {
			t.Errorf("edit_rule %s: err_code = %d, want -3", args, code)
		}
	}
}

func TestForgetfulPlugStoresNothing(t *testing.T) {
	plug := startPlug(t)
	plug.SetRules(testRules...)
	plug.SetForgetful(true)

	for method, args := range map[string]string{
		"add_rule":  newRule,
		"edit_rule": `{"id":"00000000000000000000000000000001",` + newRule[1:],
	} {
		reply := send(t, plug.Addr(), `{"schedule":{"`+method+`":`+args+`}}`)
		if code := errCode(t, methods(t, reply["schedule"])[method]); code != 0 {
			t.Errorf("%s: err_code = %d, want 0", method, code)
		}
	}
	rules := plug.Rules()
	if len(rules) != 2 || rules[0] != testRules[0] || rules[1] != testRules[1] {
		t.Errorf("a forgetful plug changed its rules: %+v", rules)
	}
}
