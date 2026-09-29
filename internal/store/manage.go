package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tanvir001728/hookyard/internal/model"
)

// ReplayableStatuses are the final states a request can be replayed from.
var ReplayableStatuses = []model.Status{model.StatusDead, model.StatusSucceeded, model.StatusCanceled, model.StatusUnknown}

// CancelableStatuses are the states a request can be canceled from. An
// in_flight request can't be canceled: the call may already have been sent.
var CancelableStatuses = []model.Status{model.StatusScheduled, model.StatusPending, model.StatusFailed}

// InvalidStateError is returned when an action isn't allowed in a request's
// current state.
type InvalidStateError struct {
	Action string
	Status model.Status
}

func (e *InvalidStateError) Error() string {
	var allowed []model.Status
	switch e.Action {
	case "replay":
		allowed = ReplayableStatuses
	case "cancel":
		allowed = CancelableStatuses
	}
	names := make([]string, len(allowed))
	for i, s := range allowed {
		names[i] = string(s)
	}
	return fmt.Sprintf("cannot %s a request that is %s (allowed: %s)", e.Action, e.Status, strings.Join(names, ", "))
}

// ReplayRequest queues a finished request for delivery again with a fresh
// retry budget. Previous attempts are kept.
func (s *Store) ReplayRequest(ctx context.Context, id, actor string) (model.Request, error) {
	return s.transition(ctx, id, actor, "replay", ReplayableStatuses, `
		status = 'pending',
		next_attempt_at = now(),
		retry_window_start = now(),
		retry_attempt_base = attempt_count,
		completed_at = NULL,
		updated_at = now()`)
}

// CancelRequest cancels a request that hasn't finished.
func (s *Store) CancelRequest(ctx context.Context, id, actor string) (model.Request, error) {
	return s.transition(ctx, id, actor, "cancel", CancelableStatuses, `
		status = 'canceled',
		next_attempt_at = NULL,
		last_error_code = 'canceled',
		last_error_message = 'canceled by ' || $2,
		completed_at = now(),
		updated_at = now()`, actor)
}

