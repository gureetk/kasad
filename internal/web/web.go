// Package web serves kasad's web app: a page that shows the plug, switches it
// and edits the schedule, by way of the JSON API.
//
// The app is a handful of static files that are compiled into the binary. It
// has no build step and loads nothing from anywhere else.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed static
var static embed.FS

// staticDir is the directory in static that holds the app's files.
const staticDir = "static"

// indexFile is the file served for the root path.
const indexFile = "index.html"

// contentTypes maps the extensions the app uses to their media types. The
// types are spelt out here, not taken from the system's tables, so that a
// file is served the same way on every machine.
var contentTypes = map[string]string{
	".css":         "text/css; charset=utf-8",
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".png":         "image/png",
	".svg":         "image/svg+xml",
	".webmanifest": "application/manifest+json",
}

// contentSecurityPolicy confines the page to files and requests from the
// server that sent it. Nothing in the app is inline, so nothing inline is
// allowed.
const contentSecurityPolicy = "default-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self'; " +
	"img-src 'self'; " +
	"connect-src 'self'; " +
	"manifest-src 'self'; " +
	"base-uri 'none'; " +
	"form-action 'none'; " +
	"frame-ancestors 'none'"

// asset is one file of the app, ready to serve.
type asset struct {
	body        []byte
	contentType string
	etag        string
}

// assets holds the app's files by the path they are served at, without the
// leading slash.
var assets = mustLoad(static)

// mustLoad reads every file of the app. It panics if a file cannot be read
// or has an extension with no known media type, which the package's tests
// would catch.
func mustLoad(files fs.FS) map[string]asset {
	loaded := make(map[string]asset)
	err := fs.WalkDir(files, staticDir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		contentType, ok := contentTypes[path.Ext(name)]
		if !ok {
			return fmt.Errorf("%s: no media type for this kind of file", name)
		}
		sum := sha256.Sum256(body)
		loaded[strings.TrimPrefix(name, staticDir+"/")] = asset{
			body:        body,
			contentType: contentType,
			etag:        `"` + hex.EncodeToString(sum[:8]) + `"`,
		}
		return nil
	})
	if err != nil {
		panic("web: " + err.Error())
	}
	return loaded
}

// Handler returns the handler for the web app. It serves the page at the
// root path and the page's files beside it.
//
// The files hold nothing secret, so they are served to anyone who asks. What
// the page can then do is decided by the API.
func Handler() http.Handler {
	return http.HandlerFunc(serve)
}

func serve(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		header.Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = indexFile
	}
	file, ok := assets[name]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	header.Set("Content-Type", file.contentType)
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	// Let browsers keep a copy, but have them check it is current before
	// each use, so that a new kasad is never shown through an old page.
	header.Set("Cache-Control", "no-cache")
	header.Set("ETag", file.etag)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(file.body))
}
