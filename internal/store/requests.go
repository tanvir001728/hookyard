package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tanvir001728/hookyard/internal/model"
)

// NewRequest is the input for CreateRequest.
type NewRequest struct {
	Upstream string
	Method   string
	Path     string
	Headers  map[string]string
	// Body is a JSON value, or nil for no body.
	Body json.RawMessage
	// DedupeKey, when set, makes creation idempotent within DedupeWindow.
	DedupeKey    string
	DedupeWindow time.Duration
	Retry        model.RetryPolicy
	Timeout      time.Duration
	Tags         map[string]string
	// DeliverAt, when in the future, creates a scheduled request.
	DeliverAt *time.Time
}

const requestColumns = `id, upstream, method, path, headers, body, dedupe_key, status, attempt_count,
	retry, timeout_ms, tags, deliver_at, next_attempt_at, last_error_code, last_error_message,
	last_status_code, created_at, updated_at, completed_at`

// CreateRequest stores a new request. If in.DedupeKey matches a live key for the
// same upstream, no request is created: the existing one is returned and created
// is false.
func (s *Store) CreateRequest(ctx context.Context, in NewRequest) (req model.Request, created bool, err error) {
	if in.DedupeKey != "" && in.DedupeWindow <= 0 {
		return model.Request{}, false, errors.New("dedupe window must be positive when a dedupe key is set")
	}

	// A concurrent creator can win the key and then roll back, or the winning
	// key can expire between our attempt and the lookup. Both are rare and
	// resolve on retry.
	for range 3 {
		req, created, err = s.createRequestOnce(ctx, in)
		if !errors.Is(err, errDedupeRace) {
			return req, created, err
		}
	}
	return model.Request{}, false, fmt.Errorf("create request: dedupe key %q kept changing, try again", in.DedupeKey)
}

var errDedupeRace = errors.New("dedupe race")

