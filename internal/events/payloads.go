package events

import (
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
)

// AttemptData is the data of an attempt event.
type AttemptData struct {
	RequestID  string     `json:"request_id"`
	Upstream   string     `json:"upstream"`
	Method     string     `json:"method"`
	Path       string     `json:"path"`
	Attempt    int        `json:"attempt"`
	Outcome    string     `json:"outcome"`
	StatusCode *int       `json:"status_code"`
	DurationMS int64      `json:"duration_ms"`
	Error      *ErrorData `json:"error"`
	// Status is the request's status after the attempt.
	Status  string     `json:"status"`
	RetryAt *time.Time `json:"retry_at"`
}

// ErrorData describes why an attempt failed.
type ErrorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RequestData is the data of a request event.
type RequestData struct {
	RequestID string `json:"request_id"`
	Upstream  string `json:"upstream"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    string `json:"status"`
	// Action is enqueued, canceled, resolved or replayed.
	Action string `json:"action"`
	Actor  string `json:"actor"`
}

// UpstreamData is the data of an upstream event.
type UpstreamData struct {
	Upstream string `json:"upstream"`
	// Kind is breaker_open, breaker_half_open, breaker_closed, paused or
	// resumed, as in the upstream's history.
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	Actor  string `json:"actor"`
}

// Attempt builds the event for a recorded attempt of req that left it in
// status.
func Attempt(req model.Request, a model.Attempt, status model.Status, at time.Time) Event {
	d := AttemptData{
		RequestID: req.ID, Upstream: req.Upstream, Method: req.Method, Path: req.Path,
		Attempt: a.Number, Outcome: string(a.Outcome), StatusCode: a.StatusCode,
		DurationMS: a.Duration.Milliseconds(), Status: string(status), RetryAt: a.RetryAt,
	}
	if a.Error != nil {
		d.Error = &ErrorData{Code: a.Error.Code, Message: a.Error.Message}
	}
	return Event{Type: TypeAttempt, At: at, Upstream: req.Upstream, Status: string(status), Data: d}
}

// Request builds the event for a state change of req made by actor.
func Request(req model.Request, action, actor string) Event {
	return Event{Type: TypeRequest, Upstream: req.Upstream, Status: string(req.Status), Data: RequestData{
		RequestID: req.ID, Upstream: req.Upstream, Method: req.Method, Path: req.Path,
		Status: string(req.Status), Action: action, Actor: actor,
	}}
}

// Upstream builds the event for an upstream transition.
func Upstream(upstream, kind, reason, actor string, at time.Time) Event {
	return Event{Type: TypeUpstream, At: at, Upstream: upstream, Data: UpstreamData{Upstream: upstream, Kind: kind, Reason: reason, Actor: actor}}
}
