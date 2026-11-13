package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	"github.com/gureetk/kasad/internal/atomicfile"
)

// Sessions that are only in memory end when the program does, which signs
// everyone out each time it is restarted. So they are also saved in a file.
//
// What the file holds is of no use for signing in. For each session it has a
// digest of the id, keyed with the password, and the time the session runs
// out. The id cannot be worked out from the digest. Nor can a guess at the
// password be checked against the file alone, because the id that went into
// the digest is 32 random bytes that exist nowhere but in the browser's
// cookie. Someone who holds both the file and that cookie could check
// guesses, but with the cookie they are signed in already.
//
// Keying the digests with the password also means that changing the password
// signs everyone out, as a restart used to. The sessions begun under the old
// password stay in the file until they run out, and nothing matches them.

// savedSessions is the form of the file.
type savedSessions struct {
	Sessions []savedSession `json:"sessions"`
}

type savedSession struct {
	// Digest is the session's digest, in hexadecimal.
	Digest string `json:"digest"`

	// Expires is when the session runs out.
	Expires time.Time `json:"expires"`
}

// save writes the sessions to the file, replacing what it held. Without a
// file it does nothing.
//
// The lock is held while the file is written, so that when two changes are
// saved at once the file ends up with both. Requests that check a session
// wait that long, which is a moment, and happens only when someone signs in
// or out.
func (s *sessions) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return nil
	}

	saved := savedSessions{Sessions: make([]savedSession, 0, len(s.expires))}
	for digest, expiry := range s.expires {
		saved.Sessions = append(saved.Sessions, savedSession{
			Digest:  hex.EncodeToString(digest[:]),
			Expires: expiry.UTC(),
		})
	}
	// A map has no order of its own. Give the file one, so that it does
	// not change when the sessions have not.
	sort.Slice(saved.Sessions, func(i, j int) bool {
		a, b := saved.Sessions[i], saved.Sessions[j]
		if !a.Expires.Equal(b.Expires) {
			return a.Expires.Before(b.Expires)
		}
		return a.Digest < b.Digest
	})

	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(s.path, append(data, '\n'))
}

// load takes up the sessions saved in the file, leaving out those that have
// run out, and returns how many that is. A file that does not exist holds
// none. If the file cannot be read or understood, no sessions are taken from
// it.
func (s *sessions) load(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var saved savedSessions
	if err := json.Unmarshal(data, &saved); err != nil {
		return 0, fmt.Errorf("%s: %w", s.path, err)
	}
	loaded := make(map[sessionDigest]time.Time, len(saved.Sessions))
	for i, session := range saved.Sessions {
		var digest sessionDigest
		// The length is checked first: Decode needs room for all it is
		// given.
		if len(session.Digest) != hex.EncodedLen(len(digest)) {
			return 0, fmt.Errorf("%s: session %d: the digest is not %d bytes long", s.path, i+1, len(digest))
		}
		if _, err := hex.Decode(digest[:], []byte(session.Digest)); err != nil {
			return 0, fmt.Errorf("%s: session %d: the digest is not hexadecimal", s.path, i+1)
		}
		loaded[digest] = session.Expires
	}

	if s.expires == nil {
		s.expires = make(map[sessionDigest]time.Time)
	}
	for digest, expiry := range loaded {
		s.expires[digest] = expiry
	}
	s.trim(now, maxSessions)
	return len(s.expires), nil
}
