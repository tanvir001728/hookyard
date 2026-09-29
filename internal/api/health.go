package api

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"
)

// ReadinessCheck reports whether a dependency is usable. It should return
// promptly and honor ctx cancellation.
type ReadinessCheck func(ctx context.Context) error

const readinessTimeout = 2 * time.Second

type checkResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type readinessResponse struct {
	Status string        `json:"status"`
	Checks []checkResult `json:"checks"`
}

// handleHealthz reports liveness: the process is up and serving HTTP.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz reports readiness: every registered dependency check passes.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	results := make([]checkResult, 0, len(s.checks))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, check := range s.checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := checkResult{Name: name, Status: "ok"}
			if err := check(ctx); err != nil {
				// /readyz is unauthenticated: log the details, but don't expose
				// internals such as host names in the response.
				s.log.Warn("readiness check failed", "check", name, "error", err)
				res.Status, res.Error = "fail", "unavailable"
			}
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })

	resp := readinessResponse{Status: "ok", Checks: results}
	status := http.StatusOK
	for _, res := range results {
		if res.Status != "ok" {
			resp.Status, status = "fail", http.StatusServiceUnavailable
			break
		}
	}
	writeJSON(w, status, resp)
}
