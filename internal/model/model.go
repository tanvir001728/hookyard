// Package model defines the core data types shared across Hookyard's packages.
// It contains plain data and small helpers only, with no I/O.
package model

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// RequestIDPrefix prefixes every request ID.
const RequestIDPrefix = "req_"

// NewRequestID returns a new, time-sortable request ID such as
// "req_01J9ZK3Q4W5E6R7T8Y9U0I1O2P".
func NewRequestID() string {
	return RequestIDPrefix + ulid.Make().String()
}

// IsRequestID reports whether s looks like a request ID.
func IsRequestID(s string) bool {
	rest, ok := strings.CutPrefix(s, RequestIDPrefix)
	if !ok {
		return false
	}
	_, err := ulid.ParseStrict(rest)
	return err == nil
}

// Status is the lifecycle state of a request. See docs/design.md.
type Status string

const (
	StatusScheduled Status = "scheduled"
	StatusPending   Status = "pending"
	StatusInFlight  Status = "in_flight"
	StatusFailed    Status = "failed"
	StatusSucceeded Status = "succeeded"
	StatusDead      Status = "dead"
	StatusUnknown   Status = "unknown"
	StatusCanceled  Status = "canceled"
)

// Statuses lists every status in lifecycle order.
var Statuses = []Status{
	StatusScheduled, StatusPending, StatusInFlight, StatusFailed,
	StatusSucceeded, StatusDead, StatusUnknown, StatusCanceled,
}

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	for _, v := range Statuses {
		if s == v {
			return true
		}
	}
	return false
}

// Final reports whether no further delivery attempts will happen on their own.
func (s Status) Final() bool {
	switch s {
	case StatusSucceeded, StatusDead, StatusUnknown, StatusCanceled:
		return true
	default:
		return false
	}
}

// RetryPolicy is a fully resolved retry policy.
type RetryPolicy struct {
	// Preset is the named preset the policy was derived from, if any.
	Preset string
	// MaxAttempts is the total number of attempts, including the first.
	MaxAttempts     int
	InitialInterval time.Duration
	MaxInterval     time.Duration
	Multiplier      float64
	// MaxAge bounds the time from creation until the request is given up.
	MaxAge time.Duration
}

// DeliveryError describes why an attempt or request failed.
type DeliveryError struct {
	Code    string
	Message string
}

// Request is one outbound call that an application asked Hookyard to deliver.
type Request struct {
	ID       string
	Upstream string
	Method   string
	// Path is appended to the upstream's base URL and may include a query string.
	Path    string
	Headers map[string]string
	// Body is the JSON value as enqueued, or nil when there is no body.
	Body      json.RawMessage
	DedupeKey string
	Status    Status
	// AttemptCount is the number of attempts made so far.
	AttemptCount int
	Retry        RetryPolicy
	Timeout      time.Duration
	Tags         map[string]string

	DeliverAt     *time.Time
	NextAttemptAt *time.Time
	// RetryWindowStart anchors Retry.MaxAge: when the request first became
	// due, or when it was last replayed.
	RetryWindowStart time.Time
	// RetryAttemptBase is AttemptCount at the last replay. Retry.MaxAttempts
	// applies to attempts made after it.
	RetryAttemptBase int
	LastError        *DeliveryError
	LastStatusCode   *int

	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
}

// AttemptOutcome is how Hookyard classified a delivery attempt.
type AttemptOutcome string

const (
	OutcomeSuccess          AttemptOutcome = "success"
	OutcomeRetryableFailure AttemptOutcome = "retryable_failure"
	OutcomePermanentFailure AttemptOutcome = "permanent_failure"
)

// AttemptResponse is the (possibly truncated) upstream response to an attempt.
type AttemptResponse struct {
	Headers       map[string]string
	Body          string
	BodyTruncated bool
}

// Attempt is one HTTP try of a request.
type Attempt struct {
	RequestID  string
	Number     int
	StartedAt  time.Time
	Duration   time.Duration
	Outcome    AttemptOutcome
	StatusCode *int
	Error      *DeliveryError
	Response   *AttemptResponse
	RetryAt    *time.Time
}
