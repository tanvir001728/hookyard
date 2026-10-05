// Package events fans out live delivery events (attempts, request state
// changes, upstream transitions) to subscribers such as the dashboard's live
// tail. Events are not stored: a subscriber sees what happens while it is
// connected, on this Hookyard instance.
//
// Publishing never blocks. Each subscriber has a bounded buffer; one that
// falls behind is dropped instead of slowing deliveries down.
package events

import (
	"sync"
	"sync/atomic"
	"time"
)

// Event types.
const (
	// TypeAttempt is a delivery attempt and the request's resulting status.
	TypeAttempt = "attempt"
	// TypeRequest is a request state change made through the API: enqueued,
	// canceled, resolved or replayed.
	TypeRequest = "request"
	// TypeUpstream is a breaker transition, pause or resume.
	TypeUpstream = "upstream"
)

// Event is one live event. Data is encoded as JSON for subscribers.
type Event struct {
	ID       uint64
	Type     string
	At       time.Time
	Upstream string
	// Status is the request's status after the event; empty for upstream
	// events.
	Status string
	Data   any
}

// Filter selects events. Empty fields match everything.
type Filter struct {
	Types     map[string]bool
	Upstreams map[string]bool
	// Statuses match the request status after the event. Upstream events
	// have none, so they don't match a status filter.
	Statuses map[string]bool
}

// Match reports whether e passes the filter.
func (f Filter) Match(e Event) bool {
	if len(f.Types) > 0 && !f.Types[e.Type] {
		return false
	}
	if len(f.Upstreams) > 0 && !f.Upstreams[e.Upstream] {
		return false
	}
	if len(f.Statuses) > 0 && !f.Statuses[e.Status] {
		return false
	}
	return true
}

// DefaultBuffer is how many events a subscriber may fall behind by.
const DefaultBuffer = 256

// Hub fans events out to subscribers.
type Hub struct {
	buffer  int
	maxSubs int

	mu     sync.Mutex
	subs   map[*Subscription]struct{}
	closed bool
	nextID atomic.Uint64
}

// NewHub returns a hub whose subscribers buffer up to buffer events, with at
// most maxSubs subscribers at once (0 means DefaultBuffer and 100).
func NewHub(buffer, maxSubs int) *Hub {
	if buffer <= 0 {
		buffer = DefaultBuffer
	}
	if maxSubs <= 0 {
		maxSubs = 100
	}
	return &Hub{buffer: buffer, maxSubs: maxSubs, subs: map[*Subscription]struct{}{}}
}

// Subscription receives events matching its filter until it is closed.
type Subscription struct {
	hub     *Hub
	filter  Filter
	ch      chan Event
	once    sync.Once
	dropped atomic.Bool
}

// Events delivers events. It is closed when the subscription ends: after
// Close, when the hub shuts down, or when the subscriber fell behind
// (see Dropped).
func (s *Subscription) Events() <-chan Event { return s.ch }

// Dropped reports whether the subscription was ended because the subscriber
// didn't keep up.
func (s *Subscription) Dropped() bool { return s.dropped.Load() }

// Close ends the subscription. It is safe to call more than once.
func (s *Subscription) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.closeLocked()
}

func (s *Subscription) closeLocked() {
	s.once.Do(func() {
		delete(s.hub.subs, s)
		close(s.ch)
	})
}

// Subscribe adds a subscriber. It returns nil when the hub is full or shut
// down.
func (h *Hub) Subscribe(f Filter) *Subscription {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || len(h.subs) >= h.maxSubs {
		return nil
	}
	s := &Subscription{hub: h, filter: f, ch: make(chan Event, h.buffer)}
	h.subs[s] = struct{}{}
	return s
}

// Publish sends an event to every matching subscriber without blocking, and
// assigns its ID and time (if unset). Subscribers whose buffer is full are
// dropped. A nil hub ignores events, so publishers needn't check.
func (h *Hub) Publish(e Event) {
	if h == nil {
		return
	}
	e.ID = h.nextID.Add(1)
	if e.At.IsZero() {
		e.At = time.Now()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if !s.filter.Match(e) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			s.dropped.Store(true)
			s.closeLocked()
		}
	}
}

// Subscribers returns the number of current subscribers.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Close ends every subscription and refuses new ones, for shutdown.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for s := range h.subs {
		s.closeLocked()
	}
}
