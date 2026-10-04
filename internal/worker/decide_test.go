package worker

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
)

func TestPolicyDecider(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	policy := model.RetryPolicy{MaxAttempts: 5, InitialInterval: time.Second, MaxInterval: time.Minute, Multiplier: 2, MaxAge: time.Hour}
	req := model.Request{Retry: policy, RetryWindowStart: now.Add(-10 * time.Minute)}
	half := func() float64 { return 0.5 }
	decide := PolicyDecider(half)

	httpErr := func(status int, headers ...string) AttemptResult {
		h := http.Header{}
		for i := 0; i+1 < len(headers); i += 2 {
			h.Set(headers[i], headers[i+1])
		}
		return AttemptResult{StatusCode: status, Headers: h, Error: &model.DeliveryError{Code: ErrCodeHTTPStatus, Message: "failed"}}
	}

	tests := []struct {
		name        string
		req         model.Request
		attempt     int
		res         AttemptResult
		wantStatus  model.Status
		wantOutcome model.AttemptOutcome
		wantDelay   time.Duration // for failed
		wantErrCode string        // LastError override
	}{
		{"success", req, 1, AttemptResult{StatusCode: 201}, model.StatusSucceeded, model.OutcomeSuccess, 0, ""},
		{"503 backs off", req, 3, httpErr(503), model.StatusFailed, model.OutcomeRetryableFailure, 2 * time.Second, ""}, // half of 4s
		{"429 honors Retry-After", req, 1, httpErr(429, "Retry-After", "90"), model.StatusFailed, model.OutcomeRetryableFailure, 90 * time.Second, ""},
		{"500 ignores Retry-After", req, 1, httpErr(500, "Retry-After", "90"), model.StatusFailed, model.OutcomeRetryableFailure, 500 * time.Millisecond, ""},
		{"timeout retries", req, 1, AttemptResult{Error: &model.DeliveryError{Code: ErrCodeTimeout}}, model.StatusFailed, model.OutcomeRetryableFailure, 500 * time.Millisecond, ""},
		{"400 is permanent", req, 1, httpErr(400), model.StatusDead, model.OutcomePermanentFailure, 0, ""},
		{"redirect is permanent", req, 1, httpErr(302), model.StatusDead, model.OutcomePermanentFailure, 0, ""},
		{"max attempts", req, 5, httpErr(503), model.StatusDead, model.OutcomeRetryableFailure, 0, ""},
		{
			"replay grants a fresh budget",
			model.Request{Retry: policy, RetryWindowStart: now, RetryAttemptBase: 5},
			7, httpErr(503), model.StatusFailed, model.OutcomeRetryableFailure, time.Second, // 2nd attempt since replay: half of 2s
			"",
		},
		{"Retry-After beyond max_age", req, 1, httpErr(503, "Retry-After", "7200"), model.StatusDead, model.OutcomeRetryableFailure, 0, ErrCodeMaxAgeExceeded},
		{
			"window nearly used up",
			model.Request{Retry: policy, RetryWindowStart: now.Add(-time.Hour + 100*time.Millisecond)},
			1, httpErr(503), model.StatusDead, model.OutcomeRetryableFailure, 0, ErrCodeMaxAgeExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := decide(tt.req, tt.attempt, tt.res, now)
			if d.Status != tt.wantStatus || d.Outcome != tt.wantOutcome {
				t.Fatalf("got %s/%s, want %s/%s", d.Status, d.Outcome, tt.wantStatus, tt.wantOutcome)
			}
			if tt.wantStatus == model.StatusFailed {
				if d.RetryAt == nil || d.RetryAt.Sub(now) != tt.wantDelay {
					t.Errorf("retry delay = %v, want %s", d.RetryAt, tt.wantDelay)
				}
			} else if d.RetryAt != nil {
				t.Errorf("final decision must not schedule a retry: %v", d.RetryAt)
			}
			switch {
			case tt.wantErrCode == "" && d.LastError != nil:
				t.Errorf("unexpected LastError %+v", d.LastError)
			case tt.wantErrCode != "" && (d.LastError == nil || d.LastError.Code != tt.wantErrCode):
				t.Errorf("LastError = %+v, want code %s", d.LastError, tt.wantErrCode)
			}
		})
	}
}

func TestPolicyDeciderAmbiguous(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	req := model.Request{Retry: model.RetryPolicy{MaxAttempts: 5, InitialInterval: time.Second, MaxInterval: time.Minute, Multiplier: 2, MaxAge: time.Hour}, RetryWindowStart: now}
	decide := PolicyDecider(func() float64 { return 0.5 })
	timeout := &model.DeliveryError{Code: ErrCodeTimeout, Message: "no response within 5s"}

	tests := []struct {
		name string
		res  AttemptResult
		want model.Status
	}{
		{"sent, no response, unsafe", AttemptResult{Error: timeout, Ambiguous: true}, model.StatusUnknown},
		{"sent, no response, safe to repeat", AttemptResult{Error: timeout, Ambiguous: true, SafeToRepeat: true}, model.StatusFailed},
		{"never sent", AttemptResult{Error: timeout}, model.StatusFailed},
	}
	for _, tt := range tests {
		d := decide(req, 1, tt.res, now)
		if d.Status != tt.want {
			t.Errorf("%s: status %s, want %s", tt.name, d.Status, tt.want)
		}
		if tt.want == model.StatusUnknown && (d.Outcome != model.OutcomeUnknown || d.LastError == nil || !strings.Contains(d.LastError.Message, "may have been processed")) {
			t.Errorf("%s: decision %+v", tt.name, d)
		}
	}
}
