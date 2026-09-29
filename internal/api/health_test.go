package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(opts ...Option) *Server {
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)), opts...)
}

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestServer().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestReadyz(t *testing.T) {
	ok := func(context.Context) error { return nil }
	down := func(context.Context) error { return errors.New("connection refused") }

	tests := []struct {
		name       string
		opts       []Option
		wantStatus int
		wantBody   string
	}{
		{"no checks", nil, http.StatusOK, "ok"},
		{"all passing", []Option{WithReadinessCheck("database", ok)}, http.StatusOK, "ok"},
		{"one failing", []Option{WithReadinessCheck("database", down), WithReadinessCheck("other", ok)}, http.StatusServiceUnavailable, "fail"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newTestServer(tt.opts...).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			var body readinessResponse
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Status != tt.wantBody {
				t.Errorf("body status = %q, want %q", body.Status, tt.wantBody)
			}
			if len(body.Checks) != len(tt.opts) {
				t.Errorf("got %d check results, want %d", len(body.Checks), len(tt.opts))
			}
		})
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestServer().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestPanicIsRecovered(t *testing.T) {
	s := newTestServer()
	s.mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("boom") })

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
