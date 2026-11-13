package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"regexp"
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
	return startServeWithEnv(t, mock, dir, nil, flags...)
}

// startServeWithEnv is startServe with environment variables set.
func startServeWithEnv(t *testing.T, mock *mockplug.Plug, dir string, vars map[string]string, flags ...string) *serving {
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
			getenv:        func(name string) string { return vars[name] },
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
// waitForApplied waits for serve to say it has applied the rule with the
// given id, which it does once the plug has confirmed the switch and the
// progress is saved. The rule's id alone is not enough to wait for, since
// serve also names the next rule when it starts.
func (s *serving) waitForApplied(id string) {
	s.t.Helper()
	s.waitForLog(`msg="applied rule" rule=` + id + " ")
}

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
	s.waitForApplied("soon-on")

	s.clock.Set(at(4, 10))
	s.waitFor("the plug to be switched off", func() bool { return !mock.RelayOn() })
	s.waitForApplied("soon-off")

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
	s.waitForApplied("missed")
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

var apiAddrPattern = regexp.MustCompile(`msg="serving the web app and API" addr=(\S+)`)

// apiURL waits for serve to say where its API is and returns the URL of path
// there.
func (s *serving) apiURL(path string) string {
	s.t.Helper()
	s.waitForLog("serving the web app and API")
	match := apiAddrPattern.FindStringSubmatch(s.stderr.String())
	if match == nil {
		s.t.Fatalf("could not find the API address in the log:\n%s", s.stderr.String())
	}
	return "http://" + match[1] + path
}

// request sends an API request and returns the status code and body.
func (s *serving) request(method, path, body string, headers map[string]string) (int, string) {
	s.t.Helper()
	request, err := http.NewRequest(method, s.apiURL(path), strings.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		s.t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return response.StatusCode, string(data)
}

func TestServeOffersNoAPIUnlessAsked(t *testing.T) {
	mock := startMock(t)
	s := startServe(t, mock, t.TempDir(), "-sync-interval", "0")
	s.waitForLog("starting")
	s.settle()
	s.stop()
	if strings.Contains(s.stderr.String(), "serving the web app and API") {
		t.Errorf("serve started the API without -listen:\n%s", s.stderr.String())
	}
}

func TestServeAPI(t *testing.T) {
	mock := startMock(t)
	s := startServe(t, mock, t.TempDir(), "-sync-interval", "0", "-listen", "127.0.0.1:0")

	code, body := s.request("GET", "/api/status", "", nil)
	if code != http.StatusOK || !strings.Contains(body, `"reachable": true`) {
		t.Errorf("GET /api/status = %d:\n%s", code, body)
	}

	code, body = s.request("POST", "/api/relay", `{"on": true}`, nil)
	if code != http.StatusOK {
		t.Errorf("POST /api/relay = %d:\n%s", code, body)
	}
	if !mock.RelayOn() {
		t.Error("the plug was not switched on through the API")
	}

	if code := s.stop(); code != exitOK {
		t.Errorf("exit code = %d, want %d. Log:\n%s", code, exitOK, s.stderr.String())
	}
	// Loopback only, so no warning about an open API.
	if strings.Contains(s.stderr.String(), "ask for no password or token") {
		t.Errorf("serve warned about an API that only this machine can reach:\n%s", s.stderr.String())
	}
}

func TestServeAPIScheduleChangeNeedsNoRestart(t *testing.T) {
	mock := startMock(t)
	dir := t.TempDir()
	s := startServe(t, mock, dir, "-sync-interval", "0", "-listen", "127.0.0.1:0")
	s.waitForLog("starting from now")

	code, body := s.request("PUT", "/api/schedule", soonSchedule, nil)
	if code != http.StatusOK {
		t.Fatalf("PUT /api/schedule = %d:\n%s", code, body)
	}
	// Wait for the scheduler to take the new schedule on before moving the
	// clock: a schedule starts from the moment it is noticed.
	s.waitFor("the scheduler to notice the new schedule", func() bool {
		return strings.Count(s.stderr.String(), "starting from now") == 2
	})

	s.clock.Set(at(4, 6))
	s.waitFor("the plug to be switched on", mock.RelayOn)
	s.waitForApplied("soon-on")
	s.stop()

	saved, err := schedule.Load(filepath.Join(dir, scheduleFile))
	if err != nil {
		t.Fatalf("the schedule file was not written: %v", err)
	}
	if len(saved.Rules) != 2 {
		t.Errorf("the schedule file holds %d rules, want 2", len(saved.Rules))
	}
}

func TestServeAPIToken(t *testing.T) {
	mock := startMock(t)
	vars := map[string]string{tokenEnv: "s3cret"}
	s := startServeWithEnv(t, mock, t.TempDir(), vars, "-sync-interval", "0", "-listen", "127.0.0.1:0")

	if code, body := s.request("GET", "/api/status", "", nil); code != http.StatusUnauthorized {
		t.Errorf("without the token: %d:\n%s", code, body)
	}
	if code, body := s.request("GET", "/api/status", "", map[string]string{"Authorization": "Bearer s3cret"}); code != http.StatusOK {
		t.Errorf("with the token: %d:\n%s", code, body)
	}
	s.stop()
	if strings.Contains(s.stderr.String(), "s3cret") {
		t.Errorf("the token appears in the log:\n%s", s.stderr.String())
	}
}

func TestServeWebApp(t *testing.T) {
	mock := startMock(t)
	vars := map[string]string{tokenEnv: "s3cret"}
	s := startServeWithEnv(t, mock, t.TempDir(), vars, "-sync-interval", "0", "-listen", "127.0.0.1:0")

	// The page is served without the token, since a browser cannot send one
	// when it first asks. The page can do nothing until it is given one.
	code, body := s.request("GET", "/", "", nil)
	if code != http.StatusOK || !strings.Contains(body, "<title>kasad</title>") {
		t.Errorf("GET / = %d:\n%s", code, body)
	}
	if code, body := s.request("GET", "/app.js", "", nil); code != http.StatusOK {
		t.Errorf("GET /app.js = %d:\n%s", code, body)
	}
	if code, body := s.request("GET", "/api/status", "", nil); code != http.StatusUnauthorized {
		t.Errorf("GET /api/status without the token = %d:\n%s", code, body)
	}
	if code, body := s.request("GET", "/no-such-page", "", nil); code != http.StatusNotFound {
		t.Errorf("GET /no-such-page = %d:\n%s", code, body)
	}
	s.stop()
}

func TestServeOffersThePlugsOwnRules(t *testing.T) {
	mock := startMock(t)
	mock.SetRules(plugRules...)
	s := startServe(t, mock, t.TempDir(), "-sync-interval", "0", "-listen", "127.0.0.1:0")

	code, body := s.request("GET", "/api/plug/rules", "", nil)
	if code != http.StatusOK || !strings.Contains(body, `"id": "00000000000000000000000000000002"`) {
		t.Fatalf("GET /api/plug/rules = %d:\n%s", code, body)
	}

	code, body = s.request("DELETE", "/api/plug/rules/00000000000000000000000000000002", "", nil)
	if code != http.StatusOK {
		t.Fatalf("DELETE = %d:\n%s", code, body)
	}
	if left := mock.Rules(); len(left) != 2 {
		t.Errorf("%d rules left on the plug, want 2", len(left))
	}
	s.waitForLog("deleted a rule from the plug")
	s.stop()
}

func TestServeWarnsAboutAnOpenAPI(t *testing.T) {
	mock := startMock(t)

	open := startServe(t, mock, t.TempDir(), "-sync-interval", "0", "-listen", "0.0.0.0:0")
	open.waitForLog("serving the web app and API")
	open.waitForLog("ask for no password or token")
	open.stop()

	for name, vars := range map[string]map[string]string{
		"a token":    {tokenEnv: "s3cret"},
		"a password": {passwordEnv: "correct horse"},
	} {
		guarded := startServeWithEnv(t, mock, t.TempDir(), vars, "-sync-interval", "0", "-listen", "0.0.0.0:0")
		guarded.waitForLog("serving the web app and API")
		guarded.settle()
		guarded.stop()
		if strings.Contains(guarded.stderr.String(), "ask for no password or token") {
			t.Errorf("serve warned about an API that has %s:\n%s", name, guarded.stderr.String())
		}
	}
}

func TestServeSaysWhatAPasswordOverPlainHTTPMeans(t *testing.T) {
	mock := startMock(t)
	vars := map[string]string{passwordEnv: "correct horse"}

	// Reachable from other machines: say once that the password crosses
	// the network in the clear.
	reachable := startServeWithEnv(t, mock, t.TempDir(), vars, "-sync-interval", "0", "-listen", "0.0.0.0:0")
	reachable.waitForLog(`password=true token=false`)
	reachable.waitForLog("meant for a network you trust")
	reachable.stop()
	if strings.Contains(reachable.stderr.String(), "is short") {
		t.Errorf("serve called a 13-character password short:\n%s", reachable.stderr.String())
	}
	if strings.Contains(reachable.stderr.String(), "correct horse") {
		t.Errorf("the password is in the log:\n%s", reachable.stderr.String())
	}

	// On this machine only, there is no network to watch.
	local := startServeWithEnv(t, mock, t.TempDir(), vars, "-sync-interval", "0", "-listen", "127.0.0.1:0")
	local.waitForLog("serving the web app and API")
	local.settle()
	local.stop()
	if strings.Contains(local.stderr.String(), "plain HTTP") {
		t.Errorf("serve mentioned the network though only this machine can reach it:\n%s", local.stderr.String())
	}

	short := startServeWithEnv(t, mock, t.TempDir(), map[string]string{passwordEnv: "hunter2"}, "-sync-interval", "0", "-listen", "127.0.0.1:0")
	short.waitForLog("is short")
	short.stop()
	if strings.Contains(short.stderr.String(), "hunter2") {
		t.Errorf("the password is in the log:\n%s", short.stderr.String())
	}
}

func TestServeSignIn(t *testing.T) {
	mock := startMock(t)
	vars := map[string]string{passwordEnv: "correct horse"}
	s := startServeWithEnv(t, mock, t.TempDir(), vars, "-sync-interval", "0", "-listen", "127.0.0.1:0")

	// The page is there for anyone, so that there is somewhere to sign in.
	if code, body := s.request("GET", "/", "", nil); code != http.StatusOK {
		t.Fatalf("GET / = %d:\n%s", code, body)
	}
	if code, body := s.request("GET", "/api/status", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("GET /api/status before signing in = %d:\n%s", code, body)
	}

	// A browser keeps the cookie that signing in sets, and sends it back.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Timeout: 10 * time.Second, Jar: jar}
	post := func(path, body string) int {
		t.Helper()
		response, err := browser.Post(s.apiURL(path), "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if code := post("/api/session", `{"password": "wrong"}`); code != http.StatusUnauthorized {
		t.Errorf("signing in with the wrong password = %d, want 401", code)
	}
	if code := post("/api/session", `{"password": "correct horse"}`); code != http.StatusOK {
		t.Fatalf("signing in = %d, want 200", code)
	}
	if code := post("/api/relay", `{"on": true}`); code != http.StatusOK || !mock.RelayOn() {
		t.Errorf("switching the plug once signed in = %d, plug on = %v", code, mock.RelayOn())
	}

	s.waitForLog("wrong password given to the web app")
	s.waitForLog("signed in to the web app")
	s.stop()
	if strings.Contains(s.stderr.String(), "correct horse") {
		t.Errorf("the password is in the log:\n%s", s.stderr.String())
	}
}

func TestServeKeepsPeopleSignedInAcrossARestart(t *testing.T) {
	mock := startMock(t)
	dir := t.TempDir()
	start := func(password string) *serving {
		vars := map[string]string{passwordEnv: password}
		return startServeWithEnv(t, mock, dir, vars, "-sync-interval", "0", "-listen", "127.0.0.1:0")
	}

	// One browser throughout. Each run listens on a port of its own, which
	// makes no difference to which cookies a browser sends.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Timeout: 10 * time.Second, Jar: jar}
	status := func(s *serving) int {
		t.Helper()
		response, err := browser.Get(s.apiURL("/api/status"))
		if err != nil {
			t.Fatalf("GET /api/status: %v", err)
		}
		response.Body.Close()
		return response.StatusCode
	}

	first := start("correct horse")
	response, err := browser.Post(first.apiURL("/api/session"), "application/json",
		strings.NewReader(`{"password": "correct horse"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("signing in = %d, want 200", response.StatusCode)
	}
	var id string
	for _, cookie := range response.Cookies() {
		id = cookie.Value
	}
	if id == "" {
		t.Fatal("signing in set no cookie")
	}
	if code := status(first); code != http.StatusOK {
		t.Fatalf("GET /api/status once signed in = %d, want 200", code)
	}
	first.stop()

	// The sessions are in the data directory, in a form that is no use
	// for signing in.
	saved, err := os.ReadFile(filepath.Join(dir, sessionsFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), id) || strings.Contains(string(saved), "correct horse") {
		t.Errorf("the session file holds the cookie's value or the password:\n%s", saved)
	}

	second := start("correct horse")
	if code := status(second); code != http.StatusOK {
		t.Errorf("GET /api/status after a restart = %d, want 200. Log:\n%s", code, second.stderr.String())
	}
	second.waitForLog(`msg="sessions carried over from the last run" sessions=1`)
	second.stop()

	// Changing the password is how to sign everyone out.
	third := start("another password")
	if code := status(third); code != http.StatusUnauthorized {
		t.Errorf("GET /api/status after the password changed = %d, want 401", code)
	}
}

func TestServeFailsWhenTheAPIAddressIsTaken(t *testing.T) {
	mock := startMock(t)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer taken.Close()

	got := kasad(t, nil, "-host", mock.Addr(), "-data", t.TempDir(), "-listen", taken.Addr().String(), "serve")
	if got.code != exitFailed || !strings.Contains(got.stderr, "-listen:") {
		t.Errorf("exit code = %d, stderr = %q", got.code, got.stderr)
	}
	if mock.Requests() != 0 {
		t.Errorf("serve contacted the plug %d times before failing to start", mock.Requests())
	}
}
