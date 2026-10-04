package worker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tanvir001728/hookyard/internal/config"
	"github.com/tanvir001728/hookyard/internal/flakyvendor"
	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
	"github.com/tanvir001728/hookyard/internal/store/storetest"
)

// fastRetry keeps tests quick: retries 50ms apart.
var fastRetry = model.RetryPolicy{MaxAttempts: 5, InitialInterval: 50 * time.Millisecond, MaxInterval: time.Second, Multiplier: 2, MaxAge: time.Hour}

type harness struct {
	t      *testing.T
	store  *store.Store
	dbURL  string
	vendor *flakyvendor.Server
	reg    *config.Registry
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, dbURL := storetest.NewWithURL(t)
	vendor := flakyvendor.New()
	srv := httptest.NewServer(vendor)
	t.Cleanup(srv.Close)

	base, _ := url.Parse(srv.URL)
	reg := config.NewRegistry([]config.Upstream{{
		Name:    "flaky",
		BaseURL: base,
		Timeout: 2 * time.Second,
		Retry:   fastRetry,
		Headers: map[string]string{"Authorization": "Bearer vendor-secret"},
	}})
	return &harness{t: t, store: st, dbURL: dbURL, vendor: vendor, reg: reg}
}

// start runs an engine until the test ends.
func (h *harness) start(cfg Config) *Engine {
	h.t.Helper()
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 20 * time.Millisecond
	}
	e := New(slog.New(slog.NewTextHandler(io.Discard, nil)), h.store, h.reg, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Run(ctx)
	}()
	h.t.Cleanup(func() {
		cancel()
		<-done
	})
	return e
}

func (h *harness) enqueue(path string, mod ...func(*store.NewRequest)) model.Request {
	h.t.Helper()
	in := store.NewRequest{
		Upstream: "flaky", Method: http.MethodPost, Path: path,
		Body:    json.RawMessage(`{"order_id":42}`),
		Retry:   fastRetry,
		Timeout: 2 * time.Second,
	}
	for _, m := range mod {
		m(&in)
	}
	req, _, err := h.store.CreateRequest(context.Background(), in)
	if err != nil {
		h.t.Fatal(err)
	}
	return req
}

