package plug

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gureetk/kasad/internal/mockplug"
)

func startMock(t *testing.T) *mockplug.Plug {
	t.Helper()
	mock, err := mockplug.New()
	if err != nil {
		t.Fatalf("start mock plug: %v", err)
	}
	t.Cleanup(func() { mock.Close() })
	return mock
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

func TestNewAddr(t *testing.T) {
	cases := map[string]string{
		"192.0.2.10":         "192.0.2.10:9999",
		"192.0.2.10:1234":    "192.0.2.10:1234",
		"plug.example":       "plug.example:9999",
		"plug.example:1234":  "plug.example:1234",
		"2001:db8::1":        "[2001:db8::1]:9999",
		"[2001:db8::1]":      "[2001:db8::1]:9999",
		"[2001:db8::1]:1234": "[2001:db8::1]:1234",
	}
	for host, want := range cases {
		if got := New(host).Addr; got != want {
			t.Errorf("New(%q).Addr = %q, want %q", host, got, want)
		}
	}
}

func TestSysinfo(t *testing.T) {
	mock := startMock(t)
	info, err := New(mock.Addr()).Sysinfo(context.Background())
	if err != nil {
		t.Fatalf("Sysinfo: %v", err)
	}

	want := Sysinfo{
		Alias:           "Mock Plug",
		DeviceName:      "Smart Wi-Fi Plug Lite",
		Model:           "HS103(US)",
		HardwareVersion: "1.0",
		SoftwareVersion: "0.0.0 Build 000000 Rel.000000",
		MAC:             "02:00:00:00:00:01",
		DeviceID:        "0000000000000000000000000000000000000000",
		RSSI:            -50,
	}
	if info != want {
		t.Errorf("Sysinfo\n got %+v\nwant %+v", info, want)
	}
	if info.On() {
		t.Error("On() = true for a plug that is off")
	}
	if !info.LEDOn() {
		t.Error("LEDOn() = false, want true")
	}
}

func TestSetRelay(t *testing.T) {
	mock := startMock(t)
	p := New(mock.Addr())
	ctx := context.Background()

	for _, on := range []bool{true, false, true} {
		if err := p.SetRelay(ctx, on); err != nil {
			t.Fatalf("SetRelay(%v): %v", on, err)
		}
		if got := mock.RelayOn(); got != on {
			t.Errorf("after SetRelay(%v): mock relay on = %v", on, got)
		}
		info, err := p.Sysinfo(ctx)
		if err != nil {
			t.Fatalf("Sysinfo: %v", err)
		}
		if info.On() != on {
			t.Errorf("after SetRelay(%v): Sysinfo.On() = %v", on, info.On())
		}
	}
}

func TestSysinfoOnFor(t *testing.T) {
	mock := startMock(t)
	clock := &fakeClock{now: time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)}
	mock.SetClock(clock.Now)
	p := New(mock.Addr())
	ctx := context.Background()

	if err := p.SetRelay(ctx, true); err != nil {
		t.Fatalf("SetRelay: %v", err)
	}
	clock.Advance(75 * time.Second)

	info, err := p.Sysinfo(ctx)
	if err != nil {
		t.Fatalf("Sysinfo: %v", err)
	}
	if got, want := info.OnFor(), 75*time.Second; got != want {
		t.Errorf("OnFor() = %v, want %v", got, want)
	}
}

func TestClock(t *testing.T) {
	mock := startMock(t)
	fixed := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
	mock.SetClock(func() time.Time { return fixed })
	mock.SetTimezoneIndex(7)

	clock, err := New(mock.Addr()).Clock(context.Background())
	if err != nil {
		t.Fatalf("Clock: %v", err)
	}
	want := Clock{Year: 2001, Month: 2, Day: 3, Hour: 4, Minute: 5, Second: 6, ZoneIndex: 7}
	if clock != want {
		t.Errorf("Clock = %+v, want %+v", clock, want)
	}
	if got := clock.In(time.UTC); !got.Equal(fixed) {
		t.Errorf("Clock.In(UTC) = %v, want %v", got, fixed)
	}

	// The same reading in another zone is a different instant.
	zone := time.FixedZone("test", -5*60*60)
	if got, want := clock.In(zone), fixed.Add(5*time.Hour); !got.Equal(want) {
		t.Errorf("Clock.In(UTC-5) = %v, want %v", got, want)
	}
}

