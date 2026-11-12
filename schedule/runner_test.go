package schedule

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTime is a clock whose sleeps return at once and move it forward. When
// the clock reaches end, it cancels the context the Runner is running under.
type fakeTime struct {
	mu     sync.Mutex
	now    time.Time
	end    time.Time
	cancel context.CancelFunc
	sleeps []time.Duration
}

func (f *fakeTime) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeTime) Sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	f.sleeps = append(f.sleeps, d)
	f.now = f.now.Add(d)
	if !f.now.Before(f.end) {
		f.cancel()
		return context.Canceled
	}
	return nil
}

// memoryState is a StateStore that keeps the state in memory.
type memoryState struct {
	mu    sync.Mutex
	state State
	saves int
}

func (m *memoryState) Load() (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, nil
}

func (m *memoryState) Save(state State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = state
	m.saves++
	return nil
}

// applied is one call to a Runner's Apply function.
type applied struct {
	rule string
	at   time.Time // the event's scheduled time
	when time.Time // the clock when Apply was called
}

// harness runs a Runner over a stretch of fake time and records what it did.
type harness struct {
	t       *testing.T
	clock   *fakeTime
	state   *memoryState
	log     bytes.Buffer
	applied []applied
	// fail decides whether an Apply call fails. It may be nil.
	fail func(event Event, attempt int) error
}

func newHarness(t *testing.T, start time.Time) *harness {
	return &harness{t: t, clock: &fakeTime{now: start}, state: &memoryState{}}
}

// run runs the schedule from the clock's current time until end.
func (h *harness) run(s Schedule, end time.Time) {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.clock.end = end
	h.clock.cancel = cancel

	attempts := 0
	runner := &Runner{
		Schedule: s,
		State:    h.state,
		Now:      h.clock.Now,
		Logger:   slog.New(slog.NewTextHandler(&h.log, nil)),
		sleep:    h.clock.Sleep,
		Apply: func(_ context.Context, event Event) error {
			attempts++
			if h.fail != nil {
				if err := h.fail(event, attempts); err != nil {
					return err
				}
			}
			h.applied = append(h.applied, applied{rule: event.Rule.ID, at: event.At, when: h.clock.Now()})
			return nil
		},
	}
	if err := runner.Run(ctx); err != nil {
		h.t.Fatalf("Run: %v", err)
	}
}

func (h *harness) wantApplied(want ...string) {
	h.t.Helper()
	var got []string
	for _, a := range h.applied {
		got = append(got, a.rule+"@"+a.at.Format("01-02 15:04"))
	}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		h.t.Errorf("applied [%s], want [%s]", strings.Join(got, ", "), strings.Join(want, ", "))
	}
}

func newYork(t *testing.T) *time.Location {
	return mustLocation(t, "America/New_York")
}

func TestRunnerAppliesEventsAsTheyFallDue(t *testing.T) {
	loc := newYork(t)
	h := newHarness(t, time.Date(2026, time.January, 14, 18, 0, 0, 0, loc))
	h.run(eveningSchedule(), time.Date(2026, time.January, 16, 1, 0, 0, 0, loc))

	h.wantApplied("on@01-14 18:30", "off@01-14 22:15", "on@01-15 18:30", "off@01-15 22:15")
	for _, a := range h.applied {
		if !a.when.Equal(a.at) {
			t.Errorf("rule %s scheduled for %v was applied at %v", a.rule, a.at, a.when)
		}
	}
	if want := time.Date(2026, time.January, 15, 22, 15, 0, 0, loc); !h.state.state.Since.Equal(want) {
		t.Errorf("saved progress = %v, want %v", h.state.state.Since, want)
	}
}

func TestRunnerNeverSleepsLongerThanTheCheckInterval(t *testing.T) {
	loc := newYork(t)
	h := newHarness(t, time.Date(2026, time.January, 14, 18, 0, 0, 0, loc))
	h.run(eveningSchedule(), time.Date(2026, time.January, 15, 1, 0, 0, 0, loc))

	if len(h.clock.sleeps) == 0 {
		t.Fatal("the runner never slept")
	}
	for _, d := range h.clock.sleeps {
		if d <= 0 || d > DefaultCheckInterval {
			t.Fatalf("the runner slept for %v, want more than zero and at most %v", d, DefaultCheckInterval)
		}
	}
}

func TestRunnerFirstRunStartsFromNow(t *testing.T) {
	loc := newYork(t)
	// 10:00, with yesterday's "off" the most recent event. Nothing has been
	// saved, so it is not applied.
	start := time.Date(2026, time.January, 14, 10, 0, 0, 0, loc)
	h := newHarness(t, start)
	h.run(eveningSchedule(), start.Add(2*time.Hour))

	h.wantApplied()
	if !h.state.state.Since.Equal(start) || h.state.state.Digest != eveningSchedule().Digest() {
		t.Errorf("saved state = %+v, want progress at the start time and the schedule's digest", h.state.state)
	}
	if !strings.Contains(h.log.String(), "starting from now") {
		t.Errorf("log does not say the runner started from now:\n%s", h.log.String())
	}
}

