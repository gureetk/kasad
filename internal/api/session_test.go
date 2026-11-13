package api_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const password = "correct horse battery staple"

// signIn signs in with the fixture's password and returns the cookie header
// to send with later requests.
func (f *fixture) signIn(t *testing.T) map[string]string {
	t.Helper()
	r := f.do("POST", "/api/session", `{"password": "`+password+`"}`, nil)
	wantStatus(t, r, http.StatusOK)
	cookie := sessionCookie(t, r)
	return map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}
}

// sessionCookie returns the session cookie a response sets.
func sessionCookie(t *testing.T, r reply) *http.Cookie {
	t.Helper()
	response := http.Response{Header: r.header}
	for _, cookie := range response.Cookies() {
		if cookie.Name == "kasad_session" {
			return cookie
		}
	}
	t.Fatalf("the response sets no session cookie. Headers: %v", r.header)
	return nil
}

func wantSession(t *testing.T, r reply, required, hasPassword, signedIn bool) {
	t.Helper()
	wantStatus(t, r, http.StatusOK)
	fields := r.fields(t)
	if fields["required"] != required || fields["password"] != hasPassword || fields["signed_in"] != signedIn {
		t.Errorf("session = %s, want required %v, password %v, signed_in %v", r.body, required, hasPassword, signedIn)
	}
}

func TestSessionWhenNothingIsRequired(t *testing.T) {
	f := newFixture(t)

	wantSession(t, f.do("GET", "/api/session", "", nil), false, false, true)
	// There is nothing to sign in with, and no need to.
	wantStatus(t, f.do("POST", "/api/session", `{"password": "anything"}`, nil), http.StatusConflict)
	wantSession(t, f.do("DELETE", "/api/session", "", nil), false, false, true)
}

func TestSignInAndOut(t *testing.T) {
	f := newFixture(t)
	f.server.Password = password

	// Before signing in, the API says no, without asking the browser to
	// put up a password prompt of its own.
	r := f.do("GET", "/api/status", "", nil)
	wantStatus(t, r, http.StatusUnauthorized)
	if challenge := r.header.Get("WWW-Authenticate"); challenge != "" {
		t.Errorf("WWW-Authenticate = %q, want none", challenge)
	}
	wantSession(t, f.do("GET", "/api/session", "", nil), true, true, false)

	r = f.do("POST", "/api/session", `{"password": "`+password+`"}`, nil)
	wantSession(t, r, true, true, true)
	cookie := sessionCookie(t, r)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Errorf("cookie = %+v, want HttpOnly, SameSite=Strict and Path=/", cookie)
	}
	if cookie.MaxAge != 30*24*60*60 {
		t.Errorf("cookie lasts %d s, want 30 days", cookie.MaxAge)
	}
	if cookie.Secure {
		t.Error("the cookie is marked Secure on a plain HTTP connection, where the browser would never send it back")
	}
	if len(cookie.Value) < 40 {
		t.Errorf("session id %q is too short to be unguessable", cookie.Value)
	}
	signedIn := map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}

	wantStatus(t, f.do("GET", "/api/status", "", signedIn), http.StatusOK)
	wantStatus(t, f.do("POST", "/api/relay", `{"on": true}`, signedIn), http.StatusOK)
	wantSession(t, f.do("GET", "/api/session", "", signedIn), true, true, true)

	// Signing out ends the session at the server, not just in the browser.
	r = f.do("DELETE", "/api/session", "", signedIn)
	wantSession(t, r, true, true, false)
	if cleared := sessionCookie(t, r); cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Errorf("signing out set the cookie to %+v, want it removed", cleared)
	}
	wantStatus(t, f.do("GET", "/api/status", "", signedIn), http.StatusUnauthorized)
	wantSession(t, f.do("GET", "/api/session", "", signedIn), true, true, false)
}

func TestEachSignInGetsItsOwnSession(t *testing.T) {
	f := newFixture(t)
	f.server.Password = password

	first, second := f.signIn(t), f.signIn(t)
	if first["Cookie"] == second["Cookie"] {
		t.Fatal("two sign-ins got the same session")
	}
	// Signing out of one leaves the other.
	wantStatus(t, f.do("DELETE", "/api/session", "", first), http.StatusOK)
	wantStatus(t, f.do("GET", "/api/status", "", first), http.StatusUnauthorized)
	wantStatus(t, f.do("GET", "/api/status", "", second), http.StatusOK)
}