// transition applies set to request id if its status is in from, and records
// the action in the audit log, in one transaction. In set, $1 is the id and
// $2, $3, ... are args.
func (s *Store) transition(ctx context.Context, id, actor, action string, from []model.Status, set string, args ...any) (model.Request, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Request{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current string
	err = tx.QueryRow(ctx, `SELECT status FROM requests WHERE id = $1 FOR UPDATE`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Request{}, ErrNotFound
	}
	if err != nil {
		return model.Request{}, err
	}
	if !slices.Contains(from, model.Status(current)) {
		return model.Request{}, &InvalidStateError{Action: action, Status: model.Status(current)}
	}

	req, err := scanRequest(tx.QueryRow(ctx, `UPDATE requests SET `+set+` WHERE id = $1 RETURNING `+requestColumns, append([]any{id}, args...)...))
	if err != nil {
		return model.Request{}, fmt.Errorf("%s request: %w", action, err)
	}
	if err := insertAudit(ctx, tx, actor, action, "request", id, map[string]any{"from_status": current}); err != nil {
		return model.Request{}, err
	}
	return req, tx.Commit(ctx)
}

// DLQFilter selects dead requests. Zero values match everything.
type DLQFilter struct {
	Upstream   string     `json:"upstream,omitempty"`
	ErrorCode  string     `json:"error_code,omitempty"`
	StatusCode *int       `json:"status_code,omitempty"`
	DeadAfter  *time.Time `json:"dead_after,omitempty"`
	DeadBefore *time.Time `json:"dead_before,omitempty"`
	IDs        []string   `json:"ids,omitempty"`
}

func (f DLQFilter) where() (string, []any) {
	conds := []string{"status = 'dead'"}
	var args []any
	add := func(cond string, arg any) {
		args = append(args, arg)
		conds = append(conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.Upstream != "" {
		add("upstream = ?", f.Upstream)
	}
	if f.ErrorCode != "" {
		add("last_error_code = ?", f.ErrorCode)
	}
	if f.StatusCode != nil {
		add("last_status_code = ?", *f.StatusCode)
	}
	if f.DeadAfter != nil {
		add("completed_at >= ?", *f.DeadAfter)
	}
	if f.DeadBefore != nil {
		add("completed_at < ?", *f.DeadBefore)
	}
	if f.IDs != nil {
		add("id = ANY(?)", f.IDs)
	}
	return strings.Join(conds, " AND "), args
}

// DLQGroup summarizes dead requests sharing an upstream and failure reason.
type DLQGroup struct {
	Upstream     string
	ErrorCode    string
	StatusCode   *int
	Count        int
	OldestDeadAt time.Time
	NewestDeadAt time.Time
}

// DLQSummary groups dead requests by upstream and failure reason, largest
// group first, and returns the total.
func (s *Store) DLQSummary(ctx context.Context, upstream string) ([]DLQGroup, int, error) {
	where, args := DLQFilter{Upstream: upstream}.where()
	rows, err := s.pool.Query(ctx, `
		SELECT upstream, COALESCE(last_error_code, 'internal'), last_status_code, count(*),
			min(completed_at), max(completed_at)
		FROM requests WHERE `+where+`
		GROUP BY 1, 2, 3
		ORDER BY 4 DESC, 1, 2, 3`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("dlq summary: %w", err)
	}
	groups, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DLQGroup, error) {
		var g DLQGroup
		err := row.Scan(&g.Upstream, &g.ErrorCode, &g.StatusCode, &g.Count, &g.OldestDeadAt, &g.NewestDeadAt)
		return g, err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("dlq summary: %w", err)
	}
	total := 0
	for _, g := range groups {
		total += g.Count
	}
	return groups, total, nil
}

// ReplayDead queues every dead request matching f for delivery again, with a
// fresh retry budget. With dryRun it only counts. It is idempotent: replayed
// requests are no longer dead, so they aren't matched twice.
func (s *Store) ReplayDead(ctx context.Context, f DLQFilter, dryRun bool, actor string) (matched, replayed int, err error) {
	where, args := f.where()
	if dryRun {
		err := s.pool.QueryRow(ctx, `SELECT count(*) FROM requests WHERE `+where, args...).Scan(&matched)
		return matched, 0, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE requests SET
			status = 'pending',
			next_attempt_at = now(),
			retry_window_start = now(),
			retry_attempt_base = attempt_count,
			completed_at = NULL,
			updated_at = now()
		WHERE `+where, args...)
	if err != nil {
		return 0, 0, fmt.Errorf("replay dead requests: %w", err)
	}
	n := int(tag.RowsAffected())
	if n > 0 {
		details := map[string]any{"filter": f, "replayed": n}
		if err := insertAudit(ctx, tx, actor, "dlq.replay", "dlq", "", details); err != nil {
			return 0, 0, err
		}
	}
	return n, n, tx.Commit(ctx)
}

func insertAudit(ctx context.Context, tx pgx.Tx, actor, action, targetType, targetID string, details any) error {
	b, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode audit details: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_log (actor, action, target_type, target_id, details)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5)`, actor, action, targetType, targetID, b)
	if err != nil {
		return fmt.Errorf("write audit log: %w", err)
	}
	return nil
}

// AuditEntry is one record in the audit log.
type AuditEntry struct {
	At         time.Time
	Actor      string
	Action     string
	TargetType string
	TargetID   string
	Details    json.RawMessage
}

// ListAudit returns the most recent audit entries for a target, newest first.
// An empty targetID lists entries for the whole target type.
func (s *Store) ListAudit(ctx context.Context, targetType, targetID string, limit int) ([]AuditEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT at, actor, action, target_type, COALESCE(target_id, ''), details
		FROM audit_log
		WHERE target_type = $1 AND ($2 = '' OR target_id = $2)
		ORDER BY id DESC LIMIT $3`, targetType, targetID, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit log: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AuditEntry, error) {
		var e AuditEntry
		err := row.Scan(&e.At, &e.Actor, &e.Action, &e.TargetType, &e.TargetID, &e.Details)
		return e, err
	})
}
