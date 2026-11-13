package api

import (
	"os"
	"path/filepath"
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

func TestSessionDigestDependsOnTheSecret(t *testing.T) {
	one := sessions{secret: []byte("correct horse")}
	same := sessions{secret: []byte("correct horse")}
	other := sessions{secret: []byte("correct horse ")}
	if one.digest("an id") != same.digest("an id") {
		t.Error("the same id under the same secret has two digests")
	}
	if one.digest("an id") == other.digest("an id") {
		t.Error("an id has the same digest under two secrets")
	}
	if one.digest("an id") == one.digest("an ie") {
		t.Error("two ids have the same digest")
	}
}

func TestSessionsSavedAndLoaded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	saved := sessions{secret: []byte("correct horse"), path: path}
	var ids []string
	for i := 0; i < 3; i++ {
		id, err := saved.start(start.Add(time.Duration(i) * time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := saved.save(); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Loaded half an hour after the first of them ran out, the other two
	// are taken up, each still due to end when it was.
	later := start.Add(sessionLifetime + 30*time.Minute)
	loaded := sessions{secret: []byte("correct horse"), path: path}
	kept, err := loaded.load(later)
	if err != nil || kept != 2 {
		t.Fatalf("load = %d, %v; want 2 sessions and no error", kept, err)
	}
	for i, id := range ids {
		if got, want := loaded.valid(id, later), i > 0; got != want {
			t.Errorf("session %d is valid after loading = %v, want %v", i, got, want)
		}
	}
	if end := later.Add(30 * time.Minute); loaded.valid(ids[1], end) || !loaded.valid(ids[2], end) {
		t.Error("the sessions do not run out when they were due to")
	}

	// Saving the same sessions again writes the same file, whatever order
	// the map gives them in.
	for i := 0; i < 20; i++ {
		again := sessions{secret: []byte("correct horse"), path: path}
		if _, err := again.load(start); err != nil {
			t.Fatal(err)
		}
		if err := again.save(); err != nil {
			t.Fatal(err)
		}
		now, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(now) != string(first) {
			t.Fatalf("saving the same sessions changed the file:\n%s\nwas:\n%s", now, first)
		}
	}
}

func TestLoadKeepsNoMoreThanTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	saved := sessions{path: path, expires: make(map[sessionDigest]time.Time)}
	// Written by hand, as nothing kasad does makes a file this long. The
	// later a session was added here, the later it runs out.
	const extra = 7
	for i := 0; i < maxSessions+extra; i++ {
		saved.expires[sessionDigest{byte(i), byte(i >> 8)}] = start.Add(time.Duration(i+1) * time.Hour)
	}
	if err := saved.save(); err != nil {
		t.Fatal(err)
	}

	loaded := sessions{path: path}
	kept, err := loaded.load(start)
	if err != nil || kept != maxSessions || len(loaded.expires) != maxSessions {
		t.Fatalf("load = %d, %v, and %d sessions held; want %d", kept, err, len(loaded.expires), maxSessions)
	}
	for i := 0; i < maxSessions+extra; i++ {
		if _, ok := loaded.expires[sessionDigest{byte(i), byte(i >> 8)}]; ok != (i >= extra) {
			t.Errorf("session %d was kept = %v, want %v: the ones nearest their end go first", i, ok, i >= extra)
		}
	}
}

func TestSessionsWithoutAFile(t *testing.T) {
	var s sessions
	if _, err := s.start(start); err != nil {
		t.Fatal(err)
	}
	// With nowhere to save to, saving is not attempted, so it cannot fail.
	if err := s.save(); err != nil {
		t.Errorf("save without a file = %v, want no error", err)
	}

	// A file that is not there holds no sessions, and that is no error.
	missing := sessions{path: filepath.Join(t.TempDir(), "sessions.json")}
	if kept, err := missing.load(start); kept != 0 || err != nil {
		t.Errorf("load of a file that does not exist = %d, %v; want 0 and no error", kept, err)
	}
	// One that is there and cannot be read is.
	unreadable := sessions{path: t.TempDir()}
	if kept, err := unreadable.load(start); kept != 0 || err == nil {
		t.Errorf("load of a directory = %d, %v; want 0 and an error", kept, err)
	}
}