func TestWrongPassword(t *testing.T) {
	f := newFixture(t)
	f.server.Password = password

	for name, body := range map[string]string{
		"wrong":             `{"password": "incorrect horse"}`,
		"a prefix":          `{"password": "correct horse"}`,
		"with a tail":       `{"password": "` + password + `!"}`,
		"empty":             `{"password": ""}`,
		"other letter case": `{"password": "` + strings.ToUpper(password) + `"}`,
	} {
		f.server.Now = func() time.Time { return testNow }
		r := f.do("POST", "/api/session", body, nil)
		if r.status != http.StatusUnauthorized || len(r.header.Values("Set-Cookie")) != 0 {
			t.Errorf("%s: status %d with cookies %v, want 401 and none", name, r.status, r.header.Values("Set-Cookie"))
		}
		// Let the next case start with a clean count.
		wantStatus(t, f.do("POST", "/api/session", `{"password": "`+password+`"}`, nil), http.StatusOK)
	}

	for name, body := range map[string]string{
		"no password field": `{}`,
		"password is null":  `{"password": null}`,
		"not JSON":          `open sesame`,
		"an extra field":    `{"password": "` + password + `", "remember": true}`,
	} {
		r := f.do("POST", "/api/session", body, map[string]string{"Content-Type": "application/json"})
		if r.status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.status)
		}
	}
	r := f.do("POST", "/api/session", `{"password": "`+password+`"}`, map[string]string{"Content-Type": "text/plain"})
	wantStatus(t, r, http.StatusUnsupportedMediaType)
}

func TestWrongPasswordsAreSlowedDown(t *testing.T) {
	f := newFixture(t)
	f.server.Password = password
	now := testNow
	f.server.Now = func() time.Time { return now }
	wrong, right := `{"password": "guess"}`, `{"password": "`+password+`"}`

	// The first few wrong passwords are answered at once.
	for i := 1; i <= 5; i++ {
		wantStatus(t, f.do("POST", "/api/session", wrong, nil), http.StatusUnauthorized)
	}
	// After that, nobody's attempt is looked at for a while, right or wrong.
	r := f.do("POST", "/api/session", right, nil)
	wantStatus(t, r, http.StatusTooManyRequests)
	if r.header.Get("Retry-After") != "1" || !strings.Contains(r.body, "try again in 1 s") {
		t.Errorf("Retry-After = %q, body %s; want a wait of 1 s", r.header.Get("Retry-After"), r.body)
	}
	if len(r.header.Values("Set-Cookie")) != 0 {
		t.Error("a refused attempt set a cookie")
	}

	// Each further wrong password doubles the wait.
	for _, wait := range []string{"2", "4", "8"} {
		now = now.Add(time.Minute)
		wantStatus(t, f.do("POST", "/api/session", wrong, nil), http.StatusUnauthorized)
		r = f.do("POST", "/api/session", wrong, nil)
		wantStatus(t, r, http.StatusTooManyRequests)
		if r.header.Get("Retry-After") != wait {
			t.Errorf("Retry-After = %q, want %s", r.header.Get("Retry-After"), wait)
		}
	}

	// Part of the way through the wait, the answer says what is left.
	now = now.Add(5500 * time.Millisecond)
	r = f.do("POST", "/api/session", right, nil)
	wantStatus(t, r, http.StatusTooManyRequests)
	if r.header.Get("Retry-After") != "3" {
		t.Errorf("Retry-After = %q with 2.5 s left, want 3", r.header.Get("Retry-After"))
	}

	// Once the wait is over, the right password works and clears the count.
	now = now.Add(3 * time.Second)
	wantStatus(t, f.do("POST", "/api/session", right, nil), http.StatusOK)
	for i := 1; i <= 5; i++ {
		wantStatus(t, f.do("POST", "/api/session", wrong, nil), http.StatusUnauthorized)
	}
}

func TestSlowingDownDoesNotTouchOpenSessions(t *testing.T) {
	f := newFixture(t)
	f.server.Password = password
	signedIn := f.signIn(t)

	for i := 1; i <= 8; i++ {
		f.do("POST", "/api/session", `{"password": "guess"}`, nil)
	}
	wantStatus(t, f.do("POST", "/api/session", `{"password": "guess"}`, nil), http.StatusTooManyRequests)
	wantStatus(t, f.do("GET", "/api/status", "", signedIn), http.StatusOK)
	wantStatus(t, f.do("POST", "/api/relay", `{"on": true}`, signedIn), http.StatusOK)
}

