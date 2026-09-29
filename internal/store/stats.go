package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// LatencyInfinity is the le_ms value of the overflow latency bucket.
const LatencyInfinity = 1<<31 - 1

// StatsRow is one upstream's metrics for one minute. Latency maps a bucket's
// upper bound in milliseconds to its count.
type StatsRow struct {
	Bucket         time.Time
	Upstream       string
	Attempts       int64
	FailedAttempts int64
	Succeeded      int64
	Dead           int64
	LatencySumMS   int64
	Latency        map[int]int64
}

// AddStats adds rows to the rollups. Counts are added to existing values, so
// concurrent writers (other Hookyard instances) never overwrite each other.
func (s *Store) AddStats(ctx context.Context, rows []StatsRow) error {
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(`
			INSERT INTO stats_1m (bucket, upstream, attempts, failed_attempts, succeeded, dead, latency_sum_ms)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (bucket, upstream) DO UPDATE SET
				attempts = stats_1m.attempts + EXCLUDED.attempts,
				failed_attempts = stats_1m.failed_attempts + EXCLUDED.failed_attempts,
				succeeded = stats_1m.succeeded + EXCLUDED.succeeded,
				dead = stats_1m.dead + EXCLUDED.dead,
				latency_sum_ms = stats_1m.latency_sum_ms + EXCLUDED.latency_sum_ms`,
			r.Bucket, r.Upstream, r.Attempts, r.FailedAttempts, r.Succeeded, r.Dead, r.LatencySumMS)
		for le, n := range r.Latency {
			batch.Queue(`
				INSERT INTO stats_1m_latency (bucket, upstream, le_ms, count) VALUES ($1, $2, $3, $4)
				ON CONFLICT (bucket, upstream, le_ms) DO UPDATE SET count = stats_1m_latency.count + EXCLUDED.count`,
				r.Bucket, r.Upstream, le, n)
		}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return fmt.Errorf("add stats: %w", err)
		}
		return nil
	})
}

// StatsTotal is one upstream's metrics summed over a time range.
type StatsTotal struct {
	Attempts       int64
	FailedAttempts int64
	Succeeded      int64
	Dead           int64
	LatencySumMS   int64
	Latency        map[int]int64
}

