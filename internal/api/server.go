// Package api implements Hookyard's HTTP API.
package api

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/tanvir001728/hookyard/internal/config"
	"github.com/tanvir001728/hookyard/internal/store"
)

// Server is the root HTTP handler for Hookyard.
type Server struct {
	log     *slog.Logger
	mux     *http.ServeMux
	checks  map[string]ReadinessCheck
	v1      *V1
	metrics http.Handler
	// dashboard serves the web UI; nil disables it.
	dashboard http.Handler

	hashedTokens []hashedToken
	sessionKey   []byte
}

// V1 holds the dependencies of the /v1 API.
type V1 struct {
	Store   *store.Store
	Config  *config.File
	Tokens  []config.APIToken
	MaxBody int64
	// Notify, if set, is called after a request is enqueued so delivery can
	// start without waiting for the next poll.
	Notify func()
	// Monitor, if set, provides live upstream state and is told about pauses.
	Monitor UpstreamMonitor
}

// WithDashboard serves the web dashboard h at / (for every path not handled
// by the API).
func WithDashboard(h http.Handler) Option {
	return func(s *Server) { s.dashboard = h }
}

// WithMetrics serves h at /metrics (Prometheus format, unauthenticated).
func WithMetrics(h http.Handler) Option {
	return func(s *Server) { s.metrics = h }
}

// WithV1 enables the /v1 API.
func WithV1(v1 V1) Option {
	return func(s *Server) { s.v1 = &v1 }
}

// Option configures a Server.
type Option func(*Server)

// WithReadinessCheck registers a named dependency check used by /readyz.
func WithReadinessCheck(name string, check ReadinessCheck) Option {
	return func(s *Server) { s.checks[name] = check }
}

// New builds a Server with all routes registered.
func New(log *slog.Logger, opts ...Option) *Server {
	s := &Server{
		log:    log,
		mux:    http.NewServeMux(),
		checks: make(map[string]ReadinessCheck),
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.v1 != nil {
		secrets := make([]string, len(s.v1.Tokens))
		for i, t := range s.v1.Tokens {
			s.hashedTokens = append(s.hashedTokens, hashedToken{name: t.Name, sum: sha256.Sum256([]byte(t.Secret))})
			secrets[i] = t.Secret
		}
		s.sessionKey = sessionKey(secrets)
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /readyz", s.handleReadyz)
	if s.metrics != nil {
		s.mux.Handle("GET /metrics", s.metrics)
	}

	if s.v1 != nil {
		v1 := func(pattern string, h http.HandlerFunc) {
			s.mux.Handle(pattern, s.requireToken(h))
		}
		v1("POST /v1/requests", s.handleCreateRequest)
		v1("GET /v1/requests", s.handleListRequests)
		v1("GET /v1/requests/{id}", s.handleGetRequest)
		v1("GET /v1/requests/{id}/attempts", s.handleListAttempts)
		v1("POST /v1/requests/{id}/replay", s.handleReplayRequest)
		v1("POST /v1/requests/{id}/cancel", s.handleCancelRequest)
		v1("POST /v1/requests/{id}/resolve", s.handleResolveRequest)
		v1("GET /v1/dlq", s.handleDLQSummary)
		v1("POST /v1/dlq/replay", s.handleDLQReplay)
		v1("GET /v1/upstreams", s.handleListUpstreams)
		v1("GET /v1/upstreams/{name}", s.handleGetUpstream)
		v1("POST /v1/upstreams/{name}/pause", s.handlePauseUpstream)
		v1("POST /v1/upstreams/{name}/resume", s.handleResumeUpstream)
		v1("GET /v1/upstreams/{name}/events", s.handleUpstreamEvents)
		v1("GET /v1/stats/overview", s.handleStatsOverview)
		v1("GET /v1/stats/timeseries", s.handleStatsTimeseries)

		// Dashboard sessions. Internal to the dashboard, not part of the
		// public API.
		s.mux.HandleFunc("POST /ui/session", s.handleCreateSession)
		s.mux.Handle("GET /ui/session", s.requireToken(http.HandlerFunc(s.handleGetSession)))
		s.mux.HandleFunc("DELETE /ui/session", s.handleDeleteSession)
	}
	// Unmatched API routes get a JSON 404 instead of the default text page
	// (or the dashboard).
	notFound := func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, codeNotFound, fmt.Sprintf("no route for %s %s", r.Method, r.URL.Path))
	}
	s.mux.HandleFunc("/v1/", notFound)
	s.mux.HandleFunc("/ui/", notFound)

	if s.dashboard != nil {
		// Registered without a method: "GET /" would conflict with "/v1/".
		s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				writeError(w, http.StatusMethodNotAllowed, codeNotFound, "method not allowed")
				return
			}
			s.dashboard.ServeHTTP(w, r)
		})
	}
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.withRecover(s.withAccessLog(s.mux)).ServeHTTP(w, r)
}

func (s *Server) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		// Probes are frequent and uninteresting; keep them out of info logs.
		level := slog.LevelInfo
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics" {
			level = slog.LevelDebug
		}
		s.log.Log(r.Context(), level, "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start),
		)
	})
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				s.log.Error("panic serving request", "panic", v, "path", r.URL.Path, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, codeInternal, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
