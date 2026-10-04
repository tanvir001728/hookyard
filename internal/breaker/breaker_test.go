package breaker

import (
	"testing"
	"time"
)

var t0 = time.Unix(1_000_000, 0)

func cfg() Config {
	return Config{FailureRate: 0.5, MinCalls: 4, Window: 10 * time.Second, ConsecutiveFailures: 3, Cooldown: 5 * time.Second, Probes: 2}
}

func state(t *testing.T, b *Breaker, at time.Time, want State) {
	t.Helper()
	if s, _, _ := b.State(at); s != want {
		t.Fatalf("state = %s, want %s", s, want)
	}
}

func TestOpensOnConsecutiveFailures(t *testing.T) {
	b := New(cfg(), t0)
	b.Record(false, t0)
	b.Record(false, t0)
	state(t, b, t0, Closed)
	tr := b.Record(false, t0)
	if tr == nil || tr.To != Open || tr.Reason != "3 consecutive failures" {
		t.Fatalf("transition = %+v", tr)
	}
	if n, _ := b.Allowance(t0); n != 0 {
		t.Errorf("open breaker allowance = %d", n)
	}
}

func TestSuccessResetsConsecutive(t *testing.T) {
	b := New(Config{FailureRate: 0.9, MinCalls: 100, Window: 10 * time.Second, ConsecutiveFailures: 3}, t0)
	for range 10 {
		b.Record(false, t0)
		b.Record(false, t0)
		b.Record(true, t0)
	}
	state(t, b, t0, Closed)
}

func TestOpensOnFailureRate(t *testing.T) {
	b := New(Config{FailureRate: 0.5, MinCalls: 4, Window: 10 * time.Second, ConsecutiveFailures: 100}, t0)
	b.Record(true, t0)
	b.Record(false, t0)
	b.Record(true, t0)
	state(t, b, t0, Closed) // only 3 calls, below MinCalls
	tr := b.Record(false, t0)
	if tr == nil || tr.To != Open || tr.Reason != "50% of the last 4 calls failed" {
		t.Fatalf("transition = %+v", tr)
	}
}

func TestWindowForgetsOldOutcomes(t *testing.T) {
	b := New(Config{FailureRate: 0.5, MinCalls: 4, Window: 10 * time.Second, ConsecutiveFailures: 100}, t0)
	b.Record(false, t0)
	b.Record(false, t0)
	b.Record(false, t0)
	// 11s later the old failures are outside the window.
	later := t0.Add(11 * time.Second)
	b.Record(true, later)
	b.Record(true, later)
	b.Record(true, later)
	if tr := b.Record(false, later); tr != nil {
		t.Fatalf("old failures must not count: %+v", tr)
	}
}

func TestHalfOpenProbes(t *testing.T) {
	b := New(cfg(), t0)
	b.Trip(t0, "test")

	state(t, b, t0.Add(4*time.Second), Open)
	if n, tr := b.Allowance(t0.Add(5 * time.Second)); n != 2 || tr == nil || tr.To != HalfOpen {
		t.Fatalf("after cooldown: allowance %d, transition %+v", n, tr)
	}
	now := t0.Add(5 * time.Second)
	b.Started(now)
	b.Started(now)
	if n, _ := b.Allowance(now); n != 0 {
		t.Errorf("no more probes while 2 are out: %d", n)
	}
	if tr := b.Record(true, now); tr != nil {
		t.Errorf("one probe isn't enough: %+v", tr)
	}
	tr := b.Record(true, now.Add(time.Second))
	if tr == nil || tr.To != Closed || !tr.OpenSince.Equal(t0) {
		t.Fatalf("closing transition = %+v", tr)
	}
	if n, _ := b.Allowance(now); n != -1 {
		t.Errorf("closed allowance = %d, want unlimited", n)
	}
}

func TestFailedProbeReopens(t *testing.T) {
	b := New(cfg(), t0)
	b.Trip(t0, "test")
	now := t0.Add(6 * time.Second)
	b.Allowance(now)
	b.Started(now)
	tr := b.Record(false, now)
	if tr == nil || tr.From != HalfOpen || tr.To != Open {
		t.Fatalf("transition = %+v", tr)
	}
	// The cooldown starts again.
	state(t, b, now.Add(4*time.Second), Open)
	state(t, b, now.Add(5*time.Second), HalfOpen)
}

func TestOutcomesWhileOpenAreIgnored(t *testing.T) {
	b := New(cfg(), t0)
	b.Trip(t0, "test")
	if tr := b.Record(true, t0.Add(time.Second)); tr != nil {
		t.Fatalf("a late result must not close the breaker: %+v", tr)
	}
	state(t, b, t0.Add(time.Second), Open)
}

func TestDefaults(t *testing.T) {
	if got := (Config{}).WithDefaults(); got != Defaults {
		t.Fatalf("got %+v", got)
	}
}
