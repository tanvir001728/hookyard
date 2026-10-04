package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tanvir001728/hookyard/internal/model"
)

// Callback delivery statuses.
const (
	CallbackPending    = "pending"
	CallbackDelivering = "delivering"
	CallbackDelivered  = "delivered"
	CallbackFailed     = "failed"
)

// Callback is one completion event for an application. Callbacks are queued
// by the database when a request with a callback URL finishes.
type Callback struct {
	ID        string
	RequestID string
	URL       string
	EventType string
	// RequestStatus and RequestAttempts describe the request when it finished.
	RequestStatus   model.Status
	RequestAttempts int
	Status          string
	// Payload is the signed body, empty until the first attempt.
	Payload        string
	AttemptCount   int
	NextAttemptAt  time.Time
	LastStatusCode *int
	LastError      string
	LastAttemptAt  *time.Time
	CreatedAt      time.Time
	DeliveredAt    *time.Time
}

const callbackColumns = `id, request_id, url, event_type, request_status, request_attempts, status,
	COALESCE(payload, ''), attempt_count, next_attempt_at, last_status_code, COALESCE(last_error, ''),
	last_attempt_at, created_at, delivered_at`

func scanCallback(row pgx.CollectableRow) (Callback, error) {
	var c Callback
	var status string
	err := row.Scan(&c.ID, &c.RequestID, &c.URL, &c.EventType, &status, &c.RequestAttempts, &c.Status,
		&c.Payload, &c.AttemptCount, &c.NextAttemptAt, &c.LastStatusCode, &c.LastError,
		&c.LastAttemptAt, &c.CreatedAt, &c.DeliveredAt)
	c.RequestStatus = model.Status(status)
	return c, err
}

// CallbackClaim is a callback a dispatcher owns until LeaseExpiresAt.
type CallbackClaim struct {
	Callback Callback
	// LeaseExpiresAt also acts as a fencing token for RecordCallbackAttempt.
	LeaseExpiresAt time.Time
}

