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
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tanvir001728/hookyard/internal/breaker"
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
	return newHarnessWith(t, config.Upstream{
		Name:    "flaky",
		Timeout: 2 * time.Second,
		Retry:   fastRetry,
		Headers: map[string]string{"Authorization": "Bearer vendor-secret"},
	})
}

// newHarnessWith points every given upstream at one flakyvendor.
func newHarnessWith(t *testing.T, upstreams ...config.Upstream) *harness {
	t.Helper()
	st, dbURL := storetest.NewWithURL(t)
	vendor := flakyvendor.New()
	srv := httptest.NewServer(vendor)
	t.Cleanup(srv.Close)

	base, _ := url.Parse(srv.URL)
	for i := range upstreams {
		upstreams[i].BaseURL = base
	}
	return &harness{t: t, store: st, dbURL: dbURL, vendor: vendor, reg: config.NewRegistry(upstreams)}
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
	if claims, err := h.store.ClaimDue(context.Background(), store.ClaimOptions{Limit: 1, LeaseMargin: time.Minute}); err != nil || len(claims) != 1 {
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

// receivedAt returns when the vendor received each request whose path
// starts with prefix, in order.
func (h *harness) receivedAt(prefix string) []time.Time {
	var out []time.Time
	for _, r := range h.vendor.Requests() {
		if strings.HasPrefix(r.Path, prefix) {
			out = append(out, r.At)
		}
	}
	return out
}

func TestRateLimit(t *testing.T) {
	t.Parallel()
	h := newHarnessWith(t, config.Upstream{Name: "rated", Timeout: 2 * time.Second, Retry: fastRetry, Limits: config.Limits{RateLimit: 5, Burst: 1}})
	h.start(Config{})

	var ids []string
	for range 11 {
		ids = append(ids, h.enqueue("/rated", func(in *store.NewRequest) { in.Upstream = "rated" }).ID)
	}
	for _, id := range ids {
		h.waitStatus(id, model.StatusSucceeded)
	}
	at := h.receivedAt("/rated")
	// 11 requests at 5/s with no burst: 10 intervals of 200ms.
	if took := at[len(at)-1].Sub(at[0]); took < 1800*time.Millisecond || took > 3*time.Second {
		t.Errorf("11 requests at 5/s took %s, want about 2s", took)
	}
}

func TestConcurrencyCap(t *testing.T) {
	t.Parallel()
	h := newHarnessWith(t, config.Upstream{Name: "capped", Timeout: 5 * time.Second, Retry: fastRetry, Limits: config.Limits{MaxConcurrency: 2}})
	h.start(Config{Workers: 8})

	var ids []string
	for range 6 {
		ids = append(ids, h.enqueue("/capped?latency=300ms", func(in *store.NewRequest) { in.Upstream = "capped" }).ID)
	}
	for _, id := range ids {
		h.waitStatus(id, model.StatusSucceeded)
	}
	// Each delivery lasts 300ms: count how many started within 300ms of each other.
	at := h.receivedAt("/capped")
	for i := range at {
		overlapping := 0
		for j := range at {
			if d := at[j].Sub(at[i]); d >= 0 && d < 250*time.Millisecond {
				overlapping++
			}
		}
		if overlapping > 2 {
			t.Fatalf("%d deliveries ran at once, want at most 2 (starts: %v)", overlapping, at)
		}
	}
	if took := at[len(at)-1].Sub(at[0]); took < 500*time.Millisecond {
		t.Errorf("6 deliveries of 300ms, 2 at a time, started within %s; want at least ~600ms", took)
	}
}

func TestSlowUpstreamDoesNotDelayOthers(t *testing.T) {
	t.Parallel()
	h := newHarnessWith(t,
		config.Upstream{Name: "slow", Timeout: 5 * time.Second, Retry: fastRetry, Limits: config.Limits{MaxConcurrency: 1}},
		config.Upstream{Name: "fast", Timeout: 5 * time.Second, Retry: fastRetry},
	)
	h.start(Config{Workers: 4})

	for range 5 {
		h.enqueue("/slow?latency=1s", func(in *store.NewRequest) { in.Upstream = "slow" })
	}
	start := time.Now()
	var fast []string
	for range 5 {
		fast = append(fast, h.enqueue("/fast", func(in *store.NewRequest) { in.Upstream = "fast" }).ID)
	}
	for _, id := range fast {
		h.waitStatus(id, model.StatusSucceeded)
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Errorf("fast upstream took %s while a slow one had a backlog; it must not wait behind it", took)
	}
}

func TestTooManyRequestsPausesUpstream(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.start(Config{})

	limited := h.enqueue("/first?status=429&retry_after=1", func(in *store.NewRequest) { in.Retry.MaxAttempts = 1 })
	h.waitStatus(limited.ID, model.StatusDead)

	other := h.enqueue("/second")
	h.waitStatus(other.ID, model.StatusSucceeded)
	first, second := h.receivedAt("/first"), h.receivedAt("/second")
	if gap := second[0].Sub(first[0]); gap < 900*time.Millisecond {
		t.Errorf("a request to the same upstream went out %s after a 429 with Retry-After: 1", gap)
	}
}

func breakerUpstream(cooldown time.Duration) config.Upstream {
	return config.Upstream{
		Name:    "flaky",
		Timeout: 2 * time.Second,
		Retry:   fastRetry,
		Breaker: config.Breaker{Enabled: true, Config: breaker.Config{
			FailureRate: 0.99, MinCalls: 1000, Window: time.Minute,
			ConsecutiveFailures: 3, Cooldown: cooldown, Probes: 1,
		}},
	}
}

// tripBreaker sends three failing requests and waits until the breaker
// records that it opened. It returns when it opened.
func (h *harness) tripBreaker() time.Time {
	h.t.Helper()
	for range 3 {
		h.enqueue("/down?status=503", func(in *store.NewRequest) { in.Retry.MaxAttempts = 1 })
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		events, err := h.store.ListUpstreamEvents(context.Background(), "flaky", 1)
		if err != nil {
			h.t.Fatal(err)
		}
		if len(events) == 1 && events[0].Kind == "breaker_open" {
			return events[0].At
		}
		if time.Now().After(deadline) {
			h.t.Fatal("the breaker never opened")
		}
	}
}

func TestBreakerPausesAndRecovers(t *testing.T) {
	t.Parallel()
	h := newHarnessWith(t, breakerUpstream(600*time.Millisecond))
	h.start(Config{})
	trippedAt := h.tripBreaker()

	// Requests enqueued while it's open wait for the cooldown, then a single
	// probe goes out, succeeds and closes the breaker, and the rest follow.
	var ids []string
	for range 4 {
		ids = append(ids, h.enqueue("/up").ID)
	}
	for _, id := range ids {
		h.waitStatus(id, model.StatusSucceeded)
	}
	up := h.receivedAt("/up")
	if first := up[0].Sub(trippedAt); first < 550*time.Millisecond {
		t.Errorf("a request went out %s after the breaker opened; want the 600ms cooldown first", first)
	}

	events, err := h.store.ListUpstreamEvents(context.Background(), "flaky", 10)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for i := len(events) - 1; i >= 0; i-- {
		kinds = append(kinds, events[i].Kind)
	}
	if got := strings.Join(kinds, ","); got != "breaker_open,breaker_half_open,breaker_closed" {
		t.Errorf("breaker history = %s", got)
	}
}

func TestPausedTimeDoesNotCountAgainstMaxAge(t *testing.T) {
	t.Parallel()
	h := newHarnessWith(t, breakerUpstream(1500*time.Millisecond))
	h.start(Config{})

	h.tripBreaker()

	// A probe that will succeed, then a request with a 1s max_age that waits
	// out a 1.5s pause, fails once, and must still be retried.
	h.enqueue("/probe")
	short := h.enqueue("/short?fail_first=1&key=short", func(in *store.NewRequest) {
		in.Retry = model.RetryPolicy{MaxAttempts: 5, InitialInterval: 50 * time.Millisecond, MaxInterval: 100 * time.Millisecond, Multiplier: 2, MaxAge: time.Second}
	})
	done := h.waitStatus(short.ID, model.StatusSucceeded)
	if done.AttemptCount != 2 {
		t.Errorf("attempt_count = %d, want 2", done.AttemptCount)
	}
}
