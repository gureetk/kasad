package web

import (
	"bytes"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func get(t *testing.T, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestServesThePageAtTheRoot(t *testing.T) {
	response := get(t, "/", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if !bytes.Equal(response.Body.Bytes(), assets[indexFile].body) {
		t.Error("GET / did not return index.html")
	}
}

func TestSetsHeadersThatConfineThePage(t *testing.T) {
	header := get(t, "/", nil).Header()
	for name, want := range map[string]string{
		"Content-Security-Policy": contentSecurityPolicy,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-cache",
	} {
		if got := header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestServesEachFileWithItsType(t *testing.T) {
	for name, want := range map[string]string{
		"/index.html":           "text/html; charset=utf-8",
		"/app.css":              "text/css; charset=utf-8",
		"/app.js":               "text/javascript; charset=utf-8",
		"/manifest.webmanifest": "application/manifest+json",
		"/icon.svg":             "image/svg+xml",
		"/icon-192.png":         "image/png",
	} {
		response := get(t, name, nil)
		if response.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", name, response.Code)
			continue
		}
		if got := response.Header().Get("Content-Type"); got != want {
			t.Errorf("GET %s: Content-Type = %q, want %q", name, got, want)
		}
	}
}

func TestAnswersNotModifiedForACurrentCopy(t *testing.T) {
	first := get(t, "/app.js", nil)
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	current := get(t, "/app.js", map[string]string{"If-None-Match": etag})
	if current.Code != http.StatusNotModified || current.Body.Len() != 0 {
		t.Errorf("with the current ETag: %d and %d bytes, want 304 and none", current.Code, current.Body.Len())
	}
	stale := get(t, "/app.js", map[string]string{"If-None-Match": `"something-else"`})
	if stale.Code != http.StatusOK {
		t.Errorf("with an old ETag: %d, want 200", stale.Code)
	}
}

func TestRefusesWhatItDoesNotServe(t *testing.T) {
	for _, path := range []string{"/nothing-here", "/static/index.html", "/../web.go", "/index.html/"} {
		if response := get(t, path, nil); response.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, response.Code)
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))
	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST / = %d with Allow %q, want 405 with GET, HEAD", recorder.Code, recorder.Header().Get("Allow"))
	}
}

func TestHeadSendsNoBody(t *testing.T) {
	request := httptest.NewRequest(http.MethodHead, "/", nil)
	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Errorf("HEAD / = %d with %d bytes, want 200 with none", recorder.Code, recorder.Body.Len())
	}
	if got := recorder.Header().Get("Content-Length"); got != strconv.Itoa(len(assets[indexFile].body)) {
		t.Errorf("Content-Length = %q", got)
	}
}

// The policy the page is served with allows nothing inline, so anything
// inline in the page would silently not work.
func TestThePageHasNothingInline(t *testing.T) {
	page := string(assets[indexFile].body)
	for what, pattern := range map[string]string{
		"a style attribute":  `\sstyle\s*=`,
		"a <style> element":  `<style[\s>]`,
		"an inline script":   `<script(?:\s+type="module")?\s*>`,
		"an event attribute": `\son[a-z]+\s*=`,
		"a javascript: link": `javascript:`,
	} {
		if regexp.MustCompile(pattern).MatchString(page) {
			t.Errorf("index.html has %s", what)
		}
	}
}

var (
	pageLinks = regexp.MustCompile(`(?:href|src)="([^"#][^"]*)"`)
	jsImports = regexp.MustCompile(`from '\./([^']+)'`)
	iconUses  = regexp.MustCompile(`href="#(i-[a-z]+)"`)
	iconNames = regexp.MustCompile(`#i-\$\{name\}|icon\('([a-z]+)'\)`)
)

func TestEverythingThePageRefersToExists(t *testing.T) {
	page := string(assets[indexFile].body)
	for _, match := range pageLinks.FindAllStringSubmatch(page, -1) {
		if _, ok := assets[match[1]]; !ok {
			t.Errorf("index.html refers to %q, which is not served", match[1])
		}
	}
	for name, file := range assets {
		if !strings.HasSuffix(name, ".js") {
			continue
		}
		for _, match := range jsImports.FindAllStringSubmatch(string(file.body), -1) {
			if _, ok := assets[match[1]]; !ok {
				t.Errorf("%s imports %q, which is not served", name, match[1])
			}
		}
	}

	// Icons are drawn from symbols defined once in the page.
	used := map[string]bool{}
	for _, match := range iconUses.FindAllStringSubmatch(page, -1) {
		used[match[1]] = true
	}
	for _, match := range iconNames.FindAllStringSubmatch(string(assets["app.js"].body), -1) {
		if match[1] != "" {
			used["i-"+match[1]] = true
		}
	}
	for id := range used {
		if !strings.Contains(page, `<symbol id="`+id+`"`) {
			t.Errorf("the icon %q is used but not defined", id)
		}
	}
}

func TestTheManifestDescribesTheIconsServed(t *testing.T) {
	var manifest struct {
		Name     string `json:"name"`
		StartURL string `json:"start_url"`
		Display  string `json:"display"`
		Icons    []struct {
			Src   string `json:"src"`
			Sizes string `json:"sizes"`
			Type  string `json:"type"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(assets["manifest.webmanifest"].body, &manifest); err != nil {
		t.Fatalf("the manifest is not JSON: %v", err)
	}
	if manifest.Name == "" || manifest.StartURL == "" || manifest.Display != "standalone" {
		t.Errorf("the manifest lacks what a browser needs to install the app: %+v", manifest)
	}

	sizes := map[string]bool{}
	for _, icon := range manifest.Icons {
		file, ok := assets[icon.Src]
		if !ok {
			t.Errorf("the manifest lists %q, which is not served", icon.Src)
			continue
		}
		if file.contentType != icon.Type {
			t.Errorf("%s is served as %q but listed as %q", icon.Src, file.contentType, icon.Type)
		}
		config, err := png.DecodeConfig(bytes.NewReader(file.body))
		if err != nil {
			t.Errorf("%s is not a PNG: %v", icon.Src, err)
			continue
		}
		if got := strconv.Itoa(config.Width) + "x" + strconv.Itoa(config.Height); got != icon.Sizes {
			t.Errorf("%s is %s but listed as %s", icon.Src, got, icon.Sizes)
		}
		sizes[icon.Sizes] = true
	}
	// Browsers ask for icons of both these sizes before offering to install.
	for _, size := range []string{"192x192", "512x512"} {
		if !sizes[size] {
			t.Errorf("the manifest has no %s icon", size)
		}
	}
}
