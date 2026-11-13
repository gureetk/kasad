package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/gureetk/kasad/internal/atomicfile"
)

// State is what a Runner remembers between runs.
type State struct {
	// Since is the moment up to which events have been dealt with.
	Since time.Time `json:"since"`

	// Digest is the Digest of the schedule that Since refers to.
	Digest string `json:"digest"`
}

// StateStore loads and saves a Runner's State.
type StateStore interface {
	// Load returns the saved state, or the zero State if none was saved.
	Load() (State, error)
	Save(State) error
}

// StateFile is a StateStore that keeps the state in a JSON file.
type StateFile struct {
	Path string
}

// Load implements StateStore. A file that does not exist is not an error.
func (f StateFile) Load() (State, error) {
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("%s: %w", f.Path, err)
	}
	return state, nil
}

// Save implements StateStore. The file is replaced in one step, so a crash
// part-way through leaves the previous state in place rather than a
// half-written file.
func (f StateFile) Save(state State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(f.Path, append(data, '\n'))
}
