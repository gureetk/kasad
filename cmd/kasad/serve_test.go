package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gureetk/kasad/internal/mockplug"
	"github.com/gureetk/kasad/schedule"
)

// testClock is a clock the test moves by hand.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// syncBuffer is a bytes.Buffer that is safe to read while it is written to.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// serving is a `kasad serve` running in the background.
type serving struct {
	t      *testing.T
	clock  *testClock
	dir    string
	stdout *syncBuffer
	stderr *syncBuffer
	cancel context.CancelFunc
	done   chan int
}

// startServe runs `kasad serve` against mock with the given data directory
// and extra flags. The clock starts at testNow.
func startServe(t *testing.T, mock *mockplug.Plug, dir string, flags ...string) *serving {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &serving{
		t:      t,
		clock:  &testClock{now: testNow},
		dir:    dir,
		stdout: &syncBuffer{},
		stderr: &syncBuffer{},
		cancel: cancel,
		done:   make(chan int, 1),
	}
	args := append([]string{"-host", mock.Addr(), "-data", dir}, flags...)
	args = append(args, "serve")
	go func() {
		s.done <- run(ctx, args, environment{
			stdout:        s.stdout,
			stderr:        s.stderr,
			getenv:        func(string) string { return "" },
			now:           s.clock.Now,
			checkInterval: 2 * time.Millisecond,
		})
	}()
	t.Cleanup(func() { s.stop() })
	return s
}

// stop ends the command and returns its exit code. It is safe to call twice.
func (s *serving) stop() int {
	s.t.Helper()
	s.cancel()
	select {
	case code := <-s.done:
		s.done <- code
		return code
	case <-time.After(10 * time.Second):
		s.t.Fatalf("serve did not stop. Log:\n%s", s.stderr.String())
		return -1
	}
}

