package retry

import (
	"net/http"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
)

func TestCeiling(t *testing.T) {
	p := model.RetryPolicy{InitialInterval: time.Second, MaxInterval: 30 * time.Second, Multiplier: 2}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := Ceiling(p, i+1); got != w {
			t.Errorf("attempt %d: ceiling %s, want %s", i+1, got, w)
		}
	}
	if got := Ceiling(p, 0); got != time.Second {
		t.Errorf("attempt 0 should be treated as 1, got %s", got)
	}
	if got := Ceiling(p, 10_000); got != 30*time.Second {
		t.Errorf("huge attempt numbers must not overflow: %s", got)
	}
}

func TestBackoffFullJitter(t *testing.T) {
	p := model.RetryPolicy{InitialInterval: time.Second, MaxInterval: time.Minute, Multiplier: 2}
	if got := Backoff(p, 3, func() float64 { return 0 }); got != 0 {
		t.Errorf("lowest roll: %s, want 0", got)
	}
	if got := Backoff(p, 3, func() float64 { return 0.5 }); got != 2*time.Second {
		t.Errorf("middle roll: %s, want 2s (half of the 4s ceiling)", got)
	}
	for range 1000 {
		if d := Backoff(p, 4, nil); d < 0 || d >= 8*time.Second {
			t.Fatalf("delay %s outside [0, 8s)", d)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"120", 2 * time.Minute, true},
		{" 0 ", 0, true},
		{"Tue, 29 Sep 2026 12:00:30 GMT", 30 * time.Second, true},
		{"Tue, 29 Sep 2026 11:00:00 GMT", 0, true}, // in the past: retry now
		{"99999999999999", 365 * 24 * time.Hour, true},
		{"", 0, false},
		{"-5", 0, false},
		{"soon", 0, false},
	}
	for _, tt := range tests {
		got, ok := ParseRetryAfter(tt.in, now)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ParseRetryAfter(%q) = %s, %v; want %s, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestClassify(t *testing.T) {
	tests := map[int]model.AttemptOutcome{
		200: model.OutcomeSuccess, 201: model.OutcomeSuccess, 204: model.OutcomeSuccess,
		301: model.OutcomePermanentFailure, 302: model.OutcomePermanentFailure,
		400: model.OutcomePermanentFailure, 401: model.OutcomePermanentFailure, 404: model.OutcomePermanentFailure, 422: model.OutcomePermanentFailure,
		408: model.OutcomeRetryableFailure, 425: model.OutcomeRetryableFailure, 429: model.OutcomeRetryableFailure,
		500: model.OutcomeRetryableFailure, 502: model.OutcomeRetryableFailure, 503: model.OutcomeRetryableFailure, 504: model.OutcomeRetryableFailure,
	}
	for status, want := range tests {
		if got := Classify(status); got != want {
			t.Errorf("Classify(%d) = %s, want %s", status, got, want)
		}
	}
	if !HonorsRetryAfter(http.StatusTooManyRequests) || !HonorsRetryAfter(http.StatusServiceUnavailable) || HonorsRetryAfter(http.StatusInternalServerError) {
		t.Error("HonorsRetryAfter is wrong")
	}
}