// ClaimCallbacks marks up to limit due callbacks as delivering and returns
// them, counting the attempt. Concurrent callers never get the same one.
func (s *Store) ClaimCallbacks(ctx context.Context, limit int, lease time.Duration) ([]CallbackClaim, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE callbacks c SET
			status = 'delivering',
			lease_expires_at = now() + $2 * interval '1 millisecond',
			attempt_count = c.attempt_count + 1,
			last_attempt_at = now()
		FROM (
			SELECT id FROM callbacks
			WHERE status = 'pending' AND next_attempt_at <= now()
			ORDER BY next_attempt_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		) due
		WHERE c.id = due.id
		RETURNING `+claimedCallbackColumns+`, c.lease_expires_at`, limit, lease.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("claim callbacks: %w", err)
	}
	claims, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (CallbackClaim, error) {
		var cl CallbackClaim
		var status string
		c := &cl.Callback
		err := row.Scan(&c.ID, &c.RequestID, &c.URL, &c.EventType, &status, &c.RequestAttempts, &c.Status,
			&c.Payload, &c.AttemptCount, &c.NextAttemptAt, &c.LastStatusCode, &c.LastError,
			&c.LastAttemptAt, &c.CreatedAt, &c.DeliveredAt, &cl.LeaseExpiresAt)
		c.RequestStatus = model.Status(status)
		return cl, err
	})
	if err != nil {
		return nil, fmt.Errorf("claim callbacks: %w", err)
	}
	return claims, nil
}

const claimedCallbackColumns = `c.id, c.request_id, c.url, c.event_type, c.request_status, c.request_attempts, c.status,
	COALESCE(c.payload, ''), c.attempt_count, c.next_attempt_at, c.last_status_code, COALESCE(c.last_error, ''),
	c.last_attempt_at, c.created_at, c.delivered_at`

// NextCallbackDueAt returns when the next pending callback is due, or nil.
func (s *Store) NextCallbackDueAt(ctx context.Context) (*time.Time, error) {
	var next *time.Time
	err := s.pool.QueryRow(ctx, `SELECT min(next_attempt_at) FROM callbacks WHERE status = 'pending'`).Scan(&next)
	return next, err
}

// CallbackResult is the outcome of one callback attempt.
type CallbackResult struct {
	ID             string
	LeaseExpiresAt time.Time
	// Payload is stored if the callback has none yet.
	Payload    string
	Delivered  bool
	StatusCode *int
	Error      string
	// NextAttemptAt schedules a retry; nil without Delivered means the
	// callback failed for good.
	NextAttemptAt *time.Time
}

// RecordCallbackAttempt stores the outcome of an attempt. It returns
// ErrLeaseLost, and changes nothing, if the lease no longer matches.
func (s *Store) RecordCallbackAttempt(ctx context.Context, r CallbackResult) error {
	status := CallbackFailed
	switch {
	case r.Delivered:
		status = CallbackDelivered
	case r.NextAttemptAt != nil:
		status = CallbackPending
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE callbacks SET
			status = $3,
			payload = COALESCE(payload, NULLIF($4, '')),
			last_status_code = $5,
			last_error = NULLIF($6, ''),
			next_attempt_at = COALESCE($7, next_attempt_at),
			lease_expires_at = NULL,
			delivered_at = CASE WHEN $3 = 'delivered' THEN now() END
		WHERE id = $1 AND status = 'delivering' AND lease_expires_at = $2`,
		r.ID, r.LeaseExpiresAt, status, r.Payload, r.StatusCode, r.Error, r.NextAttemptAt)
	if err != nil {
		return fmt.Errorf("record callback attempt: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// RecoverExpiredCallbackLeases returns callbacks whose dispatcher stopped
// mid-delivery to pending.
func (s *Store) RecoverExpiredCallbackLeases(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE callbacks SET status = 'pending', next_attempt_at = now(), lease_expires_at = NULL,
			last_error = 'delivery was interrupted before its outcome was recorded; retrying'
		WHERE status = 'delivering' AND lease_expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("recover callback leases: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListCallbacks returns a request's callbacks, oldest first, or ErrNotFound
// if the request doesn't exist.
func (s *Store) ListCallbacks(ctx context.Context, requestID string) ([]Callback, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+callbackColumns+` FROM callbacks WHERE request_id = $1 ORDER BY created_at, id`, requestID)
	if err != nil {
		return nil, fmt.Errorf("list callbacks: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanCallback)
	if err != nil {
		return nil, fmt.Errorf("list callbacks: %w", err)
	}
	if len(out) == 0 {
		if _, err := s.GetRequest(ctx, requestID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CallbackStateError is returned when retrying a callback that hasn't failed.
type CallbackStateError struct{ Status string }

func (e *CallbackStateError) Error() string {
	return fmt.Sprintf("cannot retry a callback that is %s (allowed: failed)", e.Status)
}

// RetryCallback sends a failed callback again with a fresh attempt budget.
// It returns ErrNotFound if there is no such callback for the request and
// a *CallbackStateError if it hasn't failed.
func (s *Store) RetryCallback(ctx context.Context, requestID, id, actor string) (Callback, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Callback{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		UPDATE callbacks SET status = 'pending', next_attempt_at = now(), attempt_count = 0
		WHERE request_id = $1 AND id = $2 AND status = 'failed'
		RETURNING `+callbackColumns, requestID, id)
	if err != nil {
		return Callback{}, fmt.Errorf("retry callback: %w", err)
	}
	c, err := pgx.CollectExactlyOneRow(rows, scanCallback)
	if errors.Is(err, pgx.ErrNoRows) {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM callbacks WHERE request_id = $1 AND id = $2`, requestID, id).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return Callback{}, ErrNotFound
		}
		if err != nil {
			return Callback{}, err
		}
		return Callback{}, &CallbackStateError{Status: status}
	}
	if err != nil {
		return Callback{}, fmt.Errorf("retry callback: %w", err)
	}
	if err := insertAudit(ctx, tx, actor, "callback.retry", "request", requestID, map[string]any{"callback_id": id}); err != nil {
		return Callback{}, err
	}
	return c, tx.Commit(ctx)
}