func TestSessionRunsOut(t *testing.T) {
	f := newFixture(t)
	f.server.Password = password
	now := testNow
	f.server.Now = func() time.Time { return now }
	signedIn := f.signIn(t)

	now = testNow.Add(30*24*time.Hour - time.Second)
	wantStatus(t, f.do("GET", "/api/status", "", signedIn), http.StatusOK)
	now = testNow.Add(30 * 24 * time.Hour)
	wantStatus(t, f.do("GET", "/api/status", "", signedIn), http.StatusUnauthorized)
}

func TestCookiesThatAreNotSessions(t *testing.T) {
	f := newFixture(t)
	f.server.Password = password
	f.signIn(t)

	for name, cookie := range map[string]string{
		"a made-up id":   "kasad_session=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"an empty value": "kasad_session=",
		"another name":   "session=" + password,
		"the password":   "kasad_session=" + strings.ReplaceAll(password, " ", "%20"),
	} {
		r := f.do("GET", "/api/status", "", map[string]string{"Cookie": cookie})
		if r.status != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, r.status)
		}
	}
}

func TestCookieIsKeptToHTTPSBehindAProxyThatUsesIt(t *testing.T) {
	f := newFixture(t)
	f.server.Password = password

	r := f.do("POST", "/api/session", `{"password": "`+password+`"}`, map[string]string{"X-Forwarded-Proto": "https"})
	wantStatus(t, r, http.StatusOK)
	if !sessionCookie(t, r).Secure {
		t.Error("the cookie is not marked Secure though the browser's connection was HTTPS")
	}
}

func TestTokenAndPasswordTogether(t *testing.T) {
	f := newFixture(t)
	f.server.Token = "t0ken"
	f.server.Password = password

	// With neither, the answer still names the token as a way in.
	r := f.do("GET", "/api/status", "", nil)
	wantStatus(t, r, http.StatusUnauthorized)
	if r.header.Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want Bearer", r.header.Get("WWW-Authenticate"))
	}
	wantSession(t, f.do("GET", "/api/session", "", nil), true, true, false)

	token := map[string]string{"Authorization": "Bearer t0ken"}
	wantStatus(t, f.do("GET", "/api/status", "", token), http.StatusOK)
	wantSession(t, f.do("GET", "/api/session", "", token), true, true, true)
	wantStatus(t, f.do("GET", "/api/status", "", f.signIn(t)), http.StatusOK)

	// Each secret only works in its own place.
	wantStatus(t, f.do("GET", "/api/status", "", map[string]string{"Authorization": "Bearer " + password}), http.StatusUnauthorized)
	wantStatus(t, f.do("POST", "/api/session", `{"password": "t0ken"}`, nil), http.StatusUnauthorized)
	wantStatus(t, f.do("GET", "/api/status", "", map[string]string{"Cookie": "kasad_session=t0ken"}), http.StatusUnauthorized)
}

func TestTokenAloneLeavesNothingToSignInWith(t *testing.T) {
	f := newFixture(t)
	f.server.Token = "t0ken"

	wantSession(t, f.do("GET", "/api/session", "", nil), true, false, false)
	wantStatus(t, f.do("POST", "/api/session", `{"password": "t0ken"}`, nil), http.StatusConflict)
}

