// Package flakyvendor implements an HTTP server that misbehaves on request,
// like real partner APIs do. It is used by Hookyard's tests and demos.
//
// Behavior is controlled with query parameters on any path, so a Hookyard
// request can pick it through its path, for example
// POST /shipments?fail_first=2&fail_status=503:
//
//	status=N          always respond with status N
//	fail_rate=F       fail a fraction F (0..1) of requests
//	fail_first=N      fail the first N requests for the same key, then succeed
//	key=K             counter key for fail_first (default: method and path)
//	fail_status=N     status used for failures (default 503)
//	retry_after=S     add Retry-After: S to failures (S seconds or an HTTP date)
//	fake_error=1      respond 200 OK with an error in the body
//	latency=D         wait D before responding (for example 200ms)
//	hang=1            never respond; wait until the client gives up
//
// Every response echoes the received request as JSON. Received requests are
// listed at GET /_requests and cleared, with all counters, by DELETE /_requests.
package flakyvendor

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// maxRecorded bounds memory use for long-running demos.
const maxRecorded = 1000

// Received is a request as seen by the vendor.
type Received struct {
	At      time.Time         `json:"at"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   string            `json:"query"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	// Attempt counts requests with the same key, starting at 1.
	Attempt int `json:"attempt"`
	// Status is the status code the vendor responded with (0 for hang).
	Status int `json:"status"`
}

// Server is the flaky vendor. The zero value is not usable; use New.
type Server struct {
	mu       sync.Mutex
	counts   map[string]int
	received []Received
	rand     func() float64
}

// New returns a flaky vendor server.
func New() *Server {
	return &Server{counts: map[string]int{}, rand: rand.Float64}
}

// Requests returns a copy of the requests received so far, oldest first.
func (s *Server) Requests() []Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Received(nil), s.received...)
}

// Reset clears recorded requests and fail_first counters.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts = map[string]int{}
	s.received = nil
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/_requests":
		s.handleRecorded(w, r)
		return
	case "/_health":
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	b, err := parseBehavior(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "flakyvendor: " + err.Error()})
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))

	key := b.key
	if key == "" {
		key = r.Method + " " + r.URL.Path
	}

	s.mu.Lock()
	s.counts[key]++
	attempt := s.counts[key]
	fail := b.status == 0 && (attempt <= b.failFirst || (b.failRate > 0 && s.rand() < b.failRate))
	s.mu.Unlock()

	status := http.StatusOK
	switch {
	case b.hang:
		status = 0
	case b.status != 0:
		status = b.status
	case fail:
		status = b.failStatus
	}

	rec := Received{
		At: time.Now().UTC(), Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
		Headers: flatten(r.Header), Body: string(body), Attempt: attempt, Status: status,
	}
	s.record(rec)

	if b.latency > 0 {
		select {
		case <-time.After(b.latency):
		case <-r.Context().Done():
			return
		}
	}
	if b.hang {
		<-r.Context().Done()
		return
	}

	if status >= 400 && b.retryAfter != "" {
		w.Header().Set("Retry-After", b.retryAfter)
	}
	resp := map[string]any{"received": rec}
	switch {
	case b.fakeError && status == http.StatusOK:
		resp["status"] = "FAILED"
		resp["error"] = "simulated failure reported in a 200 response"
	case status >= 400:
		resp["error"] = fmt.Sprintf("simulated %d from flakyvendor", status)
	default:
		resp["status"] = "OK"
	}
	writeJSON(w, status, resp)
}

func (s *Server) record(r Received) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.received) == maxRecorded {
		s.received = s.received[1:]
	}
	s.received = append(s.received, r)
}

func (s *Server) handleRecorded(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"data": s.Requests()})
	case http.MethodDelete:
		s.Reset()
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

type behavior struct {
	status     int
	failRate   float64
	failFirst  int
	failStatus int
	key        string
	retryAfter string
	fakeError  bool
	latency    time.Duration
	hang       bool
}

func parseBehavior(r *http.Request) (behavior, error) {
	q := r.URL.Query()
	b := behavior{failStatus: http.StatusServiceUnavailable, key: q.Get("key"), retryAfter: q.Get("retry_after")}
	var err error
	intParam := func(name string, dst *int, lo, hi int) {
		if v := q.Get(name); v != "" && err == nil {
			n, perr := strconv.Atoi(v)
			if perr != nil || n < lo || n > hi {
				err = fmt.Errorf("%s must be an integer between %d and %d", name, lo, hi)
				return
			}
			*dst = n
		}
	}
	intParam("status", &b.status, 100, 599)
	intParam("fail_first", &b.failFirst, 0, 1_000_000)
	intParam("fail_status", &b.failStatus, 100, 599)
	if v := q.Get("fail_rate"); v != "" && err == nil {
		b.failRate, err = strconv.ParseFloat(v, 64)
		if err != nil || b.failRate < 0 || b.failRate > 1 {
			err = fmt.Errorf("fail_rate must be a number between 0 and 1")
		}
	}
	if v := q.Get("latency"); v != "" && err == nil {
		b.latency, err = time.ParseDuration(v)
		if err != nil || b.latency < 0 {
			err = fmt.Errorf("latency must be a duration such as 200ms")
		}
	}
	b.fakeError = q.Get("fake_error") == "1"
	b.hang = q.Get("hang") == "1"
	return b, err
}

func flatten(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