func (s *Store) createRequestOnce(ctx context.Context, in NewRequest) (model.Request, bool, error) {
	headers, err := marshalMap(in.Headers)
	if err != nil {
		return model.Request{}, false, fmt.Errorf("encode headers: %w", err)
	}
	tags, err := marshalMap(in.Tags)
	if err != nil {
		return model.Request{}, false, fmt.Errorf("encode tags: %w", err)
	}
	retry, err := json.Marshal(retryToJSON(in.Retry))
	if err != nil {
		return model.Request{}, false, fmt.Errorf("encode retry policy: %w", err)
	}
	var body any
	if len(in.Body) > 0 && string(in.Body) != "null" {
		body = string(in.Body)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Request{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	req, err := scanRequest(tx.QueryRow(ctx, `
		INSERT INTO requests (id, upstream, method, path, headers, body, dedupe_key, status,
			retry, timeout_ms, tags, deliver_at, next_attempt_at)
		VALUES ($1, $2, $3, $4, $5, $6::json, NULLIF($7, ''),
			CASE WHEN $8::timestamptz > now() THEN 'scheduled' ELSE 'pending' END,
			$9, $10, $11, $8, GREATEST($8::timestamptz, now()))
		RETURNING `+requestColumns,
		model.NewRequestID(), in.Upstream, in.Method, in.Path, headers, body, in.DedupeKey,
		in.DeliverAt, retry, in.Timeout.Milliseconds(), tags,
	))
	if err != nil {
		return model.Request{}, false, fmt.Errorf("insert request: %w", err)
	}

	if in.DedupeKey != "" {
		var winner string
		err := tx.QueryRow(ctx, `
			INSERT INTO dedupe_keys (upstream, dedupe_key, request_id, expires_at)
			VALUES ($1, $2, $3, now() + $4 * interval '1 millisecond')
			ON CONFLICT (upstream, dedupe_key) DO UPDATE
				SET request_id = EXCLUDED.request_id, expires_at = EXCLUDED.expires_at
				WHERE dedupe_keys.expires_at <= now()
			RETURNING request_id`,
			in.Upstream, in.DedupeKey, req.ID, in.DedupeWindow.Milliseconds(),
		).Scan(&winner)
		if errors.Is(err, pgx.ErrNoRows) {
			// A live key exists: discard our insert and return the original.
			_ = tx.Rollback(ctx)
			existing, err := s.requestByDedupeKey(ctx, in.Upstream, in.DedupeKey)
			if errors.Is(err, ErrNotFound) {
				return model.Request{}, false, errDedupeRace
			}
			return existing, false, err
		}
		if err != nil {
			return model.Request{}, false, fmt.Errorf("claim dedupe key: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return model.Request{}, false, fmt.Errorf("commit: %w", err)
	}
	return req, true, nil
}

func (s *Store) requestByDedupeKey(ctx context.Context, upstream, key string) (model.Request, error) {
	req, err := scanRequest(s.pool.QueryRow(ctx, `
		SELECT `+prefixColumns("r.")+`
		FROM dedupe_keys d JOIN requests r ON r.id = d.request_id
		WHERE d.upstream = $1 AND d.dedupe_key = $2 AND d.expires_at > now()`,
		upstream, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Request{}, ErrNotFound
	}
	return req, err
}

// GetRequest returns the request with the given ID, or ErrNotFound.
func (s *Store) GetRequest(ctx context.Context, id string) (model.Request, error) {
	req, err := scanRequest(s.pool.QueryRow(ctx, `SELECT `+requestColumns+` FROM requests WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Request{}, ErrNotFound
	}
	return req, err
}

// RequestFilter selects requests for ListRequests. Zero values match everything.
type RequestFilter struct {
	Upstream      string
	Statuses      []model.Status
	Tags          map[string]string
	DedupeKey     string
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	// Limit is clamped to [1, MaxPageSize]; zero means DefaultPageSize.
	Limit int
	// Cursor is a NextCursor from a previous page.
	Cursor string
}

const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// ErrInvalidCursor is returned when a pagination cursor cannot be decoded.
var ErrInvalidCursor = errors.New("invalid cursor")

// RequestPage is one page of ListRequests results.
type RequestPage struct {
	Requests []model.Request
	// NextCursor is empty on the last page.
	NextCursor string
}

// ListRequests returns requests matching f, newest first.
func (s *Store) ListRequests(ctx context.Context, f RequestFilter) (RequestPage, error) {
	var (
		where []string
		args  []any
	)
	add := func(cond string, arg any) {
		args = append(args, arg)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}

	if f.Upstream != "" {
		add("upstream = ?", f.Upstream)
	}
	if len(f.Statuses) > 0 {
		statuses := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			statuses[i] = string(st)
		}
		add("status = ANY(?)", statuses)
	}
	if len(f.Tags) > 0 {
		tags, err := marshalMap(f.Tags)
		if err != nil {
			return RequestPage{}, err
		}
		add("tags @> ?::jsonb", tags)
	}
	if f.DedupeKey != "" {
		add("dedupe_key = ?", f.DedupeKey)
	}
	if f.CreatedAfter != nil {
		add("created_at >= ?", *f.CreatedAfter)
	}
	if f.CreatedBefore != nil {
		add("created_at < ?", *f.CreatedBefore)
	}
	if f.Cursor != "" {
		lastID, err := decodeCursor(f.Cursor)
		if err != nil {
			return RequestPage{}, err
		}
		add("id < ?", lastID)
	}

	limit := f.Limit
	switch {
	case limit <= 0:
		limit = DefaultPageSize
	case limit > MaxPageSize:
		limit = MaxPageSize
	}

	query := `SELECT ` + requestColumns + ` FROM requests`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	// Fetch one extra row to learn whether another page exists.
	query += fmt.Sprintf(` ORDER BY id DESC LIMIT %d`, limit+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return RequestPage{}, fmt.Errorf("list requests: %w", err)
	}
	reqs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Request, error) {
		return scanRequest(row)
	})
	if err != nil {
		return RequestPage{}, fmt.Errorf("list requests: %w", err)
	}

	page := RequestPage{Requests: reqs}
	if len(reqs) > limit {
		page.Requests = reqs[:limit]
		page.NextCursor = encodeCursor(reqs[limit-1].ID)
	}
	return page, nil
}

// ListAttempts returns every attempt of a request, oldest first. It returns
// ErrNotFound if the request does not exist.
func (s *Store) ListAttempts(ctx context.Context, requestID string) ([]model.Attempt, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT request_id, number, started_at, duration_ms, outcome, status_code, error_code,
			error_message, response_headers, response_body, response_body_truncated, retry_at
		FROM attempts WHERE request_id = $1 ORDER BY number`, requestID)
	if err != nil {
		return nil, fmt.Errorf("list attempts: %w", err)
	}
	attempts, err := pgx.CollectRows(rows, scanAttempt)
	if err != nil {
		return nil, fmt.Errorf("list attempts: %w", err)
	}
	if len(attempts) == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM requests WHERE id = $1)`, requestID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrNotFound
		}
	}
	return attempts, nil
}

func scanRequest(row pgx.Row) (model.Request, error) {
	var (
		r                          model.Request
		headers, tags, retry, body []byte
		dedupeKey, errCode, errMsg *string
		status                     string
		timeoutMS                  int64
	)
	err := row.Scan(&r.ID, &r.Upstream, &r.Method, &r.Path, &headers, &body, &dedupeKey, &status,
		&r.AttemptCount, &retry, &timeoutMS, &tags, &r.DeliverAt, &r.NextAttemptAt, &errCode, &errMsg,
		&r.LastStatusCode, &r.CreatedAt, &r.UpdatedAt, &r.CompletedAt)
	if err != nil {
		return model.Request{}, err
	}

	r.Status = model.Status(status)
	r.Timeout = time.Duration(timeoutMS) * time.Millisecond
	if dedupeKey != nil {
		r.DedupeKey = *dedupeKey
	}
	if body != nil {
		r.Body = json.RawMessage(body)
	}
	if errCode != nil {
		r.LastError = &model.DeliveryError{Code: *errCode, Message: deref(errMsg)}
	}
	if err := json.Unmarshal(headers, &r.Headers); err != nil {
		return model.Request{}, fmt.Errorf("decode headers of %s: %w", r.ID, err)
	}
	if err := json.Unmarshal(tags, &r.Tags); err != nil {
		return model.Request{}, fmt.Errorf("decode tags of %s: %w", r.ID, err)
	}
	var rj retryJSON
	if err := json.Unmarshal(retry, &rj); err != nil {
		return model.Request{}, fmt.Errorf("decode retry policy of %s: %w", r.ID, err)
	}
	r.Retry = rj.model()
	return r, nil
}

func scanAttempt(row pgx.CollectableRow) (model.Attempt, error) {
	var (
		a               model.Attempt
		durationMS      int64
		outcome         string
		errCode, errMsg *string
		respHeaders     []byte
		respBody        *string
		truncated       bool
	)
	err := row.Scan(&a.RequestID, &a.Number, &a.StartedAt, &durationMS, &outcome, &a.StatusCode,
		&errCode, &errMsg, &respHeaders, &respBody, &truncated, &a.RetryAt)
	if err != nil {
		return model.Attempt{}, err
	}
	a.Duration = time.Duration(durationMS) * time.Millisecond
	a.Outcome = model.AttemptOutcome(outcome)
	if errCode != nil {
		a.Error = &model.DeliveryError{Code: *errCode, Message: deref(errMsg)}
	}
	if respHeaders != nil || respBody != nil {
		a.Response = &model.AttemptResponse{Body: deref(respBody), BodyTruncated: truncated}
		if respHeaders != nil {
			if err := json.Unmarshal(respHeaders, &a.Response.Headers); err != nil {
				return model.Attempt{}, fmt.Errorf("decode response headers: %w", err)
			}
		}
	}
	return a, nil
}

// retryJSON is the stored form of a retry policy, with durations in milliseconds.
type retryJSON struct {
	Preset            string  `json:"preset,omitempty"`
	MaxAttempts       int     `json:"max_attempts"`
	InitialIntervalMS int64   `json:"initial_interval_ms"`
	MaxIntervalMS     int64   `json:"max_interval_ms"`
	Multiplier        float64 `json:"multiplier"`
	MaxAgeMS          int64   `json:"max_age_ms"`
}

func retryToJSON(p model.RetryPolicy) retryJSON {
	return retryJSON{
		Preset:            p.Preset,
		MaxAttempts:       p.MaxAttempts,
		InitialIntervalMS: p.InitialInterval.Milliseconds(),
		MaxIntervalMS:     p.MaxInterval.Milliseconds(),
		Multiplier:        p.Multiplier,
		MaxAgeMS:          p.MaxAge.Milliseconds(),
	}
}

func (r retryJSON) model() model.RetryPolicy {
	return model.RetryPolicy{
		Preset:          r.Preset,
		MaxAttempts:     r.MaxAttempts,
		InitialInterval: time.Duration(r.InitialIntervalMS) * time.Millisecond,
		MaxInterval:     time.Duration(r.MaxIntervalMS) * time.Millisecond,
		Multiplier:      r.Multiplier,
		MaxAge:          time.Duration(r.MaxAgeMS) * time.Millisecond,
	}
}

func marshalMap(m map[string]string) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

func encodeCursor(lastID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(lastID))
}

func decodeCursor(c string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || !model.IsRequestID(string(b)) {
		return "", ErrInvalidCursor
	}
	return string(b), nil
}

func prefixColumns(prefix string) string {
	cols := strings.Split(requestColumns, ",")
	for i, c := range cols {
		cols[i] = prefix + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
