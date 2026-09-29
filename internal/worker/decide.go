package worker

import (
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
)

// Decision is what happens to a request after an attempt.
type Decision struct {
	Outcome model.AttemptOutcome
	// Status is succeeded, failed (retry at RetryAt) or dead.
	Status  model.Status
	RetryAt *time.Time
}

// Decider classifies an attempt and schedules the next one.
type Decider func(req model.Request, attempt int, res AttemptResult, now time.Time) Decision

// decideFixedInterval treats 2xx as success and retries everything else after
// the policy's initial interval until max_attempts is reached.
//
// TODO(#7): replace with exponential backoff and jitter, Retry-After,
// permanent client errors and max_age.
func decideFixedInterval(req model.Request, attempt int, res AttemptResult, now time.Time) Decision {
	if res.Error == nil {
		return Decision{Outcome: model.OutcomeSuccess, Status: model.StatusSucceeded}
	}
	if attempt >= req.Retry.MaxAttempts {
		return Decision{Outcome: model.OutcomeRetryableFailure, Status: model.StatusDead}
	}
	next := now.Add(req.Retry.InitialInterval)
	return Decision{Outcome: model.OutcomeRetryableFailure, Status: model.StatusFailed, RetryAt: &next}
}
