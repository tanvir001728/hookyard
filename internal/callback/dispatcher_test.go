package callback_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/callback"
	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
	"github.com/tanvir001728/hookyard/internal/store/storetest"
)

var secret = callback.NewTestSecret()

// receiver is an app endpoint that fails the first `fail` calls.
type receiver struct {
	mu    sync.Mutex
	fail  int
	calls []call
}

type call struct {
	header http.Header
	body   []byte
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.calls = append(rc.calls, call{r.Header.Clone(), body})
	if len(rc.calls) <= rc.fail {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (rc *receiver) snapshot() []call {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]call(nil), rc.calls...)
}

// finished creates a request with a callback URL and finishes it with one
// successful attempt, as the delivery engine would.
func finished(t *testing.T, s *store.Store, url string) model.Request {
	t.Helper()
	ctx := t.Context()
	req, _, err := s.CreateRequest(ctx, store.NewRequest{
		Upstream: "courier-x", Method: "POST", Path: "/shipments", Body: json.RawMessage(`{"order":1}`),
		Retry:   model.RetryPolicy{MaxAttempts: 3, InitialInterval: time.Second, MaxInterval: time.Second, Multiplier: 2, MaxAge: time.Hour},
		Timeout: 5 * time.Second, Tags: map[string]string{"app": "orders"},
		CallbackURL: url, OnResult: "order.shipment",
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.ClaimDue(ctx, store.ClaimOptions{Limit: 10, LeaseMargin: time.Minute})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim: %v %v", claims, err)
	}
	code := 201
	err = s.RecordAttempt(ctx, store.AttemptRecord{
		Attempt: model.Attempt{
			RequestID: req.ID, Number: 1, StartedAt: time.Now(), Outcome: model.OutcomeSuccess, StatusCode: &code,
			Response: &model.AttemptResponse{Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"id":"shp_1"}`},
		},
		Status: model.StatusSucceeded, LeaseExpiresAt: claims[0].LeaseExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func run(t *testing.T, s *store.Store, cfg callback.Config) *callback.Dispatcher {
	t.Helper()
	secrets, err := callback.ParseSecrets(secret)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Secrets = secrets
	cfg.PollInterval = 20 * time.Millisecond
	d := callback.NewDispatcher(slog.New(slog.NewTextHandler(io.Discard, nil)), s, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = d.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return d
}

func waitFor(t *testing.T, s *store.Store, requestID, status string) store.Callback {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cbs, err := s.ListCallbacks(t.Context(), requestID)
		if err != nil {
			t.Fatal(err)
		}
		if len(cbs) == 1 && cbs[0].Status == status {
			return cbs[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	cbs, _ := s.ListCallbacks(t.Context(), requestID)
	t.Fatalf("callback never became %s: %+v", status, cbs)
	return store.Callback{}
}

func TestDispatcherDeliversSignedCallbackUntilAccepted(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	rc := &receiver{fail: 2}
	app := httptest.NewServer(rc)
	defer app.Close()

	req := finished(t, s, app.URL+"/hooks")
	run(t, s, callback.Config{Backoff: func(int) time.Duration { return 10 * time.Millisecond }})

	cb := waitFor(t, s, req.ID, store.CallbackDelivered)
	if cb.AttemptCount != 3 || cb.LastStatusCode == nil || *cb.LastStatusCode != 204 {
		t.Errorf("callback = %+v", cb)
	}

	// Give a duplicate delivery a chance to show up.
	time.Sleep(100 * time.Millisecond)
	calls := rc.snapshot()
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3 (two failures, then delivered once)", len(calls))
	}

	secrets, _ := callback.ParseSecrets(secret)
	for i, c := range calls {
		h := c.header
		if h.Get("webhook-id") != cb.ID {
			t.Errorf("call %d: webhook-id = %q, want the same id on every attempt", i, h.Get("webhook-id"))
		}
		if err := callback.Verify(secrets, h.Get("webhook-id"), h.Get("webhook-timestamp"), h.Get("webhook-signature"), c.body, time.Now(), 5*time.Minute); err != nil {
			t.Errorf("call %d: signature: %v", i, err)
		}
		if string(c.body) != string(calls[0].body) {
			t.Errorf("call %d: body changed between attempts", i)
		}
	}

	var e callback.Event
	if err := json.Unmarshal(calls[0].body, &e); err != nil {
		t.Fatal(err)
	}
	d := e.Data
	if e.Type != "request.succeeded" || d.RequestID != req.ID || d.Status != model.StatusSucceeded || d.AttemptCount != 1 ||
		d.OnResult == nil || *d.OnResult != "order.shipment" || d.Tags["app"] != "orders" ||
		d.Response == nil || d.Response.StatusCode != http.StatusCreated || d.Response.Body != `{"id":"shp_1"}` || d.CompletedAt == nil {
		t.Errorf("event = %s", calls[0].body)
	}
}

func TestDispatcherGivesUpAfterMaxAttempts(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	rc := &receiver{fail: 100}
	app := httptest.NewServer(rc)
	defer app.Close()

	req := finished(t, s, app.URL)
	run(t, s, callback.Config{MaxAttempts: 2, Backoff: func(int) time.Duration { return 10 * time.Millisecond }})

	cb := waitFor(t, s, req.ID, store.CallbackFailed)
	if cb.AttemptCount != 2 || cb.LastError != "the callback endpoint responded 503 (expected 2xx)" {
		t.Errorf("callback = %+v", cb)
	}
}

func TestDispatcherTimesOutAndDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	hang := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(hang) // before Close, which waits for the handler
	redirect := httptest.NewServer(http.RedirectHandler(slow.URL, http.StatusTemporaryRedirect))
	defer redirect.Close()

	timedOut := finished(t, s, slow.URL)
	redirected := finished(t, s, redirect.URL)
	run(t, s, callback.Config{MaxAttempts: 1, Timeout: 100 * time.Millisecond})

	if cb := waitFor(t, s, timedOut.ID, store.CallbackFailed); cb.LastError != "no response within 100ms" {
		t.Errorf("timed out callback = %+v", cb)
	}
	if cb := waitFor(t, s, redirected.ID, store.CallbackFailed); cb.LastStatusCode == nil || *cb.LastStatusCode != 307 {
		t.Errorf("redirected callback = %+v", cb)
	}
}
