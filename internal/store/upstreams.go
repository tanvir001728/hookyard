package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// UpstreamEvent is a change in an upstream's state, such as a circuit breaker
// opening or closing.
type UpstreamEvent struct {
	ID       int64
	Upstream string
	At       time.Time
	Kind     string
	Reason   string
	Actor    string
	Details  map[string]any
}

// AddUpstreamEvent records an upstream event.
func (s *Store) AddUpstreamEvent(ctx context.Context, e UpstreamEvent) error {
	details, err := json.Marshal(e.Details)
	if err != nil {
		return err
	}
	if e.Details == nil {
		details = []byte("{}")
	}
	actor := e.Actor
	if actor == "" {
		actor = "hookyard"
	}
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO upstream_events (upstream, at, kind, reason, actor, details)
		VALUES ($1, $2, $3, $4, $5, $6)`, e.Upstream, at, e.Kind, e.Reason, actor, details)
	if err != nil {
		return fmt.Errorf("add upstream event: %w", err)
	}
	return nil
}

// ListUpstreamEvents returns an upstream's most recent events, newest first.
func (s *Store) ListUpstreamEvents(ctx context.Context, upstream string, limit int) ([]UpstreamEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, upstream, at, kind, reason, actor, details
		FROM upstream_events WHERE upstream = $1
		ORDER BY id DESC LIMIT $2`, upstream, limit)
	if err != nil {
		return nil, fmt.Errorf("list upstream events: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (UpstreamEvent, error) {
		var e UpstreamEvent
		var details []byte
		if err := row.Scan(&e.ID, &e.Upstream, &e.At, &e.Kind, &e.Reason, &e.Actor, &details); err != nil {
			return e, err
		}
		return e, json.Unmarshal(details, &e.Details)
	})
}

// ExtendRetryWindows gives waiting requests of an upstream back the time they
// spent paused (since pausedSince), so a pause doesn't count against their
// max_age. Each request is credited only for the part of the pause that fell
// inside its own retry window. It returns how many requests were extended.
func (s *Store) ExtendRetryWindows(ctx context.Context, upstream string, pausedSince time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE requests SET
			retry_window_start = retry_window_start + (now() - GREATEST($2::timestamptz, retry_window_start)),
			updated_at = now()
		WHERE upstream = $1
			AND status IN ('scheduled', 'pending', 'failed')
			AND retry_window_start < now()`, upstream, pausedSince)
	if err != nil {
		return 0, fmt.Errorf("extend retry windows: %w", err)
	}
	return tag.RowsAffected(), nil
}
