package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	return addUpstreamEvent(ctx, s.pool, e)
}

func addUpstreamEvent(ctx context.Context, q querier, e UpstreamEvent) error {
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
	_, err = q.Exec(ctx, `
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

// querier is satisfied by both the pool and a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ExtendRetryWindows gives waiting requests of an upstream back the time they
// spent paused (since pausedSince), so a pause doesn't count against their
// max_age. Each request is credited only for the part of the pause that fell
// inside its own retry window. It returns how many requests were extended.
func (s *Store) ExtendRetryWindows(ctx context.Context, upstream string, pausedSince time.Time) (int64, error) {
	return extendRetryWindows(ctx, s.pool, upstream, pausedSince)
}

func extendRetryWindows(ctx context.Context, q querier, upstream string, pausedSince time.Time) (int64, error) {
	tag, err := q.Exec(ctx, `
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

// Pause is an operator's pause of an upstream.
type Pause struct {
	Upstream string
	PausedAt time.Time
	// Until is when the pause ends on its own; nil means until resumed.
	Until  *time.Time
	Reason string
	Actor  string
}

// PauseUpstream pauses an upstream, or updates the reason and end of an
// existing pause (keeping when it started).
func (s *Store) PauseUpstream(ctx context.Context, p Pause) (Pause, error) {
	var out Pause
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO upstream_pauses (upstream, until, reason, actor) VALUES ($1, $2, $3, $4)
			ON CONFLICT (upstream) DO UPDATE SET until = EXCLUDED.until, reason = EXCLUDED.reason, actor = EXCLUDED.actor
			RETURNING upstream, paused_at, until, reason, actor`,
			p.Upstream, p.Until, p.Reason, p.Actor,
		).Scan(&out.Upstream, &out.PausedAt, &out.Until, &out.Reason, &out.Actor)
		if err != nil {
			return fmt.Errorf("pause upstream: %w", err)
		}
		details := map[string]any{}
		if p.Until != nil {
			details["until"] = p.Until.UTC()
		}
		if err := addUpstreamEvent(ctx, tx, UpstreamEvent{Upstream: p.Upstream, Kind: "paused", Reason: p.Reason, Actor: p.Actor, Details: details}); err != nil {
			return err
		}
		return insertAudit(ctx, tx, p.Actor, "upstream.pause", "upstream", p.Upstream, map[string]any{"reason": p.Reason, "until": p.Until})
	})
	return out, err
}

// ResumeUpstream ends an upstream's pause and, in the same transaction,
// credits waiting requests the paused time. It reports false if the upstream
// wasn't paused.
func (s *Store) ResumeUpstream(ctx context.Context, upstream, actor, reason string) (bool, error) {
	resumed := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var pausedAt time.Time
		err := tx.QueryRow(ctx, `DELETE FROM upstream_pauses WHERE upstream = $1 RETURNING paused_at`, upstream).Scan(&pausedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("resume upstream: %w", err)
		}
		resumed = true
		return finishResume(ctx, tx, upstream, pausedAt, actor, reason)
	})
	return resumed, err
}

// ResumeExpiredPauses ends pauses whose "until" has passed and returns the
// upstreams that were resumed. Safe to call from several instances: each
// pause is resumed exactly once.
func (s *Store) ResumeExpiredPauses(ctx context.Context) ([]string, error) {
	var resumed []string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `DELETE FROM upstream_pauses WHERE until <= now() RETURNING upstream, paused_at`)
		if err != nil {
			return fmt.Errorf("resume expired pauses: %w", err)
		}
		type ended struct {
			upstream string
			at       time.Time
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ended, error) {
			var e ended
			return e, row.Scan(&e.upstream, &e.at)
		})
		if err != nil {
			return err
		}
		for _, e := range list {
			if err := finishResume(ctx, tx, e.upstream, e.at, "hookyard", "the pause ended"); err != nil {
				return err
			}
			resumed = append(resumed, e.upstream)
		}
		return nil
	})
	return resumed, err
}

func finishResume(ctx context.Context, tx pgx.Tx, upstream string, pausedAt time.Time, actor, reason string) error {
	n, err := extendRetryWindows(ctx, tx, upstream, pausedAt)
	if err != nil {
		return err
	}
	details := map[string]any{"paused_at": pausedAt.UTC(), "requests_extended": n}
	if err := addUpstreamEvent(ctx, tx, UpstreamEvent{Upstream: upstream, Kind: "resumed", Reason: reason, Actor: actor, Details: details}); err != nil {
		return err
	}
	return insertAudit(ctx, tx, actor, "upstream.resume", "upstream", upstream, details)
}

// ListPauses returns every active pause, keyed by upstream.
func (s *Store) ListPauses(ctx context.Context) (map[string]Pause, error) {
	rows, err := s.pool.Query(ctx, `SELECT upstream, paused_at, until, reason, actor FROM upstream_pauses`)
	if err != nil {
		return nil, fmt.Errorf("list pauses: %w", err)
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Pause, error) {
		var p Pause
		return p, row.Scan(&p.Upstream, &p.PausedAt, &p.Until, &p.Reason, &p.Actor)
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]Pause, len(list))
	for _, p := range list {
		out[p.Upstream] = p
	}
	return out, nil
}
