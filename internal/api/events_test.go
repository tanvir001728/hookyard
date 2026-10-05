package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/config"
	"github.com/tanvir001728/hookyard/internal/events"
	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store/storetest"
)

type sseEvent struct {
	id, typ string
	data    map[string]any
}

// streamResponse is the status and headers of an event stream response.
type streamResponse struct {
	StatusCode int
	Header     http.Header
}

// stream connects to /v1/events and returns a channel of parsed events. The
// body is closed when ctx ends or the server ends the stream.
func stream(t *testing.T, ctx context.Context, url string) (<-chan sseEvent, streamResponse) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the reader goroutine
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan sseEvent, 64)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		var cur sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur.typ != "" {
					out <- cur
				}
				cur = sseEvent{}
			case strings.HasPrefix(line, "id: "):
				cur.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				cur.typ = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &cur.data)
			}
		}
	}()
	return out, streamResponse{resp.StatusCode, resp.Header}
}

func next(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("stream ended")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5s")
	}
	return sseEvent{}
}

func newEventsServer(t *testing.T) (*httptest.Server, *events.Hub) {
	t.Helper()
	file, err := config.ParseFile([]byte(testConfig), nil)
	if err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub(16, 4)
	srv := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithV1(V1{
		Store: storetest.New(t), Config: file, Tokens: []config.APIToken{{Name: "orders", Secret: testToken}}, MaxBody: 4096, Events: hub,
	}))
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		hub.Close()
		ts.Close()
	})
	return ts, hub
}

func TestEventsStreamFiltersAndReconnects(t *testing.T) {
	t.Parallel()
	ts, hub := newEventsServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	ch, resp := stream(t, ctx, ts.URL+"/v1/events?upstream=courier-x&status=dead,pending")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, content type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	waitSubscribers(t, hub, 1)

	enqueue := func(upstream string) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/v1/requests",
			strings.NewReader(`{"upstream":"`+upstream+`","method":"POST","path":"/x"}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		res, err := http.DefaultClient.Do(req)
		if err != nil || res.StatusCode != http.StatusAccepted {
			t.Fatalf("enqueue: %v %v", res, err)
		}
		res.Body.Close()
	}
	// Filtered out: another upstream, and a status that isn't asked for.
	enqueue("payments-y")
	code := 500
	hub.Publish(events.Attempt(model.Request{ID: "req_1", Upstream: "courier-x"}, model.Attempt{Number: 1, Outcome: model.OutcomeRetryableFailure, StatusCode: &code}, model.StatusFailed, time.Now()))
	// Delivered: an enqueue (pending) and a dead-making attempt on courier-x.
	enqueue("courier-x")
	hub.Publish(events.Attempt(model.Request{ID: "req_2", Upstream: "courier-x", Method: "POST", Path: "/x"},
		model.Attempt{Number: 3, Outcome: model.OutcomePermanentFailure, StatusCode: &code, Duration: 42 * time.Millisecond}, model.StatusDead, time.Now()))

	e := next(t, ch)
	data := e.data["data"].(map[string]any)
	if e.typ != "request" || data["action"] != "enqueued" || data["upstream"] != "courier-x" || data["actor"] != "orders" || data["status"] != "pending" {
		t.Errorf("first event = %+v", e)
	}
	e = next(t, ch)
	data = e.data["data"].(map[string]any)
	if e.typ != "attempt" || data["request_id"] != "req_2" || data["status"] != "dead" || data["duration_ms"] != float64(42) || data["status_code"] != float64(500) {
		t.Errorf("second event = %+v", e)
	}
	lastID := e.id

	// Reconnecting gets new events, with later IDs; nothing is replayed.
	cancel()
	waitSubscribers(t, hub, 0)
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	ch2, _ := stream(t, ctx2, ts.URL+"/v1/events?type=upstream")
	waitSubscribers(t, hub, 1)
	hub.Publish(events.Upstream("courier-x", "breaker_open", "5 consecutive failures", "", time.Now()))
	e = next(t, ch2)
	newID, _ := strconv.ParseUint(e.id, 10, 64)
	oldID, _ := strconv.ParseUint(lastID, 10, 64)
	if e.typ != "upstream" || newID <= oldID || e.data["data"].(map[string]any)["kind"] != "breaker_open" {
		t.Errorf("after reconnect: %+v (last id %s)", e, lastID)
	}
}

func TestEventsDropsSlowClients(t *testing.T) {
	t.Parallel()
	ts, hub := newEventsServer(t)
	ch, _ := stream(t, t.Context(), ts.URL+"/v1/events")
	waitSubscribers(t, hub, 1)
	// Far more than the 16-event buffer, faster than the stream drains it.
	for range 10_000 {
		hub.Publish(events.Upstream("courier-x", "paused", strings.Repeat("x", 512), "ops", time.Now()))
	}
	sawDropped := false
	for e := range ch {
		if e.typ == "dropped" {
			sawDropped = true
		}
	}
	if !sawDropped {
		t.Error("a client that falls behind must be told it was dropped")
	}
}

func TestEventsErrors(t *testing.T) {
	t.Parallel()
	ts, hub := newEventsServer(t)
	for _, q := range []string{"status=lost", "type=nope"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/v1/events?"+q, nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		res, err := http.DefaultClient.Do(req)
		if err != nil || res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %v %v", q, res.StatusCode, err)
		}
		res.Body.Close()
	}
	anon, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/v1/events", nil)
	res, err := http.DefaultClient.Do(anon)
	if err != nil || res.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a token: %v %v", res.StatusCode, err)
	}
	res.Body.Close()

	// The hub allows 4 streams in this test.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	for range 4 {
		stream(t, ctx, ts.URL+"/v1/events")
	}
	waitSubscribers(t, hub, 4)
	_, resp := stream(t, ctx, ts.URL+"/v1/events")
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Errorf("over the limit: %d", resp.StatusCode)
	}
}

func waitSubscribers(t *testing.T, hub *events.Hub, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if hub.Subscribers() == n {
			return
		}
	}
	t.Fatalf("subscribers = %d, want %d", hub.Subscribers(), n)
}
