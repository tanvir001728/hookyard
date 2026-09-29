// Package worker delivers queued requests to their upstreams.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/tanvir001728/hookyard/internal/config"
	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
)

// Config tunes the delivery engine. Zero values use the defaults.
type Config struct {
	// Workers is the maximum number of concurrent deliveries.
	Workers int
	// PollInterval is how often the queue is checked when idle. Enqueues in
	// the same process wake the engine immediately through Notify.
	PollInterval time.Duration
	// LeaseMargin is added to a request's timeout to form its lease. A worker
	// that holds a request longer than that is presumed dead.
	LeaseMargin time.Duration
	// RecoveryInterval is how often expired leases are recovered.
	RecoveryInterval time.Duration
	// DrainTimeout bounds how long Run waits for in-flight deliveries after
	// its context is canceled.
	DrainTimeout time.Duration
	// Observer, if set, is told about every recorded attempt (for metrics).
	Observer Observer
}

// Observer receives each recorded attempt: the upstream, when the attempt
// finished, how long it took, how it was classified and the request's
// resulting status.
type Observer func(upstream string, finishedAt time.Time, d time.Duration, outcome model.AttemptOutcome, status model.Status)

func (c Config) withDefaults() Config {
	if c.Workers <= 0 {
		c.Workers = 32
	}
	if c.PollInterval <= 0 {
		c.PollInterval = time.Second
	}
	if c.LeaseMargin <= 0 {
		c.LeaseMargin = 30 * time.Second
	}
	if c.RecoveryInterval <= 0 {
		c.RecoveryInterval = 15 * time.Second
	}
	if c.DrainTimeout <= 0 {
		c.DrainTimeout = 30 * time.Second
	}
	return c
}

// Engine claims due requests and delivers them with a bounded worker pool.
type Engine struct {
	cfg       Config
	log       *slog.Logger
	store     *store.Store
	upstreams *config.Registry
	client    *http.Client
	decide    Decider
	wake      chan struct{}
	now       func() time.Time
}

// New returns an engine. Call Run to start delivering.
func New(log *slog.Logger, st *store.Store, upstreams *config.Registry, cfg Config) *Engine {
	return &Engine{
		cfg:       cfg.withDefaults(),
		log:       log,
		store:     st,
		upstreams: upstreams,
		client:    newHTTPClient(),
		decide:    PolicyDecider(nil),
		wake:      make(chan struct{}, 1),
		now:       time.Now,
	}
}

// Notify wakes the engine to check for due requests, for example right after
// a request was enqueued. It never blocks.
func (e *Engine) Notify() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Run delivers requests until ctx is canceled, then waits up to DrainTimeout
// for in-flight deliveries to finish. Deliveries still running after that are
// abandoned; their leases expire and they are delivered again later.
func (e *Engine) Run(ctx context.Context) error {
	e.log.Info("delivery engine started", "workers", e.cfg.Workers)

	// Deliveries outlive ctx so they can finish during the drain period.
	deliveryCtx, stopDeliveries := context.WithCancel(context.WithoutCancel(ctx))
	defer stopDeliveries()

	var (
		wg    sync.WaitGroup
		slots = make(chan struct{}, e.cfg.Workers)
		freed = make(chan struct{}, 1)
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		e.recoverLoop(ctx)
	}()

	for ctx.Err() == nil {
		free := cap(slots) - len(slots)
		claimed := 0
		if free > 0 {
			claims, err := e.store.ClaimDue(ctx, free, e.cfg.LeaseMargin)
			if err != nil && !errors.Is(err, context.Canceled) {
				e.log.Error("claiming requests failed", "error", err)
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
					e.deliver(deliveryCtx, c)
				}()
			}
		}

		// A full batch suggests more work is due: look again right away.
		if free > 0 && claimed == free {
			continue
		}
		timer := time.NewTimer(e.idleWait(ctx))
		select {
		case <-ctx.Done():
		case <-e.wake:
		case <-timer.C:
		case <-freed:
		}
		timer.Stop()
	}

	e.log.Info("delivery engine stopping", "in_flight", len(slots), "drain_timeout", e.cfg.DrainTimeout)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		e.log.Info("delivery engine stopped")
	case <-time.After(e.cfg.DrainTimeout):
		stopDeliveries()
		<-done
		e.log.Warn("delivery engine stopped before all deliveries finished; they will be retried after their leases expire")
	}
	return nil
}

// idleWait returns how long to sleep before checking the queue again: until
// the next retry is due, but never longer than the poll interval.
func (e *Engine) idleWait(ctx context.Context) time.Duration {
	next, err := e.store.NextDueAt(ctx)
	if err != nil || next == nil {
		return e.cfg.PollInterval
	}
	return min(max(next.Sub(e.now()), time.Millisecond), e.cfg.PollInterval)
}

func (e *Engine) recoverLoop(ctx context.Context) {
	ticker := time.NewTicker(e.cfg.RecoveryInterval)
	defer ticker.Stop()
	for {
		n, err := e.store.RecoverExpiredLeases(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			e.log.Error("recovering expired leases failed", "error", err)
		case n > 0:
			e.log.Warn("recovered interrupted deliveries", "count", n)
			e.Notify()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