// waitStatus polls until the request reaches want, failing after a timeout.
func (h *harness) waitStatus(id string, want model.Status) model.Request {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		req, err := h.store.GetRequest(context.Background(), id)
		if err != nil {
			h.t.Fatal(err)
		}
		if req.Status == want {
			return req
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("request %s: status %s after 10s, want %s (last error: %+v)", id, req.Status, want, req.LastError)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) attempts(id string) []model.Attempt {
	h.t.Helper()
	a, err := h.store.ListAttempts(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

func TestDeliverSuccess(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.start(Config{})

	req := h.enqueue("/orders?x=1", func(in *store.NewRequest) {
		in.Headers = map[string]string{"X-Correlation-Id": "abc", "Authorization": "Bearer app-must-not-win"}
	})
	done := h.waitStatus(req.ID, model.StatusSucceeded)
	if done.AttemptCount != 1 || done.CompletedAt == nil || done.LastError != nil {
		t.Errorf("succeeded request: %+v", done)
	}

	got := h.vendor.Requests()
	if len(got) != 1 {
		t.Fatalf("vendor received %d requests", len(got))
	}
	r := got[0]
	if r.Method != http.MethodPost || r.Path != "/orders" || r.Query != "x=1" || r.Body != `{"order_id":42}` {
		t.Errorf("vendor saw %+v", r)
	}
	checks := map[string]string{
		"Content-Type":        "application/json",
		"X-Correlation-Id":    "abc",
		"Authorization":       "Bearer vendor-secret", // upstream headers win
		"Hookyard-Request-Id": req.ID,
		"Hookyard-Attempt":    "1",
	}
	for k, v := range checks {
		if r.Headers[k] != v {
			t.Errorf("header %s = %q, want %q", k, r.Headers[k], v)
		}
	}

	attempts := h.attempts(req.ID)
	if len(attempts) != 1 || attempts[0].Outcome != model.OutcomeSuccess || *attempts[0].StatusCode != 200 ||
		attempts[0].Response == nil || attempts[0].Response.Headers["Content-Type"] != "application/json" {
		t.Errorf("attempts = %+v", attempts)
	}
}

func TestRetryUntilSuccess(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.start(Config{})

	req := h.enqueue("/orders?fail_first=2")
	done := h.waitStatus(req.ID, model.StatusSucceeded)
	if done.AttemptCount != 3 {
		t.Errorf("attempt_count = %d, want 3", done.AttemptCount)
	}
	attempts := h.attempts(req.ID)
	if len(attempts) != 3 {
		t.Fatalf("got %d attempts", len(attempts))
	}
	for i, a := range attempts[:2] {
		if a.Outcome != model.OutcomeRetryableFailure || *a.StatusCode != 503 || a.Error.Code != ErrCodeHTTPStatus || a.RetryAt == nil {
			t.Errorf("attempt %d: %+v", i+1, a)
		}
	}
	if attempts[2].Outcome != model.OutcomeSuccess {
		t.Errorf("last attempt: %+v", attempts[2])
	}
	for i, r := range h.vendor.Requests() {
		if want := strconv.Itoa(i + 1); r.Headers["Hookyard-Attempt"] != want {
			t.Errorf("vendor request %s has Hookyard-Attempt %q", want, r.Headers["Hookyard-Attempt"])
		}
	}
}

func TestDeadAfterMaxAttempts(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.start(Config{})

	req := h.enqueue("/orders?status=500", func(in *store.NewRequest) { in.Retry.MaxAttempts = 2 })
	dead := h.waitStatus(req.ID, model.StatusDead)
	if dead.AttemptCount != 2 || dead.LastError == nil || dead.LastError.Code != ErrCodeHTTPStatus ||
		*dead.LastStatusCode != 500 || dead.CompletedAt == nil || dead.NextAttemptAt != nil {
		t.Errorf("dead request: %+v", dead)
	}
	if n := len(h.vendor.Requests()); n != 2 {
		t.Errorf("vendor received %d requests, want 2", n)
	}
}

func TestPermanentFailureIsNotRetried(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.start(Config{})

	req := h.enqueue("/orders?status=422")
	dead := h.waitStatus(req.ID, model.StatusDead)
	attempts := h.attempts(req.ID)
	if dead.AttemptCount != 1 || len(attempts) != 1 || attempts[0].Outcome != model.OutcomePermanentFailure {
		t.Errorf("a 422 must go to the DLQ after one attempt: %+v %+v", dead, attempts)
	}
}

func TestRetryAfterIsHonored(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.start(Config{})

	req := h.enqueue("/orders?fail_first=1&retry_after=1")
	h.waitStatus(req.ID, model.StatusSucceeded)
	got := h.vendor.Requests()
	if len(got) != 2 {
		t.Fatalf("vendor received %d requests", len(got))
	}
	// Without Retry-After the fast policy would retry within 50ms.
	if gap := got[1].At.Sub(got[0].At); gap < 950*time.Millisecond {
		t.Errorf("retried after %s, want at least the 1s Retry-After", gap)
	}
}

func TestMaxAgeExceeded(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.start(Config{})

	req := h.enqueue("/orders?status=503", func(in *store.NewRequest) {
		in.Retry = model.RetryPolicy{MaxAttempts: 100, InitialInterval: 20 * time.Millisecond, MaxInterval: 50 * time.Millisecond, Multiplier: 2, MaxAge: 300 * time.Millisecond}
	})
	dead := h.waitStatus(req.ID, model.StatusDead)
	if dead.LastError == nil || dead.LastError.Code != ErrCodeMaxAgeExceeded || dead.AttemptCount >= 100 {
		t.Errorf("want dead by max_age before max_attempts: %+v (last error %+v)", dead.AttemptCount, dead.LastError)
	}
	// The attempt itself keeps its real error.
	attempts := h.attempts(req.ID)
	if last := attempts[len(attempts)-1]; last.Error == nil || last.Error.Code != ErrCodeHTTPStatus {
		t.Errorf("last attempt error = %+v", last.Error)
	}
}

func TestRetriesRunOnTime(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// A long poll interval: retries must still run on time.
	e := h.start(Config{PollInterval: 10 * time.Second})
	// Let the engine go idle first, then enqueue and wake it as the API does.
	// Without the wakeup the first delivery would wait for the poll interval.
	time.Sleep(200 * time.Millisecond)

	req := h.enqueue("/orders?fail_first=2")
	e.Notify()
	start := time.Now()
	h.waitStatus(req.ID, model.StatusSucceeded)
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("3 attempts with ~50ms backoff took %s; the engine should wake when retries are due", took)
	}
}

func TestStringBodySentVerbatim(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.start(Config{})

	req := h.enqueue("/xml", func(in *store.NewRequest) {
		in.Body = json.RawMessage(`"<order id=\"1\"/>"`)
		in.Headers = map[string]string{"Content-Type": "application/xml"}
	})
	h.waitStatus(req.ID, model.StatusSucceeded)
	r := h.vendor.Requests()[0]
	if r.Body != `<order id="1"/>` || r.Headers["Content-Type"] != "application/xml" {
		t.Errorf("vendor saw body %q with Content-Type %q", r.Body, r.Headers["Content-Type"])
	}
}

func TestTimeout(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.start(Config{})

	req := h.enqueue("/slow?hang=1", func(in *store.NewRequest) {
		in.Timeout = 100 * time.Millisecond
		in.Retry.MaxAttempts = 1
	})
	dead := h.waitStatus(req.ID, model.StatusDead)
	if dead.LastError == nil || dead.LastError.Code != ErrCodeTimeout || dead.LastStatusCode != nil {
		t.Errorf("timed-out request: %+v", dead)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.start(Config{})

	req := h.enqueue("/moved?status=302", func(in *store.NewRequest) { in.Retry.MaxAttempts = 1 })
	dead := h.waitStatus(req.ID, model.StatusDead)
	if *dead.LastStatusCode != 302 || len(h.vendor.Requests()) != 1 {
		t.Errorf("redirect: status %v, vendor requests %d", *dead.LastStatusCode, len(h.vendor.Requests()))
	}
}

func TestUnknownUpstreamIsRetried(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.start(Config{})

	req := h.enqueue("/x", func(in *store.NewRequest) {
		in.Upstream = "removed"
		in.Retry.MaxAttempts = 2
	})
	dead := h.waitStatus(req.ID, model.StatusDead)
	if dead.AttemptCount != 2 || dead.LastError.Code != ErrCodeInternal {
		t.Errorf("request for removed upstream: %+v", dead)
	}
}

func TestCrashRecovery(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// A worker claims the request and dies before recording anything.
	req := h.enqueue("/orders")
	if claims, err := h.store.ClaimDue(context.Background(), 1, time.Minute); err != nil || len(claims) != 1 {
		t.Fatalf("claim: %v %v", claims, err)
	}
	expireLeases(t, h.dbURL)

	h.start(Config{RecoveryInterval: 50 * time.Millisecond})
	done := h.waitStatus(req.ID, model.StatusSucceeded)
	if done.AttemptCount != 1 {
		t.Errorf("attempt_count = %d, want 1 (the crashed attempt was never recorded)", done.AttemptCount)
	}
}

func TestMultipleEnginesDeliverEachAttemptOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.start(Config{Workers: 4})
	h.start(Config{Workers: 4})

	const n = 30
	ids := make([]string, n)
	for i := range n {
		ids[i] = h.enqueue("/orders?latency=5ms").ID
	}
	for _, id := range ids {
		h.waitStatus(id, model.StatusSucceeded)
	}

	seen := map[string]int{}
	for _, r := range h.vendor.Requests() {
		seen[r.Headers["Hookyard-Request-Id"]]++
	}
	if len(seen) != n {
		t.Errorf("vendor saw %d distinct requests, want %d", len(seen), n)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("request %s delivered %d times", id, count)
		}
	}
}

func TestGracefulDrain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	e := New(slog.New(slog.NewTextHandler(io.Discard, nil)), h.store, h.reg, Config{PollInterval: 10 * time.Millisecond})

	req := h.enqueue("/orders?latency=300ms")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Run(ctx)
	}()

	// Stop the engine while the delivery is in flight.
	for len(h.vendor.Requests()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	got, err := h.store.GetRequest(context.Background(), req.ID)
	if err != nil || got.Status != model.StatusSucceeded {
		t.Fatalf("in-flight delivery should finish during drain: %+v %v", got.Status, err)
	}
}

func TestNotifyWakesEngine(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	e := h.start(Config{PollInterval: time.Hour})

	// Let the engine settle into waiting, then enqueue and notify.
	time.Sleep(50 * time.Millisecond)
	req := h.enqueue("/orders")
	e.Notify()
	h.waitStatus(req.ID, model.StatusSucceeded)
}

// expireLeases makes every current lease look expired, simulating a crashed worker.
func expireLeases(t *testing.T, dbURL string) {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	if _, err := conn.Exec(t.Context(), `UPDATE requests SET lease_expires_at = now() - interval '1 second' WHERE status = 'in_flight'`); err != nil {
		t.Fatal(err)
	}
}
