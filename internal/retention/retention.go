// Package retention deletes old data so the database doesn't grow forever.
package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/tanvir001728/hookyard/internal/store"
)

// StatsRetention is how long metric rollups are kept.
const StatsRetention = 30 * 24 * time.Hour

// Config controls what is deleted and how often.
type Config struct {
	// Requests is how long finished requests (and their attempts) are kept
	// after they complete. Zero keeps them forever.
	Requests time.Duration
	// Interval is how often the cleanup runs.
	Interval time.Duration
}

// Run deletes expired data every cfg.Interval until ctx is canceled.
func Run(ctx context.Context, log *slog.Logger, st *store.Store, cfg Config) {
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Minute
	}
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		Once(ctx, log, st, cfg.Requests, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Once runs a single cleanup pass as of now.
func Once(ctx context.Context, log *slog.Logger, st *store.Store, requests time.Duration, now time.Time) {
	report := func(what string, n int64, err error) {
		switch {
		case err != nil && ctx.Err() == nil:
			log.Error("retention cleanup failed", "what", what, "error", err)
		case n > 0:
			log.Info("retention cleanup", "what", what, "deleted", n)
		}
	}

	n, err := st.DeleteExpiredDedupeKeys(ctx)
	report("expired dedupe keys", n, err)

	n, err = st.DeleteStatsBefore(ctx, now.Add(-StatsRetention))
	report("metric rollups", n, err)

	if requests > 0 {
		n, err = st.DeleteFinishedRequestsBefore(ctx, now.Add(-requests), 1000)
		report("finished requests", n, err)
	}
}
