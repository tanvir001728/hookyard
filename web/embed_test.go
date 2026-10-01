package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))
	return rec
}

func TestSPA(t *testing.T) {
	h := newHandler(fstest.MapFS{
		"index.html":         {Data: []byte("<div id=root></div>")},
		"assets/app-1a2b.js": {Data: []byte("console.log(1)")},
		"favicon.svg":        {Data: []byte("<svg/>")},
	})

	tests := []struct {
		path, wantBody, wantCache string
		wantStatus                int
	}{
		{"/", "<div id=root></div>", "no-cache", 200},
		{"/requests/req_123", "<div id=root></div>", "no-cache", 200}, // client-side route
		{"/assets/app-1a2b.js", "console.log(1)", "public, max-age=31536000, immutable", 200},
		{"/favicon.svg", "<svg/>", "no-cache", 200},
		{"/assets/missing.js", "404 page not found\n", "", 404},
	}
	for _, tt := range tests {
		rec := get(t, h, tt.path)
		if rec.Code != tt.wantStatus || rec.Body.String() != tt.wantBody || rec.Header().Get("Cache-Control") != tt.wantCache {
			t.Errorf("%s: status %d cache %q", tt.path, rec.Code, rec.Header().Get("Cache-Control"))
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || rec.Header().Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: security headers missing", tt.path)
		}
	}
}

func TestNotBuilt(t *testing.T) {
	rec := get(t, newHandler(fstest.MapFS{".gitkeep": {}}), "/")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "make web build") {
		t.Errorf("status %d", rec.Code)
	}
}
