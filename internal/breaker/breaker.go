// Package breaker implements a circuit breaker for one upstream.
//
// Closed: requests flow; outcomes are counted in a sliding window. The breaker
// opens when the failure rate reaches FailureRate (once at least MinCalls were
// seen) or after ConsecutiveFailures failures in a row.
//
// Open: no requests are sent. After Cooldown it becomes half-open.
//
// Half-open: up to Probes requests are let through. When that many succeed
// the breaker closes; any failure opens it again.
package breaker

import (
	"fmt"
	"sync"
	"time"
)

// State is the breaker's state.
type State string

const (
	Closed   State = "closed"
	Open     State = "open"
	HalfOpen State = "half_open"
)

// Config tunes a breaker. Zero values are replaced by Defaults.
type Config struct {
	FailureRate         float64
	MinCalls            int
	Window              time.Duration
	ConsecutiveFailures int
	Cooldown            time.Duration
	Probes              int
}

// Defaults are used for unset fields.
var Defaults = Config{
	FailureRate:         0.5,
	MinCalls:            20,
	Window:              time.Minute,
	ConsecutiveFailures: 5,
	Cooldown:            30 * time.Second,
	Probes:              3,
}

// WithDefaults fills unset fields from Defaults.
func (c Config) WithDefaults() Config {
	if c.FailureRate == 0 {
		c.FailureRate = Defaults.FailureRate
	}
	if c.MinCalls == 0 {
		c.MinCalls = Defaults.MinCalls
	}
	if c.Window == 0 {
		c.Window = Defaults.Window
	}
	if c.ConsecutiveFailures == 0 {
		c.ConsecutiveFailures = Defaults.ConsecutiveFailures
	}
	if c.Cooldown == 0 {
		c.Cooldown = Defaults.Cooldown
	}
	if c.Probes == 0 {
		c.Probes = Defaults.Probes
	}
	return c
}

// Transition describes a state change, for logging and history.
type Transition struct {
	From, To State
	At       time.Time
	Reason   string
	// OpenSince is when the breaker opened, set when it closes.
	OpenSince time.Time
}

const buckets = 10

type bucket struct {
	start     time.Time
	ok, fails int
}

// Breaker is a circuit breaker. It is safe for concurrent use.
type Breaker struct {
	mu  sync.Mutex
	cfg Config

	state       State
	since       time.Time // when the current state began
	openedAt    time.Time // when the breaker last opened
	window      [buckets]bucket
	consecutive int
	probesOut   int // half-open probes in flight
	probesOK    int
}

// New returns a closed breaker.
func New(cfg Config, now time.Time) *Breaker {
	return &Breaker{cfg: cfg.WithDefaults(), state: Closed, since: now}
}

// State returns the current state, moving from open to half-open when the
// cooldown has passed. It also returns when that state began.
func (b *Breaker) State(now time.Time) (State, time.Time, *Transition) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.advance(now)
	return b.state, b.since, t
}

func (b *Breaker) advance(now time.Time) *Transition {
	if b.state == Open && !now.Before(b.since.Add(b.cfg.Cooldown)) {
		return b.set(HalfOpen, now, "cooldown elapsed; sending probe requests")
	}
	return nil
}

func (b *Breaker) set(s State, now time.Time, reason string) *Transition {
	t := &Transition{From: b.state, To: s, At: now, Reason: reason}
	if s == Closed {
		t.OpenSince = b.openedAt
	}
	if s == Open {
		b.openedAt = now
	}
	b.state, b.since = s, now
	b.probesOut, b.probesOK = 0, 0
	if s != HalfOpen {
		b.window = [buckets]bucket{}
		b.consecutive = 0
	}
	return t
}

// Allowance is how many new requests may be sent now: 0 while open, the free
// probe slots while half-open, and -1 (unlimited) while closed.
func (b *Breaker) Allowance(now time.Time) (int, *Transition) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.advance(now)
	switch b.state {
	case Open:
		return 0, t
	case HalfOpen:
		return max(0, b.cfg.Probes-b.probesOut-b.probesOK), t
	default:
		return -1, t
	}
}

// Started records that a request was sent (needed to count half-open probes).
func (b *Breaker) Started(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.advance(now)
	if b.state == HalfOpen {
		b.probesOut++
	}
}

// Record records an attempt's outcome. Only outcomes that say something about
// the upstream's health should be recorded: success, or a retryable failure.
// It returns a transition if the state changed.
func (b *Breaker) Record(success bool, now time.Time) *Transition {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.advance(now)

	switch b.state {
	case HalfOpen:
		if b.probesOut > 0 {
			b.probesOut--
		}
		if !success {
			return b.set(Open, now, "a probe request failed")
		}
		b.probesOK++
		if b.probesOK >= b.cfg.Probes {
			return b.set(Closed, now, fmt.Sprintf("%d probe requests succeeded", b.cfg.Probes))
		}
		return nil
	case Open:
		// A request that started before the breaker opened; ignore it.
		return nil
	}

	bk := b.current(now)
	if success {
		bk.ok++
		b.consecutive = 0
		return nil
	}
	bk.fails++
	b.consecutive++
	if b.consecutive >= b.cfg.ConsecutiveFailures {
		return b.set(Open, now, fmt.Sprintf("%d consecutive failures", b.consecutive))
	}
	ok, fails := b.totals(now)
	if calls := ok + fails; calls >= b.cfg.MinCalls {
		if rate := float64(fails) / float64(calls); rate >= b.cfg.FailureRate {
			return b.set(Open, now, fmt.Sprintf("%.0f%% of the last %d calls failed", rate*100, calls))
		}
	}
	return nil
}

// CooldownEnds returns when an open breaker becomes half-open, or the zero
// time if it isn't open.
func (b *Breaker) CooldownEnds() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != Open {
		return time.Time{}
	}
	return b.since.Add(b.cfg.Cooldown)
}

// Trip opens the breaker immediately, for example from an operator.
func (b *Breaker) Trip(now time.Time, reason string) *Transition {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == Open {
		return nil
	}
	return b.set(Open, now, reason)
}

func (b *Breaker) current(now time.Time) *bucket {
	width := b.cfg.Window / buckets
	start := now.Truncate(width)
	i := int(start.UnixNano()/int64(width)) % buckets
	if !b.window[i].start.Equal(start) {
		b.window[i] = bucket{start: start}
	}
	return &b.window[i]
}

func (b *Breaker) totals(now time.Time) (ok, fails int) {
	cutoff := now.Add(-b.cfg.Window)
	for _, bk := range b.window {
		if bk.start.After(cutoff) {
			ok += bk.ok
			fails += bk.fails
		}
	}
	return ok, fails
}
