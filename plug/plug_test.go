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
