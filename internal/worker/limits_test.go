package worker

import (
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/breaker"
	"github.com/tanvir001728/hookyard/internal/config"
)

func testRegistry(ups ...config.Upstream) *config.Registry {
	base, _ := url.Parse("http://vendor.test")
	for i := range ups {
		ups[i].BaseURL = base
	}
	return config.NewRegistry(ups)
}

func TestLimiterAllowance(t *testing.T) {
	t0 := time.Unix(1000, 0)
	l := newLimiter(testRegistry(
		config.Upstream{Name: "rated", Limits: config.Limits{RateLimit: 2, Burst: 2}},
		config.Upstream{Name: "capped", Limits: config.Limits{MaxConcurrency: 1}},
		config.Upstream{Name: "both", Limits: config.Limits{RateLimit: 10, Burst: 10, MaxConcurrency: 3}},
		config.Upstream{Name: "free"},
	), t0)

	exclude, caps, _ := l.allowance(t0)
	if len(exclude) != 0 || caps["rated"] != 2 || caps["capped"] != 1 || caps["both"] != 3 {
		t.Fatalf("initial: exclude=%v caps=%v", exclude, caps)
	}
	if _, ok := caps["free"]; ok {
		t.Error("unlimited upstreams must not get a cap")
	}

	l.acquire("rated", t0)
	l.acquire("rated", t0)
	l.acquire("capped", t0)
	exclude, _, _ = l.allowance(t0)
	slices.Sort(exclude)
	if !slices.Equal(exclude, []string{"capped", "rated"}) {
		t.Errorf("exhausted upstreams should be excluded: %v", exclude)
	}
	if next := l.nextChange(t0); next != t0.Add(500*time.Millisecond) {
		t.Errorf("next token for rated at %v, want +500ms", next.Sub(t0))
	}

	// Releasing frees concurrency; time refills tokens.
	l.release("capped")
	_, caps, _ = l.allowance(t0.Add(500 * time.Millisecond))
	if caps["capped"] != 1 || caps["rated"] != 1 {
		t.Errorf("after release and 0.5s: caps=%v", caps)
	}
}

func TestLimiterBlock(t *testing.T) {
	t0 := time.Unix(1000, 0)
	l := newLimiter(testRegistry(config.Upstream{Name: "free"}), t0)
	l.block("free", t0.Add(2*time.Second))

	if exclude, _, _ := l.allowance(t0.Add(time.Second)); !slices.Contains(exclude, "free") {
		t.Error("a blocked upstream must be excluded, even without limits")
	}
	if exclude, _, _ := l.allowance(t0.Add(2 * time.Second)); len(exclude) != 0 {
		t.Error("the block ends on time")
	}
	if next := l.nextChange(t0); next != t0.Add(2*time.Second) {
		t.Errorf("nextChange = %v, want the end of the block", next.Sub(t0))
	}
	// Unknown upstreams (removed from the config) are ignored.
	l.acquire("gone", t0)
	l.release("gone")
	l.block("gone", t0.Add(time.Hour))
}

// When a probe closes the breaker, the upstream must stay out of claims until
// unhold: allowance() must never see "closed but not held" in between.
func TestLimiterCloseAndHoldAreAtomic(t *testing.T) {
	cfg := breaker.Config{FailureRate: 0.5, MinCalls: 100, Window: time.Minute, ConsecutiveFailures: 1, Cooldown: time.Millisecond, Probes: 1}
	for round := range 1000 {
		now := time.Unix(1000, 0)
		l := newLimiter(testRegistry(config.Upstream{Name: "u", Breaker: config.Breaker{Enabled: true, Config: cfg}}), now)

		// Open the breaker, let the cooldown pass, and send the one probe.
		l.record("u", false, now)
		later := now.Add(time.Second)
		if _, caps, _ := l.allowance(later); caps["u"] != 1 {
			t.Fatalf("round %d: half-open probe allowance = %v", round, caps)
		}
		l.acquire("u", later)

		closed := make(chan struct{})
		leaked := make(chan bool, 1)
		go func() {
			// Spin until the probe's success has been recorded.
			for {
				exclude, _, _ := l.allowance(later)
				if !slices.Contains(exclude, "u") {
					leaked <- true // claimable before unhold
					return
				}
				select {
				case <-closed:
					leaked <- false
					return
				default:
				}
			}
		}()
		if tr := l.record("u", true, later); tr == nil || tr.To != breaker.Closed {
			t.Fatalf("round %d: expected the breaker to close, got %+v", round, tr)
		}
		close(closed)
		if <-leaked {
			t.Fatalf("round %d: the upstream became claimable before its retry windows were extended", round)
		}
		l.unhold("u")
		if exclude, _, _ := l.allowance(later); slices.Contains(exclude, "u") {
			t.Fatalf("round %d: still excluded after unhold", round)
		}
	}
}
