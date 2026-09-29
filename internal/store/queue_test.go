package store_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
	"github.com/tanvir001728/hookyard/internal/store/storetest"
)

func TestClaimDue(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	future := time.Now().Add(time.Hour)
	scheduled, _, err := s.CreateRequest(ctx, newRequest(func(in *store.NewRequest) { in.DeliverAt = &future }))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, _, err := s.CreateRequest(ctx, newRequest()); err != nil {
			t.Fatal(err)
		}
	}

	claims, err := s.ClaimDue(ctx, 2, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 2 {
		t.Fatalf("claimed %d, want limit 2", len(claims))
	}
	for _, c := range claims {
		if c.Request.Status != model.StatusInFlight {
			t.Errorf("claimed request status = %s", c.Request.Status)
		}
		// Lease = timeout (15s) + margin (30s).
		if until := time.Until(c.LeaseExpiresAt); until < 40*time.Second || until > 50*time.Second {
			t.Errorf("lease expires in %s, want about 45s", until)
		}
		if c.Request.ID == scheduled.ID {
			t.Error("a request scheduled in the future must not be claimed")
		}
	}

	rest, err := s.ClaimDue(ctx, 10, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 {
		t.Fatalf("second claim got %d, want the 1 remaining due request", len(rest))
	}
}

func TestClaimDueConcurrentNeverOverlaps(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	const total = 40
	for range total {
		if _, _, err := s.CreateRequest(ctx, newRequest()); err != nil {
			t.Fatal(err)
		}
	}

	var (
		mu   sync.Mutex
		seen = map[string]int{}
		wg   sync.WaitGroup
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				claims, err := s.ClaimDue(ctx, 3, time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				if len(claims) == 0 {
					return
				}
				mu.Lock()
				for _, c := range claims {
					seen[c.Request.ID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) != total {
		t.Errorf("claimed %d distinct requests, want %d", len(seen), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("request %s claimed %d times", id, n)
		}
	}
}

func TestRecordAttempt(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	if _, _, err := s.CreateRequest(ctx, newRequest()); err != nil {
		t.Fatal(err)
	}
	claims, err := s.ClaimDue(ctx, 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim: %v %v", claims, err)
	}
	c := claims[0]

	// First attempt fails with a retry scheduled.
	retryAt := time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond)
	status := 503
	err = s.RecordAttempt(ctx, store.AttemptRecord{
		Attempt: model.Attempt{
			RequestID: c.Request.ID, Number: 1, StartedAt: time.Now(), Duration: 120 * time.Millisecond,
			Outcome: model.OutcomeRetryableFailure, StatusCode: &status,
			Error:    &model.DeliveryError{Code: "http_status", Message: "upstream responded with 503"},
			Response: &model.AttemptResponse{Headers: map[string]string{"Retry-After": "60"}, Body: "busy"},
			RetryAt:  &retryAt,
		},
		Status: model.StatusFailed, NextAttemptAt: &retryAt, LeaseExpiresAt: c.LeaseExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, err := s.GetRequest(ctx, c.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != model.StatusFailed || req.AttemptCount != 1 || req.NextAttemptAt == nil || !req.NextAttemptAt.Equal(retryAt) ||
		req.LastError == nil || req.LastError.Code != "http_status" || *req.LastStatusCode != 503 || req.CompletedAt != nil {
		t.Errorf("after failed attempt: %+v", req)
	}

	// Not due yet, so it can't be claimed again.
	if again, _ := s.ClaimDue(ctx, 1, time.Minute); len(again) != 0 {
		t.Error("a request with a future retry must not be claimed")
	}

	attempts, err := s.ListAttempts(ctx, c.Request.ID)
	if err != nil || len(attempts) != 1 || attempts[0].Response.Headers["Retry-After"] != "60" {
		t.Fatalf("attempts = %+v, err = %v", attempts, err)
	}
}

func TestRecordAttemptFencing(t *testing.T) {
	t.Parallel()
	s, dbURL := storetest.NewWithURL(t)
	ctx := t.Context()

	if _, _, err := s.CreateRequest(ctx, newRequest()); err != nil {
		t.Fatal(err)
	}
	stale, err := s.ClaimDue(ctx, 1, time.Minute)
	if err != nil || len(stale) != 1 {
		t.Fatal(err)
	}

	// Simulate the first worker stalling past its lease.
	expireLeases(t, dbURL)
	if n, err := s.RecoverExpiredLeases(ctx); err != nil || n != 1 {
		t.Fatalf("recovered %d, err %v", n, err)
	}
	recovered, err := s.GetRequest(ctx, stale[0].Request.ID)
	if err != nil || recovered.Status != model.StatusPending || recovered.LastError == nil {
		t.Fatalf("after recovery: %+v %v", recovered, err)
	}

	fresh, err := s.ClaimDue(ctx, 1, time.Minute)
	if err != nil || len(fresh) != 1 {
		t.Fatalf("reclaim: %v %v", fresh, err)
	}

	succeed := func(c store.Claim) error {
		code := 200
		return s.RecordAttempt(ctx, store.AttemptRecord{
			Attempt: model.Attempt{RequestID: c.Request.ID, Number: 1, StartedAt: time.Now(), Outcome: model.OutcomeSuccess, StatusCode: &code},
			Status:  model.StatusSucceeded, LeaseExpiresAt: c.LeaseExpiresAt,
		})
	}

	// The stalled worker finishes late: its write must be rejected.
	if err := succeed(stale[0]); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("stale worker: err = %v, want ErrLeaseLost", err)
	}
	if err := succeed(fresh[0]); err != nil {
		t.Fatalf("current worker: %v", err)
	}

	final, _ := s.GetRequest(ctx, fresh[0].Request.ID)
	if final.Status != model.StatusSucceeded || final.CompletedAt == nil {
		t.Errorf("final: %+v", final)
	}
	attempts, _ := s.ListAttempts(ctx, final.ID)
	if len(attempts) != 1 {
		t.Errorf("got %d attempts, want only the current worker's", len(attempts))
	}
}

func TestRecordAttemptRejectsInvalidStatus(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	err := s.RecordAttempt(t.Context(), store.AttemptRecord{Status: model.StatusPending})
	if err == nil {
		t.Fatal("expected error for a non-terminal, non-failed status")
	}
	err = s.RecordAttempt(t.Context(), store.AttemptRecord{Status: model.StatusFailed})
	if err == nil {
		t.Fatal("expected error for failed without NextAttemptAt")
	}
}
