package events

import (
	"testing"
	"time"
)

func TestFilter(t *testing.T) {
	e := Event{Type: TypeAttempt, Upstream: "courier-x", Status: "dead"}
	up := Event{Type: TypeUpstream, Upstream: "courier-x"}
	tests := []struct {
		f        Filter
		attempt  bool
		upstream bool
	}{
		{Filter{}, true, true},
		{Filter{Upstreams: map[string]bool{"courier-x": true}}, true, true},
		{Filter{Upstreams: map[string]bool{"payments-y": true}}, false, false},
		{Filter{Statuses: map[string]bool{"dead": true}}, true, false},
		{Filter{Statuses: map[string]bool{"succeeded": true}}, false, false},
		{Filter{Types: map[string]bool{TypeUpstream: true}}, false, true},
	}
	for i, tt := range tests {
		if got := tt.f.Match(e); got != tt.attempt {
			t.Errorf("%d: attempt match = %v", i, got)
		}
		if got := tt.f.Match(up); got != tt.upstream {
			t.Errorf("%d: upstream match = %v", i, got)
		}
	}
}

func TestHubDeliversInOrderToMatchingSubscribers(t *testing.T) {
	h := NewHub(8, 0)
	all := h.Subscribe(Filter{})
	dead := h.Subscribe(Filter{Statuses: map[string]bool{"dead": true}})
	for _, st := range []string{"succeeded", "dead", "failed"} {
		h.Publish(Event{Type: TypeAttempt, Status: st})
	}
	var ids []uint64
	for range 3 {
		ids = append(ids, (<-all.Events()).ID)
	}
	if ids[0] >= ids[1] || ids[1] >= ids[2] {
		t.Errorf("ids not increasing: %v", ids)
	}
	if e := <-dead.Events(); e.Status != "dead" || e.At.IsZero() {
		t.Errorf("filtered event = %+v", e)
	}
	select {
	case e := <-dead.Events():
		t.Errorf("unexpected event %+v", e)
	default:
	}
}

func TestSlowSubscriberIsDroppedWithoutBlocking(t *testing.T) {
	h := NewHub(2, 0)
	slow := h.Subscribe(Filter{})
	fast := h.Subscribe(Filter{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			h.Publish(Event{Type: TypeAttempt})
			<-fast.Events()
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publishing blocked on a slow subscriber")
	}
	if !slow.Dropped() || fast.Dropped() {
		t.Errorf("dropped: slow %v, fast %v", slow.Dropped(), fast.Dropped())
	}
	// The slow subscriber's channel delivers what it buffered, then closes.
	n := 0
	for range slow.Events() {
		n++
	}
	if n != 2 || h.Subscribers() != 1 {
		t.Errorf("buffered %d, subscribers %d", n, h.Subscribers())
	}
}

func TestHubLimitsAndClose(t *testing.T) {
	h := NewHub(1, 2)
	a, b := h.Subscribe(Filter{}), h.Subscribe(Filter{})
	if h.Subscribe(Filter{}) != nil {
		t.Error("a third subscriber must be refused")
	}
	a.Close()
	a.Close() // idempotent
	if h.Subscribe(Filter{}) == nil {
		t.Error("closing frees a slot")
	}
	h.Close()
	if _, ok := <-b.Events(); ok || b.Dropped() {
		t.Error("closing the hub ends subscriptions without marking them dropped")
	}
	if h.Subscribe(Filter{}) != nil {
		t.Error("a closed hub refuses subscribers")
	}
	var nilHub *Hub
	nilHub.Publish(Event{}) // no-op
}