func TestRunnerCatchesUpOnTheLatestMissedEvent(t *testing.T) {
	loc := newYork(t)
	s := eveningSchedule()
	// The runner last made progress at 18:00 on the 12th and comes back at
	// 10:00 on the 14th. It missed on, off, on, off. Only the last counts.
	start := time.Date(2026, time.January, 14, 10, 0, 0, 0, loc)
	h := newHarness(t, start)
	h.state.state = State{Since: time.Date(2026, time.January, 12, 18, 0, 0, 0, loc), Digest: s.Digest()}
	h.run(s, time.Date(2026, time.January, 14, 20, 0, 0, 0, loc))

	h.wantApplied("off@01-13 22:15", "on@01-14 18:30")
	if first := h.applied[0]; !first.when.Equal(start) {
		t.Errorf("the missed event was applied at %v, want straight away at %v", first.when, start)
	}
}

func TestRunnerDoesNotCatchUpAfterTheScheduleChanged(t *testing.T) {
	loc := newYork(t)
	start := time.Date(2026, time.January, 14, 10, 0, 0, 0, loc)
	h := newHarness(t, start)
	h.state.state = State{Since: time.Date(2026, time.January, 12, 18, 0, 0, 0, loc), Digest: "digest of some other schedule"}
	h.run(eveningSchedule(), time.Date(2026, time.January, 14, 20, 0, 0, 0, loc))

	// Yesterday's "off" is not applied; today's "on" is.
	h.wantApplied("on@01-14 18:30")
}

func TestRunnerDoesNotRepeatAnEventAfterARestart(t *testing.T) {
	loc := newYork(t)
	s := eveningSchedule()
	h := newHarness(t, time.Date(2026, time.January, 14, 18, 0, 0, 0, loc))
	h.run(s, time.Date(2026, time.January, 14, 20, 0, 0, 0, loc))
	h.wantApplied("on@01-14 18:30")

	// Stop and start again a little later, with the same saved state.
	h.applied = nil
	h.clock.now = time.Date(2026, time.January, 14, 20, 30, 0, 0, loc)
	h.run(s, time.Date(2026, time.January, 14, 22, 0, 0, 0, loc))
	h.wantApplied()
}

func TestRunnerRetriesUntilTheEventIsApplied(t *testing.T) {
	loc := newYork(t)
	h := newHarness(t, time.Date(2026, time.January, 14, 18, 25, 0, 0, loc))
	h.fail = func(_ Event, attempt int) error {
		if attempt <= 3 {
			return errors.New("plug unreachable")
		}
		return nil
	}
	h.run(eveningSchedule(), time.Date(2026, time.January, 14, 20, 0, 0, 0, loc))

	h.wantApplied("on@01-14 18:30")
	onAt := time.Date(2026, time.January, 14, 18, 30, 0, 0, loc)
	if want := onAt.Add(3 * DefaultRetryInterval); !h.applied[0].when.Equal(want) {
		t.Errorf("the event was applied at %v, want after three retries at %v", h.applied[0].when, want)
	}
	if got := strings.Count(h.log.String(), "could not apply rule"); got != 1 {
		t.Errorf("a run of %d failures was logged %d times, want once:\n%s", 3, got, h.log.String())
	}
	if !strings.Contains(h.log.String(), "late=1m30s") {
		t.Errorf("log does not record how late the event was:\n%s", h.log.String())
	}
}

func TestRunnerKeepsProgressUnsavedWhileAnEventIsFailing(t *testing.T) {
	loc := newYork(t)
	start := time.Date(2026, time.January, 14, 18, 25, 0, 0, loc)
	h := newHarness(t, start)
	h.fail = func(Event, int) error { return errors.New("plug unreachable") }
	h.run(eveningSchedule(), time.Date(2026, time.January, 14, 20, 0, 0, 0, loc))

	h.wantApplied()
	if !h.state.state.Since.Equal(start) {
		t.Errorf("saved progress moved to %v although nothing was applied", h.state.state.Since)
	}
}

