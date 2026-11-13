package api

import (
	"testing"
	"time"
)

var start = time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)

func TestAttemptsWaitGrowsToALimit(t *testing.T) {
	var a attempts
	now := start
	for i := 1; i < freeAttempts; i++ {
		a.failed(now)
		if wait := a.wait(now); wait != 0 {
			t.Fatalf("after %d wrong passwords the wait is %v, want none", i, wait)
		}
	}
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second,
		time.Minute, time.Minute, time.Minute,
	}
	for i, wait := range want {
		if got := a.failed(now); got != freeAttempts+i {
			t.Errorf("failed returned %d, want %d", got, freeAttempts+i)
		}
		if got := a.wait(now); got != wait {
			t.Errorf("after %d wrong passwords the wait is %v, want %v", freeAttempts+i, got, wait)
		}
		now = now.Add(wait)
		if got := a.wait(now); got != 0 {
			t.Errorf("once the wait is over there is still %v to go", got)
		}
	}

	a.succeeded()
	a.failed(now)
	if wait := a.wait(now); wait != 0 {
		t.Errorf("after a right password, one wrong one makes the wait %v, want none", wait)
	}
}

func TestSessionsAreBounded(t *testing.T) {
	var s sessions
	var ids []string
	for i := 0; i < maxSessions+10; i++ {
		// Each one starts a second after the last, so the first to have
		// started are the first due to end.
		id, err := s.start(start.Add(time.Duration(i) * time.Second))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(s.expires) != maxSessions {
		t.Fatalf("%d sessions kept, want %d", len(s.expires), maxSessions)
	}
	now := start.Add(time.Hour)
	for i, id := range ids {
		if got, want := s.valid(id, now), i >= 10; got != want {
			t.Errorf("session %d is valid = %v, want %v", i, got, want)
		}
	}
}

func TestSessionsThatHaveRunOutAreDropped(t *testing.T) {
	var s sessions
	old, err := s.start(start)
	if err != nil {
		t.Fatal(err)
	}
	later := start.Add(sessionLifetime + time.Second)
	fresh, err := s.start(later)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.expires) != 1 || s.valid(old, later) || !s.valid(fresh, later) {
		t.Errorf("%d sessions kept; old valid = %v, fresh valid = %v", len(s.expires), s.valid(old, later), s.valid(fresh, later))
	}
}

func TestSessionsOfTheZeroValue(t *testing.T) {
	var s sessions
	if s.valid("anything", start) || s.valid("", start) {
		t.Error("an empty store says a session is valid")
	}
	s.end("anything") // must not panic
}

func TestSameSecret(t *testing.T) {
	if !sameSecret("correct horse", "correct horse") {
		t.Error("a secret does not match itself")
	}
	for _, other := range []string{"", "correct", "correct horse ", "Correct horse"} {
		if sameSecret("correct horse", other) {
			t.Errorf("%q matches", other)
		}
	}
}
