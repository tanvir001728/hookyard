package retry

import (
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
)

// Backoff returns the delay before the attempt following attempt (1-based),
// using exponential backoff with full jitter:
//
//	delay = random(0, min(max_interval, initial_interval * multiplier^(attempt-1)))
//
// Full jitter spreads retries from many clients evenly over the window, which
// avoids synchronized retry storms against a recovering upstream. rnd returns a
// value in [0, 1); nil means math/rand.
func Backoff(p model.RetryPolicy, attempt int, rnd func() float64) time.Duration {
	if rnd == nil {
		rnd = rand.Float64
	}
	return time.Duration(rnd() * float64(Ceiling(p, attempt)))
}

// Ceiling is the upper bound of the jittered delay after attempt.
func Ceiling(p model.RetryPolicy, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	c := float64(p.InitialInterval) * math.Pow(p.Multiplier, float64(attempt-1))
	if c > float64(p.MaxInterval) || math.IsInf(c, 0) || math.IsNaN(c) {
		return p.MaxInterval
	}
	return time.Duration(c)
}

// ParseRetryAfter parses a Retry-After header value: either delay seconds or
// an HTTP date. It reports false if the value is missing or invalid.
func ParseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs < 0 {
			return 0, false
		}
		// Clamp absurd values instead of overflowing time.Duration.
		const maxSecs = int64(365 * 24 * time.Hour / time.Second)
		return time.Duration(min(secs, maxSecs)) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

// Classify decides whether an HTTP status code is a success, a failure worth
// retrying, or a permanent failure.
//
//   - 2xx: success
//   - 408, 425, 429 and 5xx: retryable (the upstream may recover)
//   - 3xx: permanent (redirects are not followed; fix the base URL)
//   - other 4xx: permanent (retrying an invalid request cannot succeed)
func Classify(status int) model.AttemptOutcome {
	switch {
	case status >= 200 && status <= 299:
		return model.OutcomeSuccess
	case status == http.StatusRequestTimeout, status == http.StatusTooEarly, status == http.StatusTooManyRequests, status >= 500:
		return model.OutcomeRetryableFailure
	default:
		return model.OutcomePermanentFailure
	}
}

// HonorsRetryAfter reports whether a Retry-After header on a response with
// this status should set the retry delay.
func HonorsRetryAfter(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable
}