func TestStatusUsesOneExchange(t *testing.T) {
	mock := startMock(t)
	fixed := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
	mock.SetClock(func() time.Time { return fixed })
	mock.SetTimezoneIndex(7)
	mock.SetRelay(true)

	status, err := New(mock.Addr()).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := mock.Requests(); got != 1 {
		t.Errorf("Status made %d requests, want 1", got)
	}
	if status.Sysinfo.Alias != "Mock Plug" || !status.Sysinfo.On() {
		t.Errorf("Status.Sysinfo = %+v", status.Sysinfo)
	}
	wantClock := Clock{Year: 2001, Month: 2, Day: 3, Hour: 4, Minute: 5, Second: 6, ZoneIndex: 7}
	if status.Clock != wantClock {
		t.Errorf("Status.Clock = %+v, want %+v", status.Clock, wantClock)
	}
}

func TestDeviceErrorsOverTheWire(t *testing.T) {
	mock := startMock(t)
	p := New(mock.Addr())
	ctx := context.Background()

	cases := []struct {
		name string
		cmd  command
		want Error
	}{
		{
			name: "unknown method",
			cmd:  command{module: "system", method: "reboot"},
			want: Error{Module: "system", Method: "reboot", Code: -2, Message: "member not support"},
		},
		{
			name: "unknown module",
			cmd:  command{module: "emeter", method: "get_realtime"},
			want: Error{Module: "emeter", Code: -1, Message: "module not support"},
		},
		{
			name: "bad argument",
			cmd:  command{module: "system", method: "set_relay_state", params: map[string]int{"state": 5}},
			want: Error{Module: "system", Method: "set_relay_state", Code: -3, Message: "invalid argument"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := p.call(ctx, tc.cmd)
			var deviceErr *Error
			if !errors.As(err, &deviceErr) {
				t.Fatalf("call error = %v, want an *Error", err)
			}
			if *deviceErr != tc.want {
				t.Errorf("device error\n got %+v\nwant %+v", *deviceErr, tc.want)
			}
		})
	}
}

