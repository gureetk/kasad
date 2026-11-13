package schedule

import (
	"encoding/json"
	"errors"
	"io/fs"
	"sync"

	"github.com/gureetk/kasad/internal/atomicfile"
)

// Store holds a schedule that can be replaced while it is in use, and keeps
// it in a file. It is safe for concurrent use.
type Store struct {
	path    string
	changes chan struct{}

	mu      sync.RWMutex
	current Schedule
}

// OpenStore returns a Store for the schedule file at path. If the file does
// not exist, the Store starts with an empty schedule and creates the file
// the first time the schedule is replaced.
func OpenStore(path string) (*Store, error) {
	store := &Store{path: path, changes: make(chan struct{}, 1)}
	loaded, err := Load(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Start empty.
	case err != nil:
		return nil, err
	default:
		store.current = loaded
	}
	return store, nil
}

// Schedule returns the current schedule. The result is a copy: changing it
// does not change the Store.
func (s *Store) Schedule() Schedule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current.clone()
}

// Replace validates next, writes it to the file, and makes it the current
// schedule. If any of that fails, the current schedule and the file are left
// as they were.
func (s *Store) Replace(next Schedule) error {
	if err := next.Validate(); err != nil {
		return err
	}
	next = next.clone()
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := atomicfile.Write(s.path, append(data, '\n')); err != nil {
		return err
	}
	s.current = next

	// Tell whoever is listening, without waiting for them. One pending
	// notice is enough however many changes it stands for.
	select {
	case s.changes <- struct{}{}:
	default:
	}
	return nil
}

// Changes returns a channel that receives a value after the schedule has
// been replaced. It suits a Runner's Wake field.
func (s *Store) Changes() <-chan struct{} {
	return s.changes
}

// clone returns a copy of s that shares no memory with it. The copy's rule
// list is never nil, so it encodes as a list even when empty.
func (s Schedule) clone() Schedule {
	rules := make([]Rule, len(s.Rules))
	copy(rules, s.Rules)
	return Schedule{Rules: rules}
}
