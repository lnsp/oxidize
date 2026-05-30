// Package static embeds the built Oxide Console (console/dist, copied here by
// the Makefile) and serves it with a single-page-app fallback: real files are
// served as-is, every other path returns index.html so client-side routing
// works.
package static

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// securityHeaders mirror console/vercel.json so the deployed app behaves like
// the upstream production deployment.
const csp = "default-src 'self'; style-src 'unsafe-inline' 'self'; frame-src 'none'; object-src 'none'; form-action 'none'; frame-ancestors 'none'"

// Handler serves the embedded UI with SPA fallback. /v1/* and other API routes
// are expected to be handled by the caller's mux before reaching this handler.
func Handler() http.Handler {
	dist, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)

		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" || p == "index.html" {
			serveIndex(w, index)
			return
		}

		// If the path maps to a real embedded file, serve it (with long cache
		// for fingerprinted assets). Otherwise fall back to index.html.
		if f, err := dist.Open(p); err == nil {
			if st, statErr := f.Stat(); statErr == nil && !st.IsDir() {
				f.Close()
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
			f.Close()
		}

		serveIndex(w, index)
	})
}

func serveIndex(w http.ResponseWriter, index []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(index)
}

func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
}
