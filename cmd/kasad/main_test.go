package main

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gureetk/kasad/internal/mockplug"
)

// testNow is the moment the tests pretend it is.
var testNow = time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)

func startMock(t *testing.T) *mockplug.Plug {
	t.Helper()
	mock, err := mockplug.New()
	if err != nil {
		t.Fatalf("start mock plug: %v", err)
	}
	t.Cleanup(func() { mock.Close() })
	mock.SetClock(func() time.Time { return testNow })
	return mock
}

// result is what one invocation of the command produced.
type result struct {
	code   int
	stdout string
	stderr string
}

// kasad runs the command with the given environment variables and arguments.
func kasad(t *testing.T, vars map[string]string, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, environment{
		stdout: &stdout,
		stderr: &stderr,
		getenv: func(name string) string { return vars[name] },
		now:    func() time.Time { return testNow },
	})
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func TestState(t *testing.T) {
	mock := startMock(t)
	mock.SetTimezoneIndex(7)

	got := kasad(t, nil, "-host", mock.Addr(), "state")
	if got.code != exitOK {
		t.Fatalf("exit code = %d, stderr: %s", got.code, got.stderr)
	}
	want := `Mock Plug (HS103(US))
  State:     off
  Clock:     2001-02-03 04:05:06, in step with this machine
  Timezone:  index 7
  Hardware:  1.0
  Firmware:  0.0.0 Build 000000 Rel.000000
  MAC:       02:00:00:00:00:01
  Signal:    -50 dBm
  LED:       on
`
	if got.stdout != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", got.stdout, want)
	}
	if got.stderr != "" {
		t.Errorf("unexpected stderr: %s", got.stderr)
	}
}

func TestStateShowsHowLongThePlugHasBeenOn(t *testing.T) {
	mock := startMock(t)
	mock.SetClock(func() time.Time { return testNow.Add(-90 * time.Second) })
	mock.SetRelay(true)
	mock.SetClock(func() time.Time { return testNow })

	got := kasad(t, nil, "-host", mock.Addr(), "state")
	if got.code != exitOK {
		t.Fatalf("exit code = %d, stderr: %s", got.code, got.stderr)
	}
	if want := "  State:     on for 1m30s\n"; !strings.Contains(got.stdout, want) {
		t.Errorf("stdout has no line %q:\n%s", want, got.stdout)
	}
}

func TestStateReportsClockDrift(t *testing.T) {
	cases := []struct {
		offset time.Duration
		want   string
	}{
		{0, "in step with this machine"},
		{-1 * time.Second, "in step with this machine"},
		{1 * time.Second, "in step with this machine"},
		{-2 * time.Second, "2s behind this machine"},
		{90 * time.Minute, "1h30m0s ahead of this machine"},
		{47 * time.Hour, "47h0m0s ahead of this machine"},
		{-48 * time.Hour, "2 days behind this machine"},
		{-26 * 365 * 24 * time.Hour, "9490 days behind this machine"},
	}
	for _, tc := range cases {
		t.Run(tc.offset.String(), func(t *testing.T) {
			mock := startMock(t)
			mock.SetClock(func() time.Time { return testNow.Add(tc.offset) })

			got := kasad(t, nil, "-host", mock.Addr(), "state")
			if got.code != exitOK {
				t.Fatalf("exit code = %d, stderr: %s", got.code, got.stderr)
			}
			if !strings.Contains(got.stdout, ", "+tc.want+"\n") {
				t.Errorf("stdout does not say %q:\n%s", tc.want, got.stdout)
			}
		})
	}
}

func TestOnAndOff(t *testing.T) {
	mock := startMock(t)

	got := kasad(t, nil, "-host", mock.Addr(), "on")
	if got.code != exitOK {
		t.Fatalf("on: exit code = %d, stderr: %s", got.code, got.stderr)
	}
	if got.stdout != "Mock Plug is on\n" {
		t.Errorf("on: stdout = %q", got.stdout)
	}
	if !mock.RelayOn() {
		t.Error("on: the plug's relay is still off")
	}

	got = kasad(t, nil, "-host", mock.Addr(), "off")
	if got.code != exitOK {
		t.Fatalf("off: exit code = %d, stderr: %s", got.code, got.stderr)
	}
	if got.stdout != "Mock Plug is off\n" {
		t.Errorf("off: stdout = %q", got.stdout)
	}
	if mock.RelayOn() {
		t.Error("off: the plug's relay is still on")
	}
}

func TestHostFromEnvironment(t *testing.T) {
	mock := startMock(t)

	got := kasad(t, map[string]string{hostEnv: mock.Addr()}, "on")
	if got.code != exitOK {
		t.Fatalf("exit code = %d, stderr: %s", got.code, got.stderr)
	}
	if !mock.RelayOn() {
		t.Error("the plug named by the environment variable was not switched on")
	}
}

