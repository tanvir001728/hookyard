package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/tanvir001728/hookyard/internal/events"
	"github.com/tanvir001728/hookyard/internal/model"
)

// Live event stream timing.
const (
	eventsHeartbeat     = 15 * time.Second
	eventsWriteDeadline = 10 * time.Second
	// eventsRetryMS tells EventSource clients how soon to reconnect.
	eventsRetryMS = 2000
)

var eventTypes = []string{events.TypeAttempt, events.TypeRequest, events.TypeUpstream}

// handleEvents streams live events as Server-Sent Events. Events are not
// stored: a client sees what happens while it is connected, on this instance.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.v1.Events == nil {
		writeError(w, http.StatusNotFound, codeNotFound, "live events are not enabled on this server")
		return
	}
	f, problems := parseEventFilter(r)
	if len(problems) > 0 {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid query parameters: "+strings.Join(problems, "; "))
		return
	}
	sub := s.v1.Events.Subscribe(f)
	if sub == nil {
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusServiceUnavailable, codeInternal, "too many live event streams are open; try again later")
		return
	}
	defer sub.Close()

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	// Disables response buffering in nginx and similar proxies.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// write sends one chunk with a deadline, so a client that stops reading
	// can't hold this goroutine forever.
	write := func(chunk string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(eventsWriteDeadline))
		if _, err := fmt.Fprint(w, chunk); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write(fmt.Sprintf("retry: %d\n: connected\n\n", eventsRetryMS)) {
		return
	}

	heartbeat := time.NewTicker(eventsHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if !write(": ping\n\n") {
				return
			}
		case e, ok := <-sub.Events():
			if !ok {
				if sub.Dropped() {
					write("event: dropped\ndata: {\"reason\":\"the client fell too far behind; reconnect to continue\"}\n\n")
				}
				return
			}
			data, err := json.Marshal(eventJSON{Type: e.Type, At: e.At.UTC(), Data: e.Data})
			if err != nil {
				s.log.Error("encoding a live event failed", "error", err)
				continue
			}
			if !write(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, data)) {
				return
			}
		}
	}
}

type eventJSON struct {
	Type string    `json:"type"`
	At   time.Time `json:"at"`
	Data any       `json:"data"`
}

// parseEventFilter reads upstream, status and type, each comma separated.
func parseEventFilter(r *http.Request) (events.Filter, []string) {
	q := r.URL.Query()
	var f events.Filter
	var problems []string
	set := func(v string) map[string]bool {
		if v == "" {
			return nil
		}
		m := map[string]bool{}
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				m[part] = true
			}
		}
		return m
	}
	f.Upstreams = set(q.Get("upstream"))
	if f.Statuses = set(q.Get("status")); f.Statuses != nil {
		for st := range f.Statuses {
			if !model.Status(st).Valid() {
				problems = append(problems, fmt.Sprintf("status: unknown status %q", st))
			}
		}
	}
	if f.Types = set(q.Get("type")); f.Types != nil {
		for t := range f.Types {
			if !slices.Contains(eventTypes, t) {
				problems = append(problems, fmt.Sprintf("type: must be %s", strings.Join(eventTypes, ", ")))
			}
		}
	}
	return f, problems
}
