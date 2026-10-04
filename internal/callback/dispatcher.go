package callback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
	"github.com/tanvir001728/hookyard/internal/version"
)

// Config tunes the dispatcher. Zero values use the defaults.
type Config struct {
	// Secrets sign every callback; the first is the newest.
	Secrets []Secret
	// Timeout bounds one callback attempt.
	Timeout time.Duration
	// MaxAttempts is how many times a callback is tried before it fails.
	MaxAttempts int
	// Concurrency is the maximum number of callbacks sent at once.
	Concurrency int
	// PollInterval is how often the queue is checked when idle.
	PollInterval time.Duration
	// RecoveryInterval is how often interrupted deliveries are recovered.
	RecoveryInterval time.Duration
	// Backoff returns the wait before attempt n+1 after n failed attempts.
	Backoff func(n int) time.Duration
}

// Defaults for Config.
const (
	DefaultTimeout     = 10 * time.Second
	DefaultMaxAttempts = 12
)

func (c Config) withDefaults() Config {
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = DefaultMaxAttempts
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 8
	}
	if c.PollInterval <= 0 {
		c.PollInterval = time.Second
	}
	if c.RecoveryInterval <= 0 {
		c.RecoveryInterval = 15 * time.Second
	}
	if c.Backoff == nil {
		c.Backoff = Backoff
	}
	return c
}

// Backoff is the default wait between callback attempts: 5s doubling up to
// an hour, with jitter. Twelve attempts span about six hours.
func Backoff(n int) time.Duration {
	d := 5 * time.Second << min(n-1, 10)
	d = min(d, time.Hour)
	return d/2 + rand.N(d/2+1)
}

// leaseMargin is added to the timeout to form a callback's lease.
const leaseMargin = 30 * time.Second

// Dispatcher delivers queued callbacks to applications.
type Dispatcher struct {
	cfg    Config
	log    *slog.Logger
	store  *store.Store
	client *http.Client
	wake   chan struct{}
	now    func() time.Time
}

// NewDispatcher returns a dispatcher. Call Run to start delivering.
func NewDispatcher(log *slog.Logger, st *store.Store, cfg Config) *Dispatcher {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &Dispatcher{
		cfg:   cfg.withDefaults(),
		log:   log,
		store: st,
		client: &http.Client{
			Transport: transport,
			// The allow-list is checked against the configured URL; following
			// a redirect could lead anywhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		wake: make(chan struct{}, 1),
		now:  time.Now,
	}
}

// Notify wakes the dispatcher to check for due callbacks. It never blocks.
func (d *Dispatcher) Notify() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Run delivers callbacks until ctx is canceled, then waits for the ones in
// flight (each is bounded by the timeout).
func (d *Dispatcher) Run(ctx context.Context) error {
	var (
		wg    sync.WaitGroup
		slots = make(chan struct{}, d.cfg.Concurrency)
		freed = make(chan struct{}, 1)
	)
	sendCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	defer stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		d.recoverLoop(ctx)
	}()

	for ctx.Err() == nil {
		claimed := 0
		if free := cap(slots) - len(slots); free > 0 {
			claims, err := d.store.ClaimCallbacks(ctx, free, d.cfg.Timeout+leaseMargin)
			if err != nil && !errors.Is(err, context.Canceled) {
				d.log.Error("claiming callbacks failed", "error", err)
			}
			claimed = len(claims)
			for _, c := range claims {
				slots <- struct{}{}
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() {
						<-slots
						select {
						case freed <- struct{}{}:
						default:
						}
					}()
					d.deliver(sendCtx, c)
				}()
			}
		}
		if claimed > 0 {
			continue
		}
		wait := d.cfg.PollInterval
		if next, err := d.store.NextCallbackDueAt(ctx); err == nil && next != nil {
			wait = max(min(wait, next.Sub(d.now())), time.Millisecond)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
		case <-d.wake:
		case <-timer.C:
		case <-freed:
		}
		timer.Stop()
	}
	wg.Wait()
	return nil
}

