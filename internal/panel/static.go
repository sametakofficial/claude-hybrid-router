package panel

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// The UI is the web/ directory: index.html, css/, js/ (ES modules, no
// build step), embedded into the binary.
//
//go:embed web
var webFS embed.FS

// csp allows only our own files; inline styles are allowed for style=""
// attributes set from JS.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Content types are set explicitly: the OS table (mime.TypeByExtension)
// may map .js to text/plain, and with nosniff the browser would then
// refuse to run the modules.
var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".ico":   "image/x-icon",
	".woff2": "font/woff2",
	".txt":   "text/plain; charset=utf-8",
}

// webFiles is the embedded UI, or the directory in CLAUDE_HYBRID_WEB_DIR
// (to edit the UI without rebuilding).
func webFiles() fs.FS {
	if dir := os.Getenv("CLAUDE_HYBRID_WEB_DIR"); dir != "" {
		return os.DirFS(dir)
	}
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err) // the embed always has web/
	}
	return sub
}

// staticHandler serves the UI files. Directory listings are not served.
func staticHandler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasSuffix(p, "/") && p != "/" {
			http.NotFound(w, r)
			return
		}
		ext := path.Ext(p)
		if p == "/" {
			ext = ".html"
		}
		if ext == ".html" {
			w.Header().Set("Content-Security-Policy", csp)
		}
		if ct, ok := contentTypes[ext]; ok {
			w.Header().Set("Content-Type", ct)
		}
		files.ServeHTTP(w, r)
	})
}

// securityHeaders are sent with every panel response.
func securityHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
}
