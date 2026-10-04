package stats

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// QueueGauges reports live queue sizes per upstream for the metrics endpoint.
type QueueGauges func(ctx context.Context) (map[string]QueueGauge, error)

// QueueGauge is the live state of one upstream.
type QueueGauge struct {
	Waiting int64
	Dead    int64
	// BreakerOpen is 1 while the circuit breaker is open, 0.5 while
	// half-open and 0 otherwise.
	BreakerOpen float64
	Paused      bool
}

// MetricsHandler serves process-lifetime delivery metrics in the Prometheus
// text format. Counters restart from zero when the process restarts, as
// Prometheus expects.
func (c *Collector) MetricsHandler(gauges QueueGauges) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		c.writeMetrics(w)
		if gauges != nil {
			if g, err := gauges(r.Context()); err == nil {
				writeGauges(w, g)
			}
		}
	})
}

func (c *Collector) writeMetrics(w io.Writer) {
	c.mu.Lock()
	upstreams := make([]string, 0, len(c.totals))
	snapshot := make(map[string]aggregate, len(c.totals))
	for u, a := range c.totals {
		upstreams = append(upstreams, u)
		cp := *a
		cp.hist = append([]int64(nil), a.hist...)
		snapshot[u] = cp
	}
	c.mu.Unlock()
	sort.Strings(upstreams)

	counter := func(name, help string, value func(aggregate) int64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
		for _, u := range upstreams {
			fmt.Fprintf(w, "%s{upstream=%s} %d\n", name, quote(u), value(snapshot[u]))
		}
	}
	counter("hookyard_attempts_total", "Delivery attempts made.", func(a aggregate) int64 { return a.attempts })
	counter("hookyard_attempts_failed_total", "Delivery attempts that failed.", func(a aggregate) int64 { return a.failed })
	counter("hookyard_requests_succeeded_total", "Requests delivered successfully.", func(a aggregate) int64 { return a.succeeded })
	counter("hookyard_requests_dead_total", "Requests moved to the dead-letter queue.", func(a aggregate) int64 { return a.dead })

	const h = "hookyard_attempt_duration_seconds"
	fmt.Fprintf(w, "# HELP %s Delivery attempt duration.\n# TYPE %s histogram\n", h, h)
	for _, u := range upstreams {
		a := snapshot[u]
		var cum int64
		for i, bound := range LatencyBoundsMS {
			cum += a.hist[i]
			le := "+Inf"
			if i < len(LatencyBoundsMS)-1 {
				le = strconv.FormatFloat(float64(bound)/1000, 'f', -1, 64)
			}
			fmt.Fprintf(w, "%s_bucket{upstream=%s,le=%q} %d\n", h, quote(u), le, cum)
		}
		fmt.Fprintf(w, "%s_sum{upstream=%s} %s\n", h, quote(u), strconv.FormatFloat(float64(a.latencySumMS)/1000, 'f', -1, 64))
		fmt.Fprintf(w, "%s_count{upstream=%s} %d\n", h, quote(u), a.attempts)
	}
}

func writeGauges(w io.Writer, g map[string]QueueGauge) {
	upstreams := make([]string, 0, len(g))
	for u := range g {
		upstreams = append(upstreams, u)
	}
	sort.Strings(upstreams)
	fmt.Fprint(w, "# HELP hookyard_queue_waiting Requests waiting to be delivered.\n# TYPE hookyard_queue_waiting gauge\n")
	for _, u := range upstreams {
		fmt.Fprintf(w, "hookyard_queue_waiting{upstream=%s} %d\n", quote(u), g[u].Waiting)
	}
	fmt.Fprint(w, "# HELP hookyard_dlq_size Requests in the dead-letter queue.\n# TYPE hookyard_dlq_size gauge\n")
	for _, u := range upstreams {
		fmt.Fprintf(w, "hookyard_dlq_size{upstream=%s} %d\n", quote(u), g[u].Dead)
	}
	fmt.Fprint(w, "# HELP hookyard_upstream_breaker_open Circuit breaker state: 1 open, 0.5 half-open, 0 closed.\n# TYPE hookyard_upstream_breaker_open gauge\n")
	for _, u := range upstreams {
		fmt.Fprintf(w, "hookyard_upstream_breaker_open{upstream=%s} %s\n", quote(u), strconv.FormatFloat(g[u].BreakerOpen, 'f', -1, 64))
	}
	fmt.Fprint(w, "# HELP hookyard_upstream_paused 1 while an operator has paused the upstream.\n# TYPE hookyard_upstream_paused gauge\n")
	for _, u := range upstreams {
		paused := 0
		if g[u].Paused {
			paused = 1
		}
		fmt.Fprintf(w, "hookyard_upstream_paused{upstream=%s} %d\n", quote(u), paused)
	}
}

// quote escapes a Prometheus label value.
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