func TestDecodeResultRejectsMalformedReplies(t *testing.T) {
	cases := map[string]string{
		"no module section":    `{}`,
		"section not object":   `{"system":5}`,
		"no result, no status": `{"system":{}}`,
		"result not object":    `{"system":{"get_sysinfo":5}}`,
		"wrong field type":     `{"system":{"get_sysinfo":{"err_code":0,"alias":5}}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			var reply map[string]json.RawMessage
			if err := json.Unmarshal([]byte(raw), &reply); err != nil {
				t.Fatalf("test reply is not valid JSON: %v", err)
			}
			var info Sysinfo
			err := decodeResult(reply, command{module: "system", method: "get_sysinfo", result: &info})
			if err == nil {
				t.Fatal("decodeResult succeeded, want an error")
			}
			var deviceErr *Error
			if errors.As(err, &deviceErr) {
				t.Errorf("decodeResult returned a device error for a malformed reply: %v", err)
			}
		})
	}
}

func TestErrorMessage(t *testing.T) {
	cases := []struct {
		err  Error
		want string
	}{
		{
			Error{Module: "system", Method: "reboot", Code: -2, Message: "member not support"},
			"plug: device rejected system.reboot: member not support (code -2)",
		},
		{
			Error{Module: "emeter", Code: -1, Message: "module not support"},
			"plug: device rejected emeter: module not support (code -1)",
		},
		{
			Error{Module: "system", Method: "set_relay_state", Code: -3},
			"plug: device rejected system.set_relay_state (code -3)",
		},
	}
	for _, tc := range cases {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
	}
}

func TestNetworkFailureIsNotADeviceError(t *testing.T) {
	// Take a free port, then release it so that nothing is listening there.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	_, err = New(addr).Sysinfo(context.Background())
	if err == nil {
		t.Fatal("Sysinfo against a closed port succeeded")
	}
	var deviceErr *Error
	if errors.As(err, &deviceErr) {
		t.Errorf("network failure reported as a device error: %v", err)
	}
}

func TestSetClock(t *testing.T) {
	mock := startMock(t)
	start := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
	mock.SetClock(func() time.Time { return start })
	mock.SetTimezoneIndex(7)
	p := New(mock.Addr())
	ctx := context.Background()

	// Sub-second precision is dropped: the device keeps whole seconds.
	target := time.Date(2010, time.November, 12, 13, 14, 15, 987654321, time.UTC)
	if err := p.SetClock(ctx, target); err != nil {
		t.Fatalf("SetClock: %v", err)
	}
	want := target.Truncate(time.Second)
	if got := mock.Time(); !got.Equal(want) {
		t.Errorf("mock time after SetClock = %v, want %v", got, want)
	}

	clock, err := p.Clock(ctx)
	if err != nil {
		t.Fatalf("Clock: %v", err)
	}
	if got := clock.In(time.UTC); !got.Equal(want) {
		t.Errorf("Clock after SetClock = %v, want %v", got, want)
	}
	if clock.ZoneIndex != 7 {
		t.Errorf("SetClock changed the timezone index to %d", clock.ZoneIndex)
	}
}

func TestSetClockSendsWallClockFields(t *testing.T) {
	mock := startMock(t)
	start := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
	mock.SetClock(func() time.Time { return start })

	// 13:14:15 in a zone five hours behind UTC. The device is sent 13:14:15,
	// not the UTC equivalent.
	zone := time.FixedZone("test", -5*60*60)
	target := time.Date(2010, time.November, 12, 13, 14, 15, 0, zone)
	if err := New(mock.Addr()).SetClock(context.Background(), target); err != nil {
		t.Fatalf("SetClock: %v", err)
	}
	got := mock.Time()
	if got.Hour() != 13 || got.Minute() != 14 || got.Second() != 15 || got.Day() != 12 {
		t.Errorf("mock wall clock after SetClock = %v, want 2010-11-12 13:14:15", got)
	}
}

func TestSwitch(t *testing.T) {
	mock := startMock(t)
	p := New(mock.Addr())
	ctx := context.Background()

	for _, on := range []bool{true, false} {
		info, err := p.Switch(ctx, on)
		if err != nil {
			t.Fatalf("Switch(%v): %v", on, err)
		}
		if info.On() != on || mock.RelayOn() != on {
			t.Errorf("after Switch(%v): reported on = %v, mock relay on = %v", on, info.On(), mock.RelayOn())
		}
		if info.Alias != "Mock Plug" {
			t.Errorf("Switch returned sysinfo with alias %q", info.Alias)
		}
	}
}

func TestSwitchNoticesARelayThatDoesNotMove(t *testing.T) {
	mock := startMock(t)
	mock.SetStuck(true)

	info, err := New(mock.Addr()).Switch(context.Background(), true)
	if !errors.Is(err, ErrNotSwitched) {
		t.Fatalf("Switch on a stuck relay = %v, want ErrNotSwitched", err)
	}
	if info.On() {
		t.Error("Switch reported the relay on although it did not move")
	}
	var deviceErr *Error
	if errors.As(err, &deviceErr) {
		t.Errorf("a relay that did not move was reported as a device error: %v", err)
	}
}

// Three made-up rules: on at 07:30 on weekdays; off at sunset at weekends,
// switched off; and one that runs once, on at 21:05 and off again at 22:00.
var testRules = []mockplug.Rule{
	{
		ID: "00000000000000000000000000000001", Name: "morning", Enable: 1, Repeat: 1,
		Weekdays: [7]int{0, 1, 1, 1, 1, 1, 0}, StartOption: 0, StartMinute: 450, StartAction: 1, EndAction: -1,
	},
	{
		ID: "00000000000000000000000000000002", Name: "dusk", Enable: 0, Repeat: 1,
		Weekdays: [7]int{1, 0, 0, 0, 0, 0, 1}, StartOption: 2, StartMinute: 0, StartAction: 0, EndAction: -1,
	},
	{
		ID: "00000000000000000000000000000003", Name: "", Enable: 1, Repeat: 0,
		Weekdays: [7]int{0, 0, 0, 1, 0, 0, 0}, StartOption: 0, StartMinute: 1265, StartAction: 1, EndAction: 0,
	},
}

func TestRules(t *testing.T) {
	mock := startMock(t)
	mock.SetRules(testRules...)

	rules, err := New(mock.Addr()).Rules(context.Background())
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if !rules.Enabled {
		t.Error("the schedule as a whole is reported as off")
	}

	want := []Rule{
		{
			ID: "00000000000000000000000000000001", Name: "morning", Enabled: true, Repeat: true,
			Days: [7]bool{time.Monday: true, time.Tuesday: true, time.Wednesday: true, time.Thursday: true, time.Friday: true},
			Time: AtClockTime, Minute: 450, Action: RuleOn,
			EndAction: RuleNone, EndTime: NoTime,
		},
		{
			ID: "00000000000000000000000000000002", Name: "dusk", Enabled: false, Repeat: true,
			Days: [7]bool{time.Sunday: true, time.Saturday: true},
			Time: AtSunset, Action: RuleOff,
			EndAction: RuleNone, EndTime: NoTime,
		},
		{
			ID: "00000000000000000000000000000003", Enabled: true, Repeat: false,
			Days: [7]bool{time.Wednesday: true},
			Time: AtClockTime, Minute: 1265, Action: RuleOn,
			EndAction: RuleOff, EndTime: NoTime,
		},
	}
	if len(rules.List) != len(want) {
		t.Fatalf("got %d rules, want %d: %+v", len(rules.List), len(want), rules.List)
	}
	for i := range want {
		if rules.List[i] != want[i] {
			t.Errorf("rule %d:\n got %+v\nwant %+v", i, rules.List[i], want[i])
		}
	}
}

func TestRulesWhenThereAreNone(t *testing.T) {
	mock := startMock(t)
	mock.SetScheduleEnabled(false)

	rules, err := New(mock.Addr()).Rules(context.Background())
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if rules.Enabled || len(rules.List) != 0 {
		t.Errorf("got %+v, want a schedule that is off and empty", rules)
	}
}

func TestRuleWeekly(t *testing.T) {
	weekly := Rule{
		Enabled: true, Repeat: true, Days: [7]bool{time.Monday: true},
		Time: AtClockTime, Minute: 450, Action: RuleOn, EndAction: RuleNone, EndTime: NoTime,
	}
	if !weekly.Weekly() {
		t.Errorf("a plain weekly rule is not reported as one: %+v", weekly)
	}
	// Being switched off does not change what kind of rule it is.
	paused := weekly
	paused.Enabled = false
	if !paused.Weekly() {
		t.Error("a weekly rule that is switched off is not reported as weekly")
	}

	for name, change := range map[string]func(*Rule){
		"runs once":           func(r *Rule) { r.Repeat = false },
		"no days":             func(r *Rule) { r.Days = [7]bool{} },
		"at sunrise":          func(r *Rule) { r.Time = AtSunrise },
		"no time":             func(r *Rule) { r.Time = NoTime },
		"minute out of range": func(r *Rule) { r.Minute = 1440 },
		"negative minute":     func(r *Rule) { r.Minute = -1 },
		"no action":           func(r *Rule) { r.Action = RuleNone },
		"an action not known": func(r *Rule) { r.Action = 2 },
		"a second action too": func(r *Rule) { r.EndAction = RuleOff },
	} {
		rule := weekly
		change(&rule)
		if rule.Weekly() {
			t.Errorf("%s: reported as a plain weekly rule: %+v", name, rule)
		}
	}
}

func TestDeleteRule(t *testing.T) {
	mock := startMock(t)
	mock.SetRules(testRules...)

	err := New(mock.Addr()).DeleteRule(context.Background(), "00000000000000000000000000000002")
	if err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	left := mock.Rules()
	if len(left) != 2 || left[0].ID != testRules[0].ID || left[1].ID != testRules[2].ID {
		t.Errorf("rules left on the plug: %+v, want the first and third", left)
	}
}

func TestDeleteRuleThatIsNotThere(t *testing.T) {
	mock := startMock(t)
	mock.SetRules(testRules...)

	for _, id := range []string{"no-such-rule", ""} {
		before := mock.Requests()
		err := New(mock.Addr()).DeleteRule(context.Background(), id)
		if !errors.Is(err, ErrNoSuchRule) {
			t.Errorf("DeleteRule(%q) = %v, want ErrNoSuchRule", id, err)
		}
		// The plug is asked what it has, and nothing more.
		if sent := mock.Requests() - before; sent != 1 {
			t.Errorf("DeleteRule(%q) sent %d requests, want 1", id, sent)
		}
	}
	if left := mock.Rules(); len(left) != len(testRules) {
		t.Errorf("%d rules left on the plug, want all %d", len(left), len(testRules))
	}
}

func TestRulesOnAnUnreachablePlug(t *testing.T) {
	mock := startMock(t)
	addr := mock.Addr()
	mock.Close()

	p := New(addr)
	p.Client.Timeout = time.Second
	if _, err := p.Rules(context.Background()); err == nil {
		t.Error("Rules succeeded against a plug that is not there")
	}
	var deviceError *Error
	if err := p.DeleteRule(context.Background(), "anything"); err == nil || errors.As(err, &deviceError) || errors.Is(err, ErrNoSuchRule) {
		t.Errorf("DeleteRule against a plug that is not there = %v, want a network error", err)
	}
}

// evening is a rule to add: on at 18:05 on Mondays and Fridays.
var evening = WeeklyRule{
	Name: "evening", Enabled: true,
	Days:   [7]bool{time.Monday: true, time.Friday: true},
	Minute: 1085, Action: RuleOn,
}

func TestWeeklyRuleOnTheWire(t *testing.T) {
	// Every field is sent, including the ones a plain weekly rule has no
	// use for, since that is what these devices are documented to expect.
	const want = `{"name":"evening","enable":1,"repeat":1,"wday":[0,1,0,0,0,1,0],` +
		`"stime_opt":0,"smin":1085,"sact":1,"etime_opt":-1,"emin":0,"eact":-1,` +
		`"year":0,"month":0,"day":0,"force":0,"longitude":0,"latitude":0}`
	got, err := json.Marshal(evening.wire(""))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("add_rule argument:\n got %s\nwant %s", got, want)
	}

	off := WeeklyRule{Days: [7]bool{time.Sunday: true}, Minute: 0, Action: RuleOff}
	got, err = json.Marshal(off.wire("AB12"))
	if err != nil {
		t.Fatal(err)
	}
	const wantEdit = `{"id":"AB12","name":"","enable":0,"repeat":1,"wday":[1,0,0,0,0,0,0],` +
		`"stime_opt":0,"smin":0,"sact":0,"etime_opt":-1,"emin":0,"eact":-1,` +
		`"year":0,"month":0,"day":0,"force":0,"longitude":0,"latitude":0}`
	if string(got) != wantEdit {
		t.Errorf("edit_rule argument:\n got %s\nwant %s", got, wantEdit)
	}
}

func TestAddRule(t *testing.T) {
	mock := startMock(t)
	mock.SetRules(testRules...)

	stored, err := New(mock.Addr()).AddRule(context.Background(), evening)
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	want := Rule{
		ID: stored.ID, Name: "evening", Enabled: true, Repeat: true,
		Days: [7]bool{time.Monday: true, time.Friday: true},
		Time: AtClockTime, Minute: 1085, Action: RuleOn,
		EndAction: RuleNone, EndTime: NoTime,
	}
	if stored != want || len(stored.ID) != 32 {
		t.Errorf("AddRule returned:\n got %+v\nwant %+v with a 32-character id", stored, want)
	}

	rules := mock.Rules()
	if len(rules) != len(testRules)+1 {
		t.Fatalf("the plug has %d rules, want %d", len(rules), len(testRules)+1)
	}
	last := rules[len(rules)-1]
	if last.ID != stored.ID || last.Weekdays != [7]int{0, 1, 0, 0, 0, 1, 0} || last.StartMinute != 1085 || last.StartAction != 1 {
		t.Errorf("the plug stored: %+v", last)
	}
}

func TestAddRuleRefusesWhatIsNotARule(t *testing.T) {
	mock := startMock(t)
	p := New(mock.Addr())

	for name, change := range map[string]func(*WeeklyRule){
		"no days":         func(r *WeeklyRule) { r.Days = [7]bool{} },
		"negative minute": func(r *WeeklyRule) { r.Minute = -1 },
		"minute too late": func(r *WeeklyRule) { r.Minute = 1440 },
		"no action":       func(r *WeeklyRule) { r.Action = RuleNone },
	} {
		rule := evening
		change(&rule)
		if _, err := p.AddRule(context.Background(), rule); err == nil {
			t.Errorf("%s: AddRule accepted it", name)
		}
		if _, err := p.EditRule(context.Background(), "any", rule); err == nil {
			t.Errorf("%s: EditRule accepted it", name)
		}
	}
	if mock.Requests() != 0 {
		t.Errorf("%d requests were sent for rules that are not valid", mock.Requests())
	}
}

func TestAddRuleNoticesAPlugThatDoesNotStoreIt(t *testing.T) {
	mock := startMock(t)
	mock.SetForgetful(true)

	_, err := New(mock.Addr()).AddRule(context.Background(), evening)
	if !errors.Is(err, ErrNotStored) {
		t.Errorf("AddRule = %v, want ErrNotStored", err)
	}
}

func TestEditRule(t *testing.T) {
	mock := startMock(t)
	mock.SetRules(testRules...)

	// Switch the first rule off, move it and make it an "off" rule.
	change := WeeklyRule{Name: "later", Enabled: false, Days: [7]bool{time.Sunday: true}, Minute: 600, Action: RuleOff}
	stored, err := New(mock.Addr()).EditRule(context.Background(), testRules[0].ID, change)
	if err != nil {
		t.Fatalf("EditRule: %v", err)
	}
	want := Rule{
		ID: testRules[0].ID, Name: "later", Enabled: false, Repeat: true,
		Days: [7]bool{time.Sunday: true},
		Time: AtClockTime, Minute: 600, Action: RuleOff,
		EndAction: RuleNone, EndTime: NoTime,
	}
	if stored != want {
		t.Errorf("EditRule returned:\n got %+v\nwant %+v", stored, want)
	}
	rules := mock.Rules()
	if len(rules) != len(testRules) || rules[0].StartMinute != 600 || rules[0].Enable != 0 || rules[1] != testRules[1] {
		t.Errorf("the plug's rules after the edit: %+v", rules)
	}
}

func TestEditRuleLeavesOtherKindsAlone(t *testing.T) {
	mock := startMock(t)
	mock.SetRules(testRules...)
	p := New(mock.Addr())

	cases := []struct {
		name string
		id   string
		want error
	}{
		{"a rule that is not there", "no-such-rule", ErrNoSuchRule},
		{"a sunset rule", testRules[1].ID, ErrNotWeekly},
		{"a rule that runs once", testRules[2].ID, ErrNotWeekly},
	}
	for _, tc := range cases {
		before := mock.Requests()
		_, err := p.EditRule(context.Background(), tc.id, evening)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: EditRule = %v, want %v", tc.name, err, tc.want)
		}
		// The plug is asked what it has, and nothing more.
		if sent := mock.Requests() - before; sent != 1 {
			t.Errorf("%s: EditRule sent %d requests, want 1", tc.name, sent)
		}
	}
	rules := mock.Rules()
	for i := range testRules {
		if rules[i] != testRules[i] {
			t.Errorf("rule %d was changed: %+v", i, rules[i])
		}
	}
}

func TestEditRuleNoticesAPlugThatDoesNotStoreIt(t *testing.T) {
	mock := startMock(t)
	mock.SetRules(testRules...)
	mock.SetForgetful(true)

	stored, err := New(mock.Addr()).EditRule(context.Background(), testRules[0].ID, evening)
	if !errors.Is(err, ErrNotStored) {
		t.Errorf("EditRule = %v, want ErrNotStored", err)
	}
	// What the plug does hold comes back with the error.
	if stored.ID != testRules[0].ID || stored.Minute != 450 {
		t.Errorf("EditRule returned %+v alongside the error, want the rule as the plug has it", stored)
	}
}