func TestRunnerLetsANewerEventReplaceAFailingOne(t *testing.T) {
	loc := newYork(t)
	h := newHarness(t, time.Date(2026, time.January, 14, 18, 25, 0, 0, loc))
	// The plug cannot be reached until 23:30, well after both events.
	recovers := time.Date(2026, time.January, 14, 23, 30, 0, 0, loc)
	h.fail = func(Event, int) error {
		if h.clock.Now().Before(recovers) {
			return errors.New("plug unreachable")
		}
		return nil
	}
	h.run(eveningSchedule(), time.Date(2026, time.January, 15, 1, 0, 0, 0, loc))

	// "on" at 18:30 is never applied late: by the time the plug is back,
	// "off" at 22:15 is the event that counts.
	h.wantApplied("off@01-14 22:15")
	if got := strings.Count(h.log.String(), "could not apply rule"); got != 1 {
		t.Errorf("one long run of failures was logged %d times, want once", got)
	}
}

func TestRunnerRecoversWhenTheClockGoesBackwards(t *testing.T) {
	loc := newYork(t)
	s := eveningSchedule()
	// The saved progress is a year ahead of the clock, as it would be if
	// the machine's clock had been wrong when it was saved.
	h := newHarness(t, time.Date(2026, time.January, 14, 18, 0, 0, 0, loc))
	h.state.state = State{Since: time.Date(2027, time.January, 14, 18, 0, 0, 0, loc), Digest: s.Digest()}
	h.run(s, time.Date(2026, time.January, 14, 20, 0, 0, 0, loc))

	h.wantApplied("on@01-14 18:30")
	if !strings.Contains(h.log.String(), "clock is behind") {
		t.Errorf("log does not mention the clock:\n%s", h.log.String())
	}
}

func TestRunnerWithNoRulesJustWaits(t *testing.T) {
	loc := newYork(t)
	h := newHarness(t, time.Date(2026, time.January, 14, 18, 0, 0, 0, loc))
	h.run(Schedule{}, time.Date(2026, time.January, 14, 20, 0, 0, 0, loc))
	h.wantApplied()
}

func TestRunnerWithoutAStateStore(t *testing.T) {
	loc := newYork(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &fakeTime{
		now:    time.Date(2026, time.January, 14, 18, 25, 0, 0, loc),
		end:    time.Date(2026, time.January, 14, 20, 0, 0, 0, loc),
		cancel: cancel,
	}
	var got []string
	runner := &Runner{
		Schedule: eveningSchedule(),
		Now:      clock.Now,
		sleep:    clock.Sleep,
		Apply: func(_ context.Context, event Event) error {
			got = append(got, event.Rule.ID)
			return nil
		},
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 || got[0] != "on" {
		t.Errorf("applied %v, want [on]", got)
	}
}

func TestRunnerStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	runner := &Runner{
		Schedule: eveningSchedule(),
		Apply:    func(context.Context, Event) error { return nil },
	}
	go func() { done <- runner.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v after its context ended, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
}

func TestRunnerNeedsAnApplyFunction(t *testing.T) {
	runner := &Runner{Schedule: eveningSchedule()}
	if err := runner.Run(context.Background()); !errors.Is(err, ErrNoApply) {
		t.Errorf("Run without Apply = %v, want ErrNoApply", err)
	}
}

// brokenState is a StateStore whose Load always fails.
type brokenState struct{}

func (brokenState) Load() (State, error) { return State{}, errors.New("disk on fire") }
func (brokenState) Save(State) error     { return nil }

func TestRunnerReportsUnreadableState(t *testing.T) {
	runner := &Runner{
		Schedule: eveningSchedule(),
		State:    brokenState{},
		Apply:    func(context.Context, Event) error { return nil },
	}
	if err := runner.Run(context.Background()); err == nil {
		t.Error("Run succeeded although the saved state could not be read")
	}
}

func TestStateFile(t *testing.T) {
	dir := t.TempDir()
	file := StateFile{Path: filepath.Join(dir, "state.json")}

	state, err := file.Load()
	if err != nil {
		t.Fatalf("Load of a missing file: %v", err)
	}
	if !state.Since.IsZero() || state.Digest != "" {
		t.Errorf("Load of a missing file = %+v, want the zero State", state)
	}

	want := State{Since: time.Date(2026, time.January, 14, 22, 15, 0, 0, time.UTC), Digest: "abc"}
	for i := 0; i < 3; i++ {
		if err := file.Save(want); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	got, err := file.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.Since.Equal(want.Since) || got.Digest != want.Digest {
		t.Errorf("Load = %+v, want %+v", got, want)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("directory holds %v, want only state.json", names)
	}

	if err := os.WriteFile(file.Path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Load(); err == nil || !strings.Contains(err.Error(), file.Path) {
		t.Errorf("Load of a corrupt file = %v, want an error naming the file", err)
	}

	missingDir := StateFile{Path: filepath.Join(dir, "no-such-dir", "state.json")}
	if err := missingDir.Save(want); err == nil {
		t.Error("Save into a missing directory succeeded")
	}
}