// waitFor polls until ok reports true, and fails the test if it never does.
func (s *serving) waitFor(what string, ok func() bool) {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			s.t.Fatalf("timed out waiting for %s. Log:\n%s", what, s.stderr.String())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitForLog waits until the log contains text. The plug changes state a
// moment before serve records that a rule was applied, so tests that go on to
// stop serve wait for the record, not just for the plug.
func (s *serving) waitForLog(text string) {
	s.t.Helper()
	s.waitFor("the log to contain "+text, func() bool {
		return strings.Contains(s.stderr.String(), text)
	})
}

// settle gives serve time to act on the current clock, for tests that check
// that nothing happens.
func (s *serving) settle() {
	time.Sleep(100 * time.Millisecond)
}

func writeSchedule(t *testing.T, dir, content string) schedule.Schedule {
	t.Helper()
	path := filepath.Join(dir, scheduleFile)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := schedule.Load(path)
	if err != nil {
		t.Fatalf("test schedule is invalid: %v", err)
	}
	return parsed
}

func readState(t *testing.T, dir string) schedule.State {
	t.Helper()
	state, err := schedule.StateFile{Path: filepath.Join(dir, stateFile)}.Load()
	if err != nil {
		t.Fatalf("read saved state: %v", err)
	}
	return state
}

const everyDay = `["mon", "tue", "wed", "thu", "fri", "sat", "sun"]`

// testNow is 04:05:06, so these rules fire shortly after the tests start.
const soonSchedule = `{"rules": [
  {"id": "soon-on", "days": ` + everyDay + `, "time": "04:06", "action": "on"},
  {"id": "soon-off", "days": ` + everyDay + `, "time": "04:10", "action": "off"}
]}`

func at(hour, minute int) time.Time {
	return time.Date(testNow.Year(), testNow.Month(), testNow.Day(), hour, minute, 0, 0, testNow.Location())
}

func TestServeAppliesRulesAsTheyFallDue(t *testing.T) {
	mock := startMock(t)
	dir := t.TempDir()
	rules := writeSchedule(t, dir, soonSchedule)
	s := startServe(t, mock, dir, "-sync-interval", "0")

	s.settle()
	if mock.RelayOn() {
		t.Fatal("the plug was switched on before the rule's time")
	}

	s.clock.Set(at(4, 6))
	s.waitFor("the plug to be switched on", mock.RelayOn)
	s.waitForLog("rule=soon-on")

	s.clock.Set(at(4, 10))
	s.waitFor("the plug to be switched off", func() bool { return !mock.RelayOn() })
	s.waitForLog("rule=soon-off")

	if code := s.stop(); code != exitOK {
		t.Errorf("exit code = %d, want %d. Log:\n%s", code, exitOK, s.stderr.String())
	}
	state := readState(t, dir)
	if !state.Since.Equal(at(4, 10)) || state.Digest != rules.Digest() {
		t.Errorf("saved state = %+v, want progress at 04:10 and the schedule's digest", state)
	}
	log := s.stderr.String()
	for _, want := range []string{"rule=soon-on", "rule=soon-off", "action=on", "action=off", "stopped"} {
		if !strings.Contains(log, want) {
			t.Errorf("log does not contain %q:\n%s", want, log)
		}
	}
}

func TestServeCatchesUpAfterDowntime(t *testing.T) {
	mock := startMock(t)
	dir := t.TempDir()
	rules := writeSchedule(t, dir, `{"rules": [
	  {"id": "missed", "days": `+everyDay+`, "time": "04:00", "action": "on"}
	]}`)
	// A previous run got as far as 03:00 with this same schedule.
	saved := schedule.State{Since: at(3, 0), Digest: rules.Digest()}
	if err := (schedule.StateFile{Path: filepath.Join(dir, stateFile)}).Save(saved); err != nil {
		t.Fatal(err)
	}

	s := startServe(t, mock, dir, "-sync-interval", "0")
	s.waitFor("the missed rule to be applied", mock.RelayOn)
	s.waitForLog("rule=missed")
	s.stop()

	if state := readState(t, dir); !state.Since.Equal(at(4, 0)) {
		t.Errorf("saved progress = %v, want 04:00", state.Since)
	}
}

func TestServeFirstStartLeavesThePlugAlone(t *testing.T) {
	mock := startMock(t)
	dir := t.TempDir()
	rules := writeSchedule(t, dir, `{"rules": [
	  {"id": "earlier", "days": `+everyDay+`, "time": "04:00", "action": "on"}
	]}`)

	s := startServe(t, mock, dir, "-sync-interval", "0")
	s.waitFor("the state file to appear", func() bool {
		_, err := os.Stat(filepath.Join(dir, stateFile))
		return err == nil
	})
	s.settle()
	s.stop()

	if mock.RelayOn() {
		t.Error("a rule from before the first start was applied")
	}
	if state := readState(t, dir); !state.Since.Equal(testNow) || state.Digest != rules.Digest() {
		t.Errorf("saved state = %+v, want progress at the start time", state)
	}
}

func TestServeSetsThePlugsClock(t *testing.T) {
	mock := startMock(t)
	mock.SetClock(func() time.Time { return testNow.Add(-5 * time.Minute) })
	dir := t.TempDir()

	s := startServe(t, mock, dir, "-sync-interval", "1h")
	s.waitFor("the plug's clock to be set", func() bool { return mock.Time().Equal(testNow) })
	s.waitFor("the correction to be logged", func() bool {
		return strings.Contains(s.stderr.String(), "corrected the plug's clock")
	})
	s.stop()

	if log := s.stderr.String(); !strings.Contains(log, "5m0s behind this machine") {
		t.Errorf("log does not say how far out the clock was:\n%s", log)
	}
}

func TestServeLeavesTheClockAloneWhenToldTo(t *testing.T) {
	mock := startMock(t)
	behind := testNow.Add(-5 * time.Minute)
	mock.SetClock(func() time.Time { return behind })
	dir := t.TempDir()

	s := startServe(t, mock, dir, "-sync-interval", "0")
	s.settle()
	s.stop()

	if !mock.Time().Equal(behind) {
		t.Errorf("the plug's clock was changed to %v with -sync-interval 0", mock.Time())
	}
	if mock.Requests() != 0 {
		t.Errorf("serve sent %d requests with no rules and no clock setting", mock.Requests())
	}
}

func TestServeRunsWithoutAScheduleFile(t *testing.T) {
	mock := startMock(t)
	s := startServe(t, mock, t.TempDir(), "-sync-interval", "0")
	s.waitFor("the warning", func() bool {
		return strings.Contains(s.stderr.String(), "no schedule file")
	})
	if code := s.stop(); code != exitOK {
		t.Errorf("exit code = %d, want %d", code, exitOK)
	}
}

func TestServeRejectsABadSchedule(t *testing.T) {
	mock := startMock(t)
	dir := t.TempDir()
	path := filepath.Join(dir, scheduleFile)
	if err := os.WriteFile(path, []byte(`{"rules": [{"id": "a", "days": ["mon"], "time": "25:00", "action": "on"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := kasad(t, nil, "-host", mock.Addr(), "-data", dir, "serve")
	if got.code != exitFailed {
		t.Errorf("exit code = %d, want %d", got.code, exitFailed)
	}
	if !strings.Contains(got.stderr, path) || !strings.Contains(got.stderr, "HH:MM") {
		t.Errorf("stderr does not name the file and the problem:\n%s", got.stderr)
	}
	if mock.Requests() != 0 {
		t.Errorf("serve contacted the plug %d times before rejecting the schedule", mock.Requests())
	}
}

func TestServeNeedsItsDataDirectory(t *testing.T) {
	mock := startMock(t)
	dir := t.TempDir()

	missing := filepath.Join(dir, "missing")
	got := kasad(t, nil, "-host", mock.Addr(), "-data", missing, "serve")
	if got.code != exitFailed || !strings.Contains(got.stderr, "data directory") {
		t.Errorf("missing directory: exit code = %d, stderr = %q", got.code, got.stderr)
	}

	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got = kasad(t, nil, "-host", mock.Addr(), "-data", file, "serve")
	if got.code != exitFailed || !strings.Contains(got.stderr, "not a directory") {
		t.Errorf("file as directory: exit code = %d, stderr = %q", got.code, got.stderr)
	}
}

func TestServeRejectsANegativeSyncInterval(t *testing.T) {
	mock := startMock(t)
	got := kasad(t, nil, "-host", mock.Addr(), "-sync-interval", "-1s", "serve")
	if got.code != exitUsage || !strings.Contains(got.stderr, "-sync-interval") {
		t.Errorf("exit code = %d, stderr = %q", got.code, got.stderr)
	}
}

func TestStateFileIsReadableJSON(t *testing.T) {
	mock := startMock(t)
	dir := t.TempDir()
	writeSchedule(t, dir, soonSchedule)
	s := startServe(t, mock, dir, "-sync-interval", "0")
	s.waitFor("the state file to appear", func() bool {
		_, err := os.Stat(filepath.Join(dir, stateFile))
		return err == nil
	})
	s.stop()

	data, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("state file is not JSON: %v\n%s", err, data)
	}
	for _, key := range []string{"since", "digest"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("state file has no %q field:\n%s", key, data)
		}
	}
}