func TestHostFlagWinsOverEnvironment(t *testing.T) {
	fromFlag := startMock(t)
	fromEnv := startMock(t)

	got := kasad(t, map[string]string{hostEnv: fromEnv.Addr()}, "-host", fromFlag.Addr(), "on")
	if got.code != exitOK {
		t.Fatalf("exit code = %d, stderr: %s", got.code, got.stderr)
	}
	if !fromFlag.RelayOn() {
		t.Error("the plug named by -host was not switched on")
	}
	if fromEnv.RelayOn() {
		t.Error("the plug named by the environment variable was switched on")
	}
}

func TestUsageErrors(t *testing.T) {
	mock := startMock(t)

	cases := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{"no command", []string{"-host", mock.Addr()}, "no command given"},
		{"unknown command", []string{"-host", mock.Addr(), "toggle"}, `unknown command "toggle"`},
		{"too many commands", []string{"-host", mock.Addr(), "on", "off"}, "expected one command"},
		{"no host", []string{"state"}, "no plug address"},
		{"unknown flag", []string{"-colour", "state"}, "flag provided but not defined"},
		{"zero timeout", []string{"-host", mock.Addr(), "-timeout", "0s", "state"}, "-timeout must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := kasad(t, nil, tc.args...)
			if got.code != exitUsage {
				t.Errorf("exit code = %d, want %d", got.code, exitUsage)
			}
			if !strings.Contains(got.stderr, tc.wantStderr) {
				t.Errorf("stderr does not mention %q:\n%s", tc.wantStderr, got.stderr)
			}
			if got.stdout != "" {
				t.Errorf("unexpected stdout: %s", got.stdout)
			}
		})
	}
	if mock.Requests() != 0 {
		t.Errorf("a usage error still sent %d requests to the plug", mock.Requests())
	}
}

func TestHelp(t *testing.T) {
	got := kasad(t, nil, "-h")
	if got.code != exitOK {
		t.Errorf("exit code = %d, want %d", got.code, exitOK)
	}
	for _, want := range []string{"Usage: kasad", "state", "sync", "-host", "-timeout", hostEnv} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("help text does not mention %q:\n%s", want, got.stderr)
		}
	}
}

func TestUnreachablePlug(t *testing.T) {
	// Take a free port, then release it so that nothing is listening there.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	got := kasad(t, nil, "-host", addr, "-timeout", "2s", "state")
	if got.code != exitFailed {
		t.Errorf("exit code = %d, want %d", got.code, exitFailed)
	}
	if !strings.HasPrefix(got.stderr, "kasad: ") {
		t.Errorf("stderr = %q, want an error prefixed with the command name", got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("unexpected stdout: %s", got.stdout)
	}
}

func TestSync(t *testing.T) {
	mock := startMock(t)
	mock.SetClock(func() time.Time { return testNow.Add(-5 * time.Minute) })

	got := kasad(t, nil, "-host", mock.Addr(), "sync")
	if got.code != exitOK {
		t.Fatalf("exit code = %d, stderr: %s", got.code, got.stderr)
	}
	want := "Mock Plug clock set to 2001-02-03 04:05:06 (was 5m0s behind this machine)\n"
	if got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
	if plugTime := mock.Time(); !plugTime.Equal(testNow) {
		t.Errorf("plug clock after sync = %v, want %v", plugTime, testNow)
	}

	state := kasad(t, nil, "-host", mock.Addr(), "state")
	if !strings.Contains(state.stdout, "in step with this machine") {
		t.Errorf("state after sync does not report the clock in step:\n%s", state.stdout)
	}
}

func TestSyncWhenAlreadyInStep(t *testing.T) {
	mock := startMock(t)

	got := kasad(t, nil, "-host", mock.Addr(), "sync")
	if got.code != exitOK {
		t.Fatalf("exit code = %d, stderr: %s", got.code, got.stderr)
	}
	want := "Mock Plug clock set to 2001-02-03 04:05:06 (was in step with this machine)\n"
	if got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
}

func TestSyncRoundsToTheNearestSecond(t *testing.T) {
	mock := startMock(t)
	now := testNow.Add(700 * time.Millisecond)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-host", mock.Addr(), "sync"}, environment{
		stdout: &stdout,
		stderr: &stderr,
		getenv: func(string) string { return "" },
		now:    func() time.Time { return now },
	})
	if code != exitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr.String())
	}
	if want := testNow.Add(time.Second); !mock.Time().Equal(want) {
		t.Errorf("plug clock after sync = %v, want %v", mock.Time(), want)
	}
}