func (d *Dispatcher) recoverLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.RecoveryInterval)
	defer ticker.Stop()
	for {
		n, err := d.store.RecoverExpiredCallbackLeases(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			d.log.Error("recovering callback leases failed", "error", err)
		case n > 0:
			d.log.Warn("recovered interrupted callbacks", "count", n)
			d.Notify()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// deliver makes one attempt and records it.
func (d *Dispatcher) deliver(ctx context.Context, c store.CallbackClaim) {
	cb := c.Callback
	res := store.CallbackResult{ID: cb.ID, LeaseExpiresAt: c.LeaseExpiresAt, Payload: cb.Payload}
	if res.Payload == "" {
		payload, err := d.payload(ctx, cb)
		if err != nil {
			// The request is gone or the database is unavailable: try later.
			d.log.Error("building callback failed", "callback_id", cb.ID, "request_id", cb.RequestID, "error", err)
			d.schedule(&res, cb.AttemptCount, "internal: "+err.Error())
			d.record(ctx, res, cb)
			return
		}
		res.Payload = payload
	}

	status, err := d.send(ctx, cb, res.Payload)
	if status != 0 {
		res.StatusCode = &status
	}
	switch {
	case err != nil:
		d.schedule(&res, cb.AttemptCount, err.Error())
	case status < 200 || status > 299:
		d.schedule(&res, cb.AttemptCount, fmt.Sprintf("the callback endpoint responded %d (expected 2xx)", status))
	default:
		res.Delivered = true
	}
	d.record(ctx, res, cb)
}

// schedule sets up a retry, or failure after the last attempt.
func (d *Dispatcher) schedule(res *store.CallbackResult, attempts int, msg string) {
	res.Error = msg
	if attempts < d.cfg.MaxAttempts {
		next := d.now().Add(d.cfg.Backoff(attempts))
		res.NextAttemptAt = &next
	}
}

func (d *Dispatcher) record(ctx context.Context, res store.CallbackResult, cb store.Callback) {
	err := d.store.RecordCallbackAttempt(ctx, res)
	switch {
	case errors.Is(err, store.ErrLeaseLost):
		d.log.Warn("callback lease expired before its outcome was recorded", "callback_id", cb.ID)
	case err != nil:
		d.log.Error("recording callback attempt failed", "callback_id", cb.ID, "error", err)
	case res.Delivered:
		d.log.Info("callback delivered", "callback_id", cb.ID, "request_id", cb.RequestID, "type", cb.EventType, "attempt", cb.AttemptCount)
	case res.NextAttemptAt == nil:
		d.log.Warn("callback failed", "callback_id", cb.ID, "request_id", cb.RequestID, "attempts", cb.AttemptCount, "error", res.Error)
	default:
		d.log.Info("callback attempt failed", "callback_id", cb.ID, "request_id", cb.RequestID, "attempt", cb.AttemptCount, "retry_at", res.NextAttemptAt, "error", res.Error)
	}
}

// payload builds the event body from the request as it was when it finished.
func (d *Dispatcher) payload(ctx context.Context, cb store.Callback) (string, error) {
	req, err := d.store.GetRequest(ctx, cb.RequestID)
	if err != nil {
		return "", err
	}
	req.Status, req.AttemptCount = cb.RequestStatus, cb.RequestAttempts
	var last *model.Attempt
	if cb.RequestAttempts > 0 {
		a, err := d.store.GetAttempt(ctx, cb.RequestID, cb.RequestAttempts)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		if err == nil {
			last = &a
		}
	}
	body, err := Marshal(Build(req, last, cb.CreatedAt))
	return string(body), err
}

// send posts the signed event and returns the response status.
func (d *Dispatcher) send(ctx context.Context, cb store.Callback, payload string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cb.URL, bytes.NewBufferString(payload))
	if err != nil {
		return 0, err
	}
	now := d.now()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Hookyard/"+version.Version)
	req.Header.Set("webhook-id", cb.ID)
	req.Header.Set("webhook-timestamp", strconv.FormatInt(now.Unix(), 10))
	req.Header.Set("webhook-signature", Sign(d.cfg.Secrets, cb.ID, now, []byte(payload)))
	resp, err := d.client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return 0, fmt.Errorf("no response within %s", model.FormatDuration(d.cfg.Timeout))
		}
		// The URL is shown next to the error; keep just the cause.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			return 0, ue.Err
		}
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
