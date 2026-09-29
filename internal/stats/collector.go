// Package stats collects per-upstream delivery metrics and flushes them to
// one-minute rollups in the store.
package stats

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
)

// LatencyBoundsMS are the upper bounds (inclusive, in milliseconds) of the
// latency histogram buckets. The last bucket catches everything slower.
var LatencyBoundsMS = []int{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, store.LatencyInfinity}

type bucketKey struct {
	bucket   time.Time
	upstream string
}

type aggregate struct {
	attempts, failed, succeeded, dead, latencySumMS int64
	hist                                            []int64
}

func newAggregate() *aggregate { return &aggregate{hist: make([]int64, len(LatencyBoundsMS))} }

func (a *aggregate) merge(b *aggregate) {
	a.attempts += b.attempts
	a.failed += b.failed
	a.succeeded += b.succeeded
	a.dead += b.dead
	a.latencySumMS += b.latencySumMS
	for i := range a.hist {
		a.hist[i] += b.hist[i]
	}
}

// Collector accumulates attempt outcomes in memory until they are flushed. It
// also keeps process-lifetime totals for the Prometheus endpoint. It is safe
// for concurrent use.
type Collector struct {
	store *store.Store
	log   *slog.Logger

	mu      sync.Mutex
	pending map[bucketKey]*aggregate
	totals  map[string]*aggregate
}

// NewCollector returns a collector that flushes to st.
func NewCollector(log *slog.Logger, st *store.Store) *Collector {
	return &Collector{store: st, log: log, pending: map[bucketKey]*aggregate{}, totals: map[string]*aggregate{}}
}

// Observe records one delivery attempt. status is the request's resulting
// status: succeeded, failed (retry scheduled) or dead.
func (c *Collector) Observe(upstream string, finishedAt time.Time, d time.Duration, outcome model.AttemptOutcome, status model.Status) {
	ms := d.Milliseconds()
	idx := sort.SearchInts(LatencyBoundsMS, int(ms))

	c.mu.Lock()
	defer c.mu.Unlock()
	key := bucketKey{bucket: finishedAt.UTC().Truncate(time.Minute), upstream: upstream}
	for _, a := range []*aggregate{c.pendingFor(key), c.totalFor(upstream)} {
		a.attempts++
		if outcome != model.OutcomeSuccess {
			a.failed++
		}
		switch status {
		case model.StatusSucceeded:
			a.succeeded++
		case model.StatusDead:
			a.dead++
		default:
		}
		a.latencySumMS += ms
		a.hist[idx]++
	}
}

func (c *Collector) pendingFor(k bucketKey) *aggregate {
	a, ok := c.pending[k]
	if !ok {
		a = newAggregate()
		c.pending[k] = a
	}
	return a
}

func (c *Collector) totalFor(upstream string) *aggregate {
	a, ok := c.totals[upstream]
	if !ok {
		a = newAggregate()
		c.totals[upstream] = a
	}
	return a
}

// Flush writes pending metrics to the store. On failure they are kept and
// retried on the next flush, so nothing is lost or counted twice.
func (c *Collector) Flush(ctx context.Context) error {
	c.mu.Lock()
	batch := c.pending
	c.pending = map[bucketKey]*aggregate{}
	c.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}

	rows := make([]store.StatsRow, 0, len(batch))
	for k, a := range batch {
		row := store.StatsRow{
			Bucket: k.bucket, Upstream: k.upstream,
			Attempts: a.attempts, FailedAttempts: a.failed, Succeeded: a.succeeded, Dead: a.dead,
			LatencySumMS: a.latencySumMS, Latency: map[int]int64{},
		}
		for i, n := range a.hist {
			if n > 0 {
				row.Latency[LatencyBoundsMS[i]] = n
			}
		}
		rows = append(rows, row)
	}

	if err := c.store.AddStats(ctx, rows); err != nil {
		c.mu.Lock()
		for k, a := range batch {
			c.pendingFor(k).merge(a)
		}
		c.mu.Unlock()
		return err
	}
	return nil
}

// Run flushes every interval until ctx is canceled, then flushes once more.
func (c *Collector) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := c.Flush(ctx); err != nil && ctx.Err() == nil {
				c.log.Error("flushing metrics failed; will retry", "error", err)
			}
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if err := c.Flush(flushCtx); err != nil {
				c.log.Error("final metrics flush failed", "error", err)
			}
			cancel()
			return
		}
	}
}
