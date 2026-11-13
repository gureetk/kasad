package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Signing in is how a person shows the web app's password once, and is then
// recognised by a cookie. Programs have no use for it: they send the token
// with each request instead.

const (
	// sessionCookie names the cookie that carries a session.
	sessionCookie = "kasad_session"

	// sessionLifetime is how long signing in lasts.
	sessionLifetime = 30 * 24 * time.Hour

	// maxSessions is how many sessions are kept at once. It is far more
	// than a household needs, and keeps their number bounded.
	maxSessions = 64

	// freeAttempts is how many wrong passwords in a row are answered at
	// once. After that, each further one makes the next attempt wait:
	// first a second, then twice as long each time, up to maxAttemptWait.
	freeAttempts   = 5
	maxAttemptWait = time.Minute
)

// sameSecret reports whether two secrets are the same, taking the same time
// whether or not they are, and whatever their lengths.
func sameSecret(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

// sessions holds the sessions that are current. Sessions are kept in memory
// only, so they end when the program does. The zero value is ready to use.
type sessions struct {
	mu sync.Mutex
	// expires maps the hash of each session's id to when it runs out. The
	// ids themselves, which are as good as the password while they last,
	// are not kept.
	expires map[[sha256.Size]byte]time.Time
}

// start begins a session and returns its id, which is the cookie's value.
func (s *sessions) start(now time.Time) (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(random)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expires == nil {
		s.expires = make(map[[sha256.Size]byte]time.Time)
	}
	for key, expiry := range s.expires {
		if !now.Before(expiry) {
			delete(s.expires, key)
		}
	}
	for len(s.expires) >= maxSessions {
		// Make room by ending the session nearest its end.
		var oldest [sha256.Size]byte
		var soonest time.Time
		for key, expiry := range s.expires {
			if soonest.IsZero() || expiry.Before(soonest) {
				oldest, soonest = key, expiry
			}
		}
		delete(s.expires, oldest)
	}
	s.expires[sha256.Sum256([]byte(id))] = now.Add(sessionLifetime)
	return id, nil
}

// valid reports whether id is that of a session that has not run out.
func (s *sessions) valid(id string, now time.Time) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.expires[sha256.Sum256([]byte(id))]
	return ok && now.Before(expiry)
}

// end finishes the session with the given id, if there is one.
func (s *sessions) end(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.expires, sha256.Sum256([]byte(id)))
}

// attempts slows down guessing at the password. It counts wrong passwords
// from everyone together, not for each address they come from, because an
// address is cheap to change. That means someone on the network can keep
// others from signing in by guessing wrongly over and over. They cannot use
// it to get in, and sessions that are already open are not affected.
//
// The zero value is ready to use.
type attempts struct {
	mu       sync.Mutex
	failures int       // wrong passwords in a row
	blocked  time.Time // no attempt is looked at before this
}

// wait returns how long it is until an attempt will be looked at, or zero if
// one may be made now.
func (a *attempts) wait(now time.Time) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if now.Before(a.blocked) {
		return a.blocked.Sub(now)
	}
	return 0
}

// failed records a wrong password, and returns how many there have now been
// in a row.
func (a *attempts) failed(now time.Time) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failures++
	if extra := a.failures - freeAttempts; extra >= 0 {
		wait := maxAttemptWait
		if extra < 6 { // 1 s doubled six times is already past the limit
			wait = time.Second << extra
		}
		a.blocked = now.Add(wait)
	}
	return a.failures
}

// succeeded records a right password, which clears the count.
func (a *attempts) succeeded() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failures = 0
	a.blocked = time.Time{}
}

// open reports whether the API is open to anyone who can reach it.
func (s *Server) open() bool {
	return s.Token == "" && s.Password == ""
}

// authorized reports whether a request shows that it may use the API: by
// carrying the token, or by carrying the cookie of a current session.
func (s *Server) authorized(r *http.Request) bool {
	if s.Token != "" {
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if ok && strings.EqualFold(scheme, "Bearer") && sameSecret(strings.TrimSpace(token), s.Token) {
			return true
		}
	}
	if s.Password != "" {
		if cookie, err := r.Cookie(sessionCookie); err == nil && s.sessions.valid(cookie.Value, s.now()) {
			return true
		}
	}
	return false
}

// crossSite reports whether a browser sent this request on behalf of a page
// that kasad did not serve. Such a request would carry the session cookie
// of whoever is using that browser, so one that changes anything is refused.
//
// Requests from programs carry neither header and pass: they have no cookie
// to misuse, and show the token instead.
func crossSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "":
		// An older browser, or not a browser. Go by the Origin header.
	case "same-origin", "none":
		return false
	default:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	from, err := url.Parse(origin)
	return err != nil || from.Host != r.Host
}

// sessionResponse is the body of the answers to requests on /api/session.
// It tells a page what it needs to know to ask the person to sign in.
type sessionResponse struct {
	// Required reports whether the API asks who is calling at all.
	Required bool `json:"required"`

	// Password reports whether there is a password to sign in with.
	Password bool `json:"password"`

	// SignedIn reports whether the request showed that it may use the
	// API. It is true of every request when nothing is required.
	SignedIn bool `json:"signed_in"`
}

func (s *Server) sessionState(signedIn bool) sessionResponse {
	return sessionResponse{
		Required: !s.open(),
		Password: s.Password != "",
		SignedIn: signedIn,
	}
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.sessionState(s.open() || s.authorized(r)))
}

func (s *Server) postSession(w http.ResponseWriter, r *http.Request) {
	if s.Password == "" {
		writeError(w, http.StatusConflict, "there is no password to sign in with")
		return
	}
	var request struct {
		Password *string `json:"password"`
	}
	if err := decodeBody(r, &request); err != nil {
		writeBodyError(w, err)
		return
	}
	if request.Password == nil {
		writeError(w, http.StatusBadRequest, `the request needs a "password"`)
		return
	}

	now := s.now()
	if wait := s.attempts.wait(now); wait > 0 {
		seconds := int((wait + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, http.StatusTooManyRequests,
			fmt.Sprintf("too many wrong passwords; try again in %d s", seconds))
		return
	}
	if !sameSecret(*request.Password, s.Password) {
		failures := s.attempts.failed(now)
		s.logger().Warn("wrong password given to the web app", "from", r.RemoteAddr, "in_a_row", failures)
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	s.attempts.succeeded()

	id, err := s.sessions.start(now)
	if err != nil {
		s.logger().Error("could not start a session", "error", err)
		writeError(w, http.StatusInternalServerError, "could not sign in")
		return
	}
	http.SetCookie(w, s.cookie(r, id, int(sessionLifetime/time.Second)))
	s.logger().Info("signed in to the web app", "from", r.RemoteAddr)
	writeJSON(w, http.StatusOK, s.sessionState(true))
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.end(cookie.Value)
	}
	http.SetCookie(w, s.cookie(r, "", -1))
	writeJSON(w, http.StatusOK, s.sessionState(s.open()))
}

// cookie returns the session cookie to set. A negative maxAge removes it.
func (s *Server) cookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:   sessionCookie,
		Value:  value,
		Path:   "/",
		MaxAge: maxAge,
		// Scripts in the page cannot read it, and a browser does not send
		// it along with requests that another site started.
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// Where the browser reached kasad over HTTPS, by way of something
		// in front of it, the cookie is kept to HTTPS.
		Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
	}
}
