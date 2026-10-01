// Package web embeds the dashboard's production build and serves it as a
// single-page application.
//
// Build the assets with `make web` before `make build`; without them the
// server still compiles and shows a page explaining how to build them.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// contentSecurityPolicy allows only same-origin resources. Inline style
// attributes are needed by the charting library.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; " +
	"base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// Handler serves the embedded dashboard. Unknown paths get index.html so the
// client-side router can handle them.
func Handler() http.Handler {
	assets, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory exists
	}
	return newHandler(assets)
}

func newHandler(assets fs.FS) http.Handler {
	files := http.FileServerFS(assets)
	index, indexErr := fs.ReadFile(assets, "index.html")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")

		if indexErr != nil {
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(notBuiltPage))
			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if info, err := fs.Stat(assets, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Vite puts a content hash in these file names.
					h.Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					h.Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
				return
			}
			// A missing asset is a real 404, not a client-side route.
			if strings.HasPrefix(name, "assets/") {
				http.NotFound(w, r)
				return
			}
		}

		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}

const notBuiltPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Hookyard</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:40rem;margin:4rem auto;padding:0 1rem;color:#18181b}
code{background:#f4f4f5;padding:.1rem .3rem;border-radius:.25rem}</style></head>
<body><h1>Hookyard is running</h1>
<p>The API is available at <code>/v1</code>, but this binary was built without the dashboard.</p>
<p>Build it with <code>make web build</code>, or use the official Docker image, which includes it.</p>
</body></html>`
