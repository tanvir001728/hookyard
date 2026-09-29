package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tanvir001728/hookyard/internal/model"
)

// ErrLeaseLost is returned by RecordAttempt when the worker no longer owns the
// request, because its lease expired and the request was recovered.
var ErrLeaseLost = errors.New("lease lost")

// Claim is a request a worker owns until LeaseExpiresAt.
type Claim struct {
	Request model.Request
	// LeaseExpiresAt also acts as a fencing token for RecordAttempt.
	LeaseExpiresAt time.Time
}

// ClaimDue marks up to limit due requests as in_flight and returns them. Each
// lease lasts the request's timeout plus leaseMargin. Concurrent callers never
// receive the same request.
func (s *Store) ClaimDue(ctx context.Context, limit int, leaseMargin time.Duration) ([]Claim, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE requests SET
			status = 'in_flight',
			lease_expires_at = now() + (timeout_ms + $2) * interval '1 millisecond',
			updated_at = now()
		WHERE id IN (
			SELECT id FROM requests
			WHERE status IN ('scheduled', 'pending', 'failed') AND next_attempt_at <= now()
			ORDER BY next_attempt_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+requestColumns+`, lease_expires_at`,
		limit, leaseMargin.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("claim due requests: %w", err)
	}
	claims, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Claim, error) {
		var c Claim
		req, err := scanRequest(row, &c.LeaseExpiresAt)
		c.Request = req
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("claim due requests: %w", err)
	}
	return claims, nil
}

// NextDueAt returns when the earliest waiting request becomes due, or nil if
// nothing is waiting.
func (s *Store) NextDueAt(ctx context.Context) (*time.Time, error) {
	var next *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT next_attempt_at FROM requests
		WHERE status IN ('scheduled', 'pending', 'failed')
		ORDER BY next_attempt_at
		LIMIT 1`).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("next due request: %w", err)
	}
	return next, nil
}

// RecoverExpiredLeases returns in_flight requests whose lease expired, because
// their worker crashed or stalled, to pending so they are delivered again.
func (s *Store) RecoverExpiredLeases(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE requests SET
			status = 'pending',
			next_attempt_at = now(),
			lease_expires_at = NULL,
			updated_at = now(),
			last_error_code = 'internal',
			last_error_message = 'delivery was interrupted before its outcome was recorded; retrying'
		WHERE status = 'in_flight' AND lease_expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("recover expired leases: %w", err)
	}
	return tag.RowsAffected(), nil
}

// AttemptRecord is the outcome of one delivery attempt and the resulting state
// of its request.
type AttemptRecord struct {
	Attempt model.Attempt
	// Status is the request's new status: succeeded, failed (retry scheduled)
	// or dead.
	Status model.Status
	// NextAttemptAt is required when Status is failed.
	NextAttemptAt *time.Time
	// LeaseExpiresAt must be the value from the Claim.
	LeaseExpiresAt time.Time
	// LastError, if set, is stored as the request's last error instead of the
	// attempt's error (for example "max_age_exceeded").
	LastError *model.DeliveryError
}

// RecordAttempt stores an attempt and updates its request in one transaction.
// It returns ErrLeaseLost, and changes nothing, if the lease no longer matches.
func (s *Store) RecordAttempt(ctx context.Context, rec AttemptRecord) error {
	a := rec.Attempt
	switch rec.Status {
	case model.StatusSucceeded, model.StatusDead:
	case model.StatusFailed:
		if rec.NextAttemptAt == nil {
			return errors.New("record attempt: a failed request needs NextAttemptAt")
		}
	default:
		return fmt.Errorf("record attempt: invalid resulting status %q", rec.Status)
	}

	var errCode, errMsg *string
	if a.Error != nil {
		errCode, errMsg = &a.Error.Code, &a.Error.Message
	}
	reqErrCode, reqErrMsg := errCode, errMsg
	if rec.LastError != nil {
		reqErrCode, reqErrMsg = &rec.LastError.Code, &rec.LastError.Message
	}
	var respHeaders []byte
	var respBody *string
	truncated := false
	if a.Response != nil {
		var err error
		if respHeaders, err = marshalMap(a.Response.Headers); err != nil {
			return fmt.Errorf("encode response headers: %w", err)
		}
		respBody, truncated = &a.Response.Body, a.Response.BodyTruncated
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE requests SET
			status = $3,
			attempt_count = $4,
			next_attempt_at = $5,
			lease_expires_at = NULL,
			last_error_code = $6,
			last_error_message = $7,
			last_status_code = $8,
			updated_at = now(),
			completed_at = CASE WHEN $3 IN ('succeeded', 'dead') THEN now() END
		WHERE id = $1 AND status = 'in_flight' AND lease_expires_at = $2`,
		a.RequestID, rec.LeaseExpiresAt, string(rec.Status), a.Number, rec.NextAttemptAt,
		reqErrCode, reqErrMsg, a.StatusCode)
	if err != nil {
		return fmt.Errorf("update request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO attempts (request_id, number, started_at, duration_ms, outcome, status_code,
			error_code, error_message, response_headers, response_body, response_body_truncated, retry_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		a.RequestID, a.Number, a.StartedAt, a.Duration.Milliseconds(), string(a.Outcome), a.StatusCode,
		errCode, errMsg, nullJSON(respHeaders), respBody, truncated, a.RetryAt)
	if err != nil {
		return fmt.Errorf("insert attempt: %w", err)
	}
	return tx.Commit(ctx)
}

func nullJSON(b []byte) any {
	if b == nil {
		return nil
	}
	return json.RawMessage(b)
}