func TestRequestsFromOtherSitesAreRefused(t *testing.T) {
	f := newFixture(t)
	f.server.Password = password
	signedIn := f.signIn(t)
	host := strings.TrimPrefix(f.http.URL, "http://")

	with := func(extra map[string]string) map[string]string {
		headers := map[string]string{"Cookie": signedIn["Cookie"]}
		for name, value := range extra {
			headers[name] = value
		}
		return headers
	}
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"the page itself", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://" + host}, http.StatusOK},
		{"typed or bookmarked", map[string]string{"Sec-Fetch-Site": "none"}, http.StatusOK},
		{"a program", nil, http.StatusOK},
		{"an older browser, same origin", map[string]string{"Origin": "http://" + host}, http.StatusOK},
		{"another site", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://elsewhere.example"}, http.StatusForbidden},
		{"another port on this machine", map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "http://127.0.0.1:1"}, http.StatusForbidden},
		{"an older browser, another site", map[string]string{"Origin": "http://elsewhere.example"}, http.StatusForbidden},
		{"an origin that says nothing", map[string]string{"Origin": "null"}, http.StatusForbidden},
	}
	for _, tc := range cases {
		f.mock.SetRelay(false)
		r := f.do("POST", "/api/relay", `{"on": true}`, with(tc.headers))
		if r.status != tc.want {
			t.Errorf("%s: status %d, want %d. Body: %s", tc.name, r.status, tc.want, r.body)
		}
		if refused := tc.want == http.StatusForbidden; refused == f.mock.RelayOn() {
			t.Errorf("%s: the plug is on = %v", tc.name, f.mock.RelayOn())
		}
	}

	// It applies to everything that changes something, signing in and out
	// included, but not to reading.
	elsewhere := map[string]string{"Sec-Fetch-Site": "cross-site"}
	wantStatus(t, f.do("PUT", "/api/schedule", twoRules, with(elsewhere)), http.StatusForbidden)
	wantStatus(t, f.do("DELETE", "/api/plug/rules/any", "", with(elsewhere)), http.StatusForbidden)
	wantStatus(t, f.do("POST", "/api/session", `{"password": "`+password+`"}`, elsewhere), http.StatusForbidden)
	wantStatus(t, f.do("DELETE", "/api/session", "", with(elsewhere)), http.StatusForbidden)
	wantStatus(t, f.do("GET", "/api/status", "", with(elsewhere)), http.StatusOK)
	wantStatus(t, f.do("GET", "/api/status", "", signedIn), http.StatusOK)
}

func TestSessionPathAllowsOnlyItsMethods(t *testing.T) {
	f := newFixture(t)
	r := f.do("PUT", "/api/session", `{}`, nil)
	wantStatus(t, r, http.StatusMethodNotAllowed)
	if r.header.Get("Allow") != "DELETE, GET, HEAD, POST" {
		t.Errorf("Allow = %q", r.header.Get("Allow"))
	}
}

// logBuffer collects a server's log, for tests that check what it says.
type logBuffer struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

// newRun returns a fixture that stands for one run of kasad with a password
// and a file to keep its sessions in, along with what that run logs. A
// second one given the same file stands for kasad having been restarted.
func newRun(t *testing.T, file, password string) (*fixture, *logBuffer) {
	t.Helper()
	f := newFixture(t)
	log := &logBuffer{}
	f.server.Password = password
	f.server.SessionFile = file
	f.server.Logger = slog.New(slog.NewTextHandler(log, nil))
	return f, log
}

