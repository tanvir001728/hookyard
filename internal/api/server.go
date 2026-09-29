// Package api implements Hookyard's HTTP API.
package api

import (
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
	log    *slog.Logger
	mux    *http.ServeMux
	checks map[string]ReadinessCheck
	v1     *V1
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
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /readyz", s.handleReadyz)

	if s.v1 != nil {
		v1 := func(pattern string, h http.HandlerFunc) {
			s.mux.Handle(pattern, s.requireToken(h))
		}
		v1("POST /v1/requests", s.handleCreateRequest)
	}
	// Unmatched /v1 routes get a JSON 404 instead of the default text page.
	s.mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, codeNotFound, fmt.Sprintf("no route for %s %s", r.Method, r.URL.Path))
	})
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
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
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
