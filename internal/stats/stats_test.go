package stats

import (
	"context"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
	"github.com/tanvir001728/hookyard/internal/store/storetest"
)

func TestPercentile(t *testing.T) {
	if Percentile(map[int]int64{}, 0.5) != nil {
		t.Error("empty histogram must give nil")
	}

	// 100 attempts: 50 in (0,5], 40 in (5,10], 10 in (100,250].
	h := map[int]int64{5: 50, 10: 40, 250: 10}
	tests := map[float64]float64{
		0.50: 5,     // exactly the top of the first bucket
		0.25: 2.5,   // halfway through the first bucket
		0.70: 7.5,   // halfway through the second bucket
		0.95: 175,   // halfway through (100,250]
		0.99: 235,   // 90% through (100,250]
		1.00: 250.0, // the top of the last non-empty bucket
	}
	for q, want := range tests {
		got := Percentile(h, q)
		if got == nil || math.Abs(*got-want) > 1e-9 {
			t.Errorf("Percentile(%.2f) = %v, want %v", q, got, want)
		}
	}

	// Everything in the overflow bucket reports the largest finite bound.
	if got := Percentile(map[int]int64{store.LatencyInfinity: 3}, 0.5); got == nil || *got != 60000 {
		t.Errorf("overflow bucket: %v", got)
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestCollectorFlush(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	c := NewCollector(discard(), st)
	at := time.Now()

	c.Observe("courier-x", at, 40*time.Millisecond, model.OutcomeSuccess, model.StatusSucceeded)
	c.Observe("courier-x", at, 3*time.Second, model.OutcomeRetryableFailure, model.StatusFailed)
	c.Observe("courier-x", at, 7*time.Millisecond, model.OutcomePermanentFailure, model.StatusDead)
	c.Observe("payments-y", at, time.Millisecond, model.OutcomeSuccess, model.StatusSucceeded)

	if err := c.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Flushing twice must not double count.
	if err := c.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}

	totals, err := st.StatsTotals(t.Context(), at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	cx := totals["courier-x"]
	if cx == nil || cx.Attempts != 3 || cx.FailedAttempts != 2 || cx.Succeeded != 1 || cx.Dead != 1 || cx.LatencySumMS != 3047 {
		t.Fatalf("courier-x totals = %+v", cx)
	}
	if cx.Latency[50] != 1 || cx.Latency[5000] != 1 || cx.Latency[10] != 1 {
		t.Errorf("latency histogram = %v", cx.Latency)
	}
	if py := totals["payments-y"]; py == nil || py.Attempts != 1 || py.Latency[5] != 1 {
		t.Errorf("payments-y totals = %+v", py)
	}
}

func TestCollectorKeepsDataWhenFlushFails(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	c := NewCollector(discard(), st)
	c.Observe("courier-x", time.Now(), time.Millisecond, model.OutcomeSuccess, model.StatusSucceeded)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.Flush(canceled); err == nil {
		t.Fatal("flush with a canceled context should fail")
	}
	c.Observe("courier-x", time.Now(), time.Millisecond, model.OutcomeSuccess, model.StatusSucceeded)
	if err := c.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}

	totals, _ := st.StatsTotals(t.Context(), time.Now().Add(-time.Hour))
	if got := totals["courier-x"]; got == nil || got.Succeeded != 2 {
		t.Errorf("after a failed then successful flush: %+v, want both observations", got)
	}
}

func TestMetricsHandler(t *testing.T) {
	c := NewCollector(discard(), nil)
	c.Observe(`we"ird`, time.Now(), 7*time.Millisecond, model.OutcomeSuccess, model.StatusSucceeded)
	c.Observe(`we"ird`, time.Now(), 2*time.Second, model.OutcomeRetryableFailure, model.StatusDead)

	gauges := func(context.Context) (map[string]QueueGauge, error) {
		return map[string]QueueGauge{`we"ird`: {Waiting: 4, Dead: 1, BreakerOpen: 0.5, Paused: true}}, nil
	}
	rec := httptest.NewRecorder()
	c.MetricsHandler(gauges).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	for _, want := range []string{
		`hookyard_attempts_total{upstream="we\"ird"} 2`,
		`hookyard_attempts_failed_total{upstream="we\"ird"} 1`,
		`hookyard_requests_succeeded_total{upstream="we\"ird"} 1`,
		`hookyard_requests_dead_total{upstream="we\"ird"} 1`,
		`hookyard_attempt_duration_seconds_bucket{upstream="we\"ird",le="0.01"} 1`,
		`hookyard_attempt_duration_seconds_bucket{upstream="we\"ird",le="2.5"} 2`,
		`hookyard_attempt_duration_seconds_bucket{upstream="we\"ird",le="+Inf"} 2`,
		`hookyard_attempt_duration_seconds_sum{upstream="we\"ird"} 2.007`,
		`hookyard_attempt_duration_seconds_count{upstream="we\"ird"} 2`,
		`hookyard_queue_waiting{upstream="we\"ird"} 4`,
		`hookyard_dlq_size{upstream="we\"ird"} 1`,
		`hookyard_upstream_breaker_open{upstream="we\"ird"} 0.5`,
		`hookyard_upstream_paused{upstream="we\"ird"} 1`,
		"# TYPE hookyard_attempt_duration_seconds histogram",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output lacks %q\n%s", want, body)
		}
	}
}