// savedSessionCount returns how many sessions the file holds.
func savedSessionCount(t *testing.T, file string) int {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Sessions []struct {
			Digest  string    `json:"digest"`
			Expires time.Time `json:"expires"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("the session file is not as expected: %v\n%s", err, data)
	}
	if saved.Sessions == nil {
		t.Fatalf("the session file has no list of sessions:\n%s", data)
	}
	return len(saved.Sessions)
}

func TestSessionsOutlastARestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sessions.json")
	before, _ := newRun(t, file, password)
	signedIn := before.signIn(t)
	wantStatus(t, before.do("GET", "/api/status", "", signedIn), http.StatusOK)

	after, log := newRun(t, file, password)
	wantSession(t, after.do("GET", "/api/session", "", signedIn), true, true, true)
	wantStatus(t, after.do("POST", "/api/relay", `{"on": true}`, signedIn), http.StatusOK)
	if !after.mock.RelayOn() {
		t.Error("the plug is off after a request that carried a session from before the restart")
	}
	if !strings.Contains(log.String(), `msg="sessions carried over from the last run" sessions=1`) {
		t.Errorf("the log does not say that a session was carried over:\n%s", log.String())
	}

	// Being restarted makes nothing else into a session.
	wantStatus(t, after.do("GET", "/api/status", "", nil), http.StatusUnauthorized)
	wantStatus(t, after.do("GET", "/api/status", "", map[string]string{"Cookie": "kasad_session=made-up"}),
		http.StatusUnauthorized)
}

func TestSeveralSessionsOutlastARestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sessions.json")
	before, _ := newRun(t, file, password)

	// People signing in at the same moment must not lose each other's
	// sessions from the file.
	const people = 8
	replies := make([]reply, people)
	var signingIn sync.WaitGroup
	for i := range replies {
		signingIn.Add(1)
		go func() {
			defer signingIn.Done()
			response, err := http.Post(before.http.URL+"/api/session", "application/json",
				strings.NewReader(`{"password": "`+password+`"}`))
			if err != nil {
				t.Errorf("signing in: %v", err)
				return
			}
			response.Body.Close()
			replies[i] = reply{status: response.StatusCode, header: response.Header}
		}()
	}
	signingIn.Wait()
	if t.Failed() {
		t.FailNow()
	}
	cookies := make([]map[string]string, people)
	for i, r := range replies {
		wantStatus(t, r, http.StatusOK)
		cookie := sessionCookie(t, r)
		cookies[i] = map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}
	}
	if got := savedSessionCount(t, file); got != people {
		t.Errorf("the file holds %d sessions, want %d", got, people)
	}

	after, _ := newRun(t, file, password)
	for i, signedIn := range cookies {
		if r := after.do("GET", "/api/status", "", signedIn); r.status != http.StatusOK {
			t.Errorf("session %d after the restart = %d, want 200", i, r.status)
		}
	}
}

func TestSigningOutOutlastsARestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sessions.json")
	before, _ := newRun(t, file, password)
	leaving, staying := before.signIn(t), before.signIn(t)
	wantStatus(t, before.do("DELETE", "/api/session", "", leaving), http.StatusOK)
	if got := savedSessionCount(t, file); got != 1 {
		t.Errorf("the file holds %d sessions after one of two signed out, want 1", got)
	}

	after, _ := newRun(t, file, password)
	wantStatus(t, after.do("GET", "/api/status", "", leaving), http.StatusUnauthorized)
	wantStatus(t, after.do("GET", "/api/status", "", staying), http.StatusOK)
}

func TestAChangedPasswordEndsSavedSessions(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sessions.json")
	before, _ := newRun(t, file, password)
	signedIn := before.signIn(t)

	changed, _ := newRun(t, file, "another password altogether")
	wantSession(t, changed.do("GET", "/api/session", "", signedIn), true, true, false)
	wantStatus(t, changed.do("GET", "/api/status", "", signedIn), http.StatusUnauthorized)

	// It is the password that decides, not that there was a restart with
	// another one in between.
	back, _ := newRun(t, file, password)
	wantStatus(t, back.do("GET", "/api/status", "", signedIn), http.StatusOK)
}

func TestSavedSessionsStillRunOut(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sessions.json")
	before, _ := newRun(t, file, password)
	signedIn := before.signIn(t)

	// Started again a moment before the session runs out, kasad takes it
	// up, and then lets it run out.
	now := testNow.Add(30*24*time.Hour - time.Second)
	inTime, _ := newRun(t, file, password)
	inTime.server.Now = func() time.Time { return now }
	wantStatus(t, inTime.do("GET", "/api/status", "", signedIn), http.StatusOK)
	now = now.Add(time.Second)
	wantStatus(t, inTime.do("GET", "/api/status", "", signedIn), http.StatusUnauthorized)

	// Started again after that, it does not take the session up at all.
	late, log := newRun(t, file, password)
	late.server.Now = func() time.Time { return testNow.Add(30 * 24 * time.Hour) }
	wantStatus(t, late.do("GET", "/api/status", "", signedIn), http.StatusUnauthorized)
	if strings.Contains(log.String(), "carried over") {
		t.Errorf("the log says a session was carried over when it had run out:\n%s", log.String())
	}
}

func TestSessionFileHoldsNothingToSignInWith(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sessions.json")
	f, log := newRun(t, file, password)
	r := f.do("POST", "/api/session", `{"password": "`+password+`"}`, nil)
	wantStatus(t, r, http.StatusOK)
	cookie := sessionCookie(t, r)

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), cookie.Value) {
		t.Errorf("the session's id is in the file:\n%s", data)
	}
	if strings.Contains(string(data), password) {
		t.Errorf("the password is in the file:\n%s", data)
	}
	if strings.Contains(log.String(), cookie.Value) || strings.Contains(log.String(), password) {
		t.Errorf("the session's id or the password is in the log:\n%s", log.String())
	}
	if got := savedSessionCount(t, file); got != 1 {
		t.Errorf("the file holds %d sessions, want 1", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("the file's mode is %o, want 600", mode)
		}
	}
}

func TestADamagedSessionFileSignsEveryoneOut(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	damaged := map[string]string{
		"empty":             "",
		"not JSON":          "sessions\n",
		"not a list":        `{"sessions": "none"}`,
		"cut short":         `{"sessions": [{"digest": "` + digest + `", "expires": "2001-03-01T00:00:00Z"}`,
		"short digest":      `{"sessions": [{"digest": "abab", "expires": "2001-03-01T00:00:00Z"}]}`,
		"long digest":       `{"sessions": [{"digest": "` + digest + `ab", "expires": "2001-03-01T00:00:00Z"}]}`,
		"digest of letters": `{"sessions": [{"digest": "` + strings.Repeat("xy", 32) + `", "expires": "2001-03-01T00:00:00Z"}]}`,
		"no digest":         `{"sessions": [{"expires": "2001-03-01T00:00:00Z"}]}`,
		"expiry not a time": `{"sessions": [{"digest": "` + digest + `", "expires": "soon"}]}`,
	}
	for name, content := range damaged {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "sessions.json")
			if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			f, log := newRun(t, file, password)

			wantStatus(t, f.do("GET", "/api/status", "", nil), http.StatusUnauthorized)
			if !strings.Contains(log.String(), "level=WARN") || !strings.Contains(log.String(), "could not read the saved sessions") {
				t.Errorf("the log does not warn that the sessions could not be read:\n%s", log.String())
			}

			// Signing in works as usual, and puts the file right.
			signedIn := f.signIn(t)
			wantStatus(t, f.do("GET", "/api/status", "", signedIn), http.StatusOK)
			if got := savedSessionCount(t, file); got != 1 {
				t.Errorf("the file holds %d sessions after signing in, want 1", got)
			}
			after, _ := newRun(t, file, password)
			wantStatus(t, after.do("GET", "/api/status", "", signedIn), http.StatusOK)
		})
	}
}

func TestOneBadSessionSpoilsTheFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sessions.json")
	before, _ := newRun(t, file, password)
	signedIn := before.signIn(t)

	// Add an entry that cannot be right beside the good one. A file that
	// kasad did not write is not half believed.
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	spoiled := strings.Replace(string(data), `"sessions": [`, `"sessions": [{"digest": "abab", "expires": "2001-03-01T00:00:00Z"},`, 1)
	if spoiled == string(data) {
		t.Fatalf("could not add an entry to the file:\n%s", data)
	}
	if err := os.WriteFile(file, []byte(spoiled), 0o600); err != nil {
		t.Fatal(err)
	}

	after, _ := newRun(t, file, password)
	wantStatus(t, after.do("GET", "/api/status", "", signedIn), http.StatusUnauthorized)
}

func TestSigningInWorksWhenSessionsCannotBeSaved(t *testing.T) {
	// The directory the file belongs in does not exist.
	file := filepath.Join(t.TempDir(), "missing", "sessions.json")
	f, log := newRun(t, file, password)

	signedIn := f.signIn(t)
	wantStatus(t, f.do("GET", "/api/status", "", signedIn), http.StatusOK)
	if !strings.Contains(log.String(), "level=ERROR") || !strings.Contains(log.String(), "could not save the sessions") {
		t.Errorf("the log does not say that the sessions could not be saved:\n%s", log.String())
	}
	wantStatus(t, f.do("DELETE", "/api/session", "", signedIn), http.StatusOK)
	wantStatus(t, f.do("GET", "/api/status", "", signedIn), http.StatusUnauthorized)
}

func TestSessionFileIsLeftAloneWhenNotInUse(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sessions.json")
	before, _ := newRun(t, file, password)
	signedIn := before.signIn(t)
	saved, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	// With no password there is nothing to sign in to, and so no use for
	// the file. Requests neither read it nor change it.
	open, log := newRun(t, file, "")
	wantSession(t, open.do("GET", "/api/session", "", signedIn), false, false, true)
	wantSession(t, open.do("DELETE", "/api/session", "", signedIn), false, false, true)
	if strings.Contains(log.String(), "sessions") {
		t.Errorf("the log speaks of sessions when there is no password:\n%s", log.String())
	}
	if now, err := os.ReadFile(file); err != nil || string(now) != string(saved) {
		t.Errorf("the file changed while there was no password (error %v):\n%s", err, now)
	}
}

func TestSigningOutWithoutASessionSavesNothing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sessions.json")
	f, _ := newRun(t, file, password)
	wantStatus(t, f.do("DELETE", "/api/session", "", nil), http.StatusOK)
	wantStatus(t, f.do("DELETE", "/api/session", "", map[string]string{"Cookie": "kasad_session=made-up"}), http.StatusOK)
	if _, err := os.Stat(file); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a file was written though no session began or ended (error %v)", err)
	}
}
