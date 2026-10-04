package worker

import (
	"fmt"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/retry"
)

// ErrCodeMaxAgeExceeded marks requests given up because their retry budget
// (max_age) ran out.
const ErrCodeMaxAgeExceeded = "max_age_exceeded"

// Decision is what happens to a request after an attempt.
type Decision struct {
	Outcome model.AttemptOutcome
	// Status is succeeded, failed (retry at RetryAt) or dead.
	Status  model.Status
	RetryAt *time.Time
	// LastError, if set, replaces the attempt's error as the request's
	// last_error, for example to explain that max_age was exceeded.
	LastError *model.DeliveryError
}

// Decider classifies an attempt and schedules the next one.
type Decider func(req model.Request, attempt int, res AttemptResult, now time.Time) Decision

// PolicyDecider applies each request's retry policy: exponential backoff with
// full jitter, Retry-After on 429 and 503, permanent client errors, and
// max_age. rnd returns values in [0, 1); nil means math/rand.
func PolicyDecider(rnd func() float64) Decider {
	return func(req model.Request, attempt int, res AttemptResult, now time.Time) Decision {
		outcome := model.OutcomeSuccess
		switch {
		case res.Outcome != "":
			outcome = res.Outcome // decided by a classification rule
		case res.StatusCode != 0:
			outcome = retry.Classify(res.StatusCode)
		case res.Error != nil:
			// Timeouts, connection errors and internal problems may be transient.
			outcome = model.OutcomeRetryableFailure
		}

		// Count attempts since the last replay, which granted a fresh budget.
		n := attempt - req.RetryAttemptBase

		switch {
		case outcome == model.OutcomeSuccess:
			return Decision{Outcome: outcome, Status: model.StatusSucceeded}
		case res.Ambiguous && !res.SafeToRepeat:
			// The upstream may have processed it: repeating could charge a
			// customer twice. A person or the app has to settle it.
			return Decision{Outcome: model.OutcomeUnknown, Status: model.StatusUnknown, LastError: &model.DeliveryError{
				Code: res.Error.Code,
				Message: res.Error.Message + "; the request was sent but no response arrived, so it may have been processed. " +
					"Not retried to avoid a duplicate (add an Idempotency-Key header or set on_timeout: retry to retry these). Resolve or replay it.",
			}}
		case outcome == model.OutcomePermanentFailure:
			return Decision{Outcome: outcome, Status: model.StatusDead}
		case n >= req.Retry.MaxAttempts:
			return Decision{Outcome: outcome, Status: model.StatusDead}
		}

		delay := retry.Backoff(req.Retry, n, rnd)
		if retry.HonorsRetryAfter(res.StatusCode) && res.Headers != nil {
			if d, ok := retry.ParseRetryAfter(res.Headers.Get("Retry-After"), now); ok {
				delay = d
			}
		}
		next := now.Add(delay)

		if deadline := req.RetryWindowStart.Add(req.Retry.MaxAge); next.After(deadline) {
			return Decision{Outcome: outcome, Status: model.StatusDead, LastError: &model.DeliveryError{
				Code: ErrCodeMaxAgeExceeded,
				Message: fmt.Sprintf("gave up after %d attempt(s): the next attempt would be after max_age (%s) ran out; last error: %s",
					n, model.FormatDuration(req.Retry.MaxAge), errorMessage(res)),
			}}
		}
		return Decision{Outcome: outcome, Status: model.StatusFailed, RetryAt: &next}
	}
}

func errorMessage(res AttemptResult) string {
	if res.Error != nil {
		return res.Error.Message
	}
	return fmt.Sprintf("status %d", res.StatusCode)
}
