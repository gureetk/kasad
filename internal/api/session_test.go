package api_test

import (
	"net/http"
	"strings"
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
