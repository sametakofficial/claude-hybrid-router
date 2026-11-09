package panel

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStaticFilesAndHeaders(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>x</title>")},
		"css/base.css":         {Data: []byte("body{}")},
		"js/main.js":           {Data: []byte("import './lib/dom.js'")},
		"js/lib/dom.js":        {Data: []byte("export {}")},
		"js/views/settings.js": {Data: []byte("export {}")},
	}
	_, h, _ := setup(t)
	static := staticHandler(fsys)
	for path, want := range map[string]string{
		"/":                     "text/html; charset=utf-8",
		"/css/base.css":         "text/css; charset=utf-8",
		"/js/main.js":           "text/javascript; charset=utf-8",
		"/js/lib/dom.js":        "text/javascript; charset=utf-8",
		"/js/views/settings.js": "text/javascript; charset=utf-8",
	} {
		w := httptest.NewRecorder()
		static.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != want {
			t.Errorf("%s: %d %q", path, w.Code, w.Header().Get("Content-Type"))
		}
		if hasCSP := w.Header().Get("Content-Security-Policy") != ""; hasCSP != (path == "/") {
			t.Errorf("%s: CSP only on HTML, got %v", path, hasCSP)
		}
	}
	w := httptest.NewRecorder()
	static.ServeHTTP(w, httptest.NewRequest("GET", "/js/", nil))
	if w.Code != 404 {
		t.Errorf("directory listing must not be served: %d", w.Code)
	}

	// Through the full handler: embedded index, headers on page and API,
	// static files need no X-CHR, but still the Host check.
	for _, path := range []string{"/", "/api/status"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "127.0.0.1:3458"
		r.Header.Set("X-CHR", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		hd := w.Header()
		if w.Code != 200 || hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("X-Frame-Options") != "DENY" ||
			hd.Get("Referrer-Policy") != "no-referrer" || hd.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d %v", path, w.Code, hd)
		}
		if path == "/" && !strings.Contains(hd.Get("Content-Security-Policy"), "script-src 'self'") {
			t.Errorf("index CSP: %q", hd.Get("Content-Security-Policy"))
		}
	}
	if code, _ := do(h, "GET", "/", "", map[string]string{"X-CHR": ""}); code != 200 {
		t.Errorf("page must load without X-CHR: %d", code)
	}
	if code, _ := do(h, "GET", "/", "", map[string]string{"Host": "evil.example:3458"}); code != 403 {
		t.Errorf("page must check Host: %d", code)
	}
}

func TestWebDirOverride(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "js"), 0o755)
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("dev page"), 0o644)
	os.WriteFile(filepath.Join(dir, "js", "main.js"), []byte("dev js"), 0o644)
	t.Setenv("CLAUDE_HYBRID_WEB_DIR", dir)
	for path, want := range map[string]string{"/": "dev page", "/js/main.js": "dev js"} {
		w := httptest.NewRecorder()
		staticHandler(webFiles()).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || w.Body.String() != want {
			t.Errorf("%s: %d %q", path, w.Code, w.Body.String())
		}
	}
}