// StatsTotals sums the rollups since the given time, per upstream.
func (s *Store) StatsTotals(ctx context.Context, since time.Time) (map[string]*StatsTotal, error) {
	out := map[string]*StatsTotal{}
	get := func(u string) *StatsTotal {
		t, ok := out[u]
		if !ok {
			t = &StatsTotal{Latency: map[int]int64{}}
			out[u] = t
		}
		return t
	}

	rows, err := s.pool.Query(ctx, `
		SELECT upstream, sum(attempts), sum(failed_attempts), sum(succeeded), sum(dead), sum(latency_sum_ms)
		FROM stats_1m WHERE bucket >= $1 GROUP BY upstream`, since)
	if err != nil {
		return nil, fmt.Errorf("stats totals: %w", err)
	}
	for rows.Next() {
		var u string
		var t StatsTotal
		if err := rows.Scan(&u, &t.Attempts, &t.FailedAttempts, &t.Succeeded, &t.Dead, &t.LatencySumMS); err != nil {
			rows.Close()
			return nil, err
		}
		dst := get(u)
		t.Latency = dst.Latency
		*dst = t
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.pool.Query(ctx, `
		SELECT upstream, le_ms, sum(count) FROM stats_1m_latency
		WHERE bucket >= $1 GROUP BY upstream, le_ms`, since)
	if err != nil {
		return nil, fmt.Errorf("stats latency: %w", err)
	}
	for rows.Next() {
		var u string
		var le int
		var n int64
		if err := rows.Scan(&u, &le, &n); err != nil {
			rows.Close()
			return nil, err
		}
		get(u).Latency[le] = n
	}
	return out, rows.Err()
}

// QueueStat is the live queue state of one upstream.
type QueueStat struct {
	// Waiting counts requests that are pending or failed (retry scheduled).
	Waiting int64
	// OldestWaitingSince is when the oldest waiting request became due.
	OldestWaitingSince *time.Time
	// Dead counts requests in the dead-letter queue.
	Dead int64
}

// QueueStats returns live queue state per upstream.
func (s *Store) QueueStats(ctx context.Context) (map[string]QueueStat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT upstream,
			count(*) FILTER (WHERE status IN ('pending', 'failed')),
			min(retry_window_start) FILTER (WHERE status IN ('pending', 'failed')),
			count(*) FILTER (WHERE status = 'dead')
		FROM requests
		WHERE status IN ('pending', 'failed', 'dead')
		GROUP BY upstream`)
	if err != nil {
		return nil, fmt.Errorf("queue stats: %w", err)
	}
	out := map[string]QueueStat{}
	for rows.Next() {
		var u string
		var q QueueStat
		if err := rows.Scan(&u, &q.Waiting, &q.OldestWaitingSince, &q.Dead); err != nil {
			rows.Close()
			return nil, err
		}
		out[u] = q
	}
	return out, rows.Err()
}

// SeriesBucket is one time bucket of a stats series.
type SeriesBucket struct {
	Start          time.Time
	Attempts       int64
	FailedAttempts int64
	Succeeded      int64
	Dead           int64
	Latency        map[int]int64
}

// StatsSeries returns metrics in step-sized buckets starting at from (rounded
// down to the minute) and covering [from, to), oldest first. Empty buckets are
// included with zero counts. An empty upstream combines all upstreams.
func (s *Store) StatsSeries(ctx context.Context, upstream string, from, to time.Time, step time.Duration) ([]SeriesBucket, error) {
	from = from.UTC().Truncate(time.Minute)
	var out []SeriesBucket
	index := map[int64]int{}
	for t := from; t.Before(to); t = t.Add(step) {
		index[t.Unix()] = len(out)
		out = append(out, SeriesBucket{Start: t, Latency: map[int]int64{}})
	}
	if len(out) == 0 {
		return out, nil
	}

	// Bins start at from, matching the buckets generated above for any step.
	const bin = `date_bin(make_interval(secs => $1), bucket, $2)`
	filter := `bucket >= $2 AND bucket < $3 AND ($4 = '' OR upstream = $4)`
	args := []any{step.Seconds(), from, to, upstream}

	rows, err := s.pool.Query(ctx, `
		SELECT `+bin+` AS b, sum(attempts), sum(failed_attempts), sum(succeeded), sum(dead)
		FROM stats_1m WHERE `+filter+` GROUP BY b`, args...)
	if err != nil {
		return nil, fmt.Errorf("stats series: %w", err)
	}
	for rows.Next() {
		var b time.Time
		var r SeriesBucket
		if err := rows.Scan(&b, &r.Attempts, &r.FailedAttempts, &r.Succeeded, &r.Dead); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := index[b.Unix()]; ok {
			r.Start, r.Latency = out[i].Start, out[i].Latency
			out[i] = r
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.pool.Query(ctx, `
		SELECT `+bin+` AS b, le_ms, sum(count)
		FROM stats_1m_latency WHERE `+filter+` GROUP BY b, le_ms`, args...)
	if err != nil {
		return nil, fmt.Errorf("stats series latency: %w", err)
	}
	for rows.Next() {
		var b time.Time
		var le int
		var n int64
		if err := rows.Scan(&b, &le, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := index[b.Unix()]; ok {
			out[i].Latency[le] = n
		}
	}
	return out, rows.Err()
}

// DeleteStatsBefore removes rollups older than t.
func (s *Store) DeleteStatsBefore(ctx context.Context, t time.Time) (int64, error) {
	var total int64
	for _, table := range []string{"stats_1m", "stats_1m_latency"} {
		tag, err := s.pool.Exec(ctx, `DELETE FROM `+table+` WHERE bucket < $1`, t)
		if err != nil {
			return total, fmt.Errorf("delete old stats: %w", err)
		}
		total += tag.RowsAffected()
	}
	return total, nil
}

// DeleteExpiredDedupeKeys removes dedupe keys whose window has passed.
func (s *Store) DeleteExpiredDedupeKeys(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM dedupe_keys WHERE expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("delete expired dedupe keys: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteFinishedRequestsBefore removes requests (and their attempts) that
// reached a final state before t, in batches to keep transactions short.
func (s *Store) DeleteFinishedRequestsBefore(ctx context.Context, t time.Time, batchSize int) (int64, error) {
	var total int64
	for {
		tag, err := s.pool.Exec(ctx, `
			DELETE FROM requests WHERE id IN (
				SELECT id FROM requests WHERE completed_at < $1 LIMIT $2
			)`, t, batchSize)
		if err != nil {
			return total, fmt.Errorf("delete finished requests: %w", err)
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < int64(batchSize) {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}
