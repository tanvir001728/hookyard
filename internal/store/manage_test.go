package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
	"github.com/tanvir001728/hookyard/internal/store/storetest"
)

// finish drives a newly created request to a final state through the queue,
// the same way a worker would.
func finish(t *testing.T, s *store.Store, in store.NewRequest, status model.Status, code int, errCode string) model.Request {
	t.Helper()
	ctx := context.Background()
	req, _, err := s.CreateRequest(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.ClaimDue(ctx, 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range claims {
		if c.Request.ID != req.ID {
			continue
		}
		a := model.Attempt{RequestID: req.ID, Number: 1, StartedAt: time.Now(), StatusCode: &code, Outcome: model.OutcomeSuccess}
		if status != model.StatusSucceeded {
			a.Outcome = model.OutcomePermanentFailure
			a.Error = &model.DeliveryError{Code: errCode, Message: "failed"}
		}
		if err := s.RecordAttempt(ctx, store.AttemptRecord{Attempt: a, Status: status, LeaseExpiresAt: c.LeaseExpiresAt}); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetRequest(ctx, req.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	t.Fatalf("request %s was not claimed", req.ID)
	return model.Request{}
}

func TestReplayRequest(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	dead := finish(t, s, newRequest(), model.StatusDead, 400, "http_status")
	replayed, err := s.ReplayRequest(ctx, dead.ID, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Status != model.StatusPending || replayed.CompletedAt != nil || replayed.RetryAttemptBase != 1 ||
		replayed.AttemptCount != 1 || !replayed.RetryWindowStart.After(dead.RetryWindowStart) {
		t.Errorf("replayed request: %+v", replayed)
	}

	// Replaying again while pending is not allowed.
	_, err = s.ReplayRequest(ctx, dead.ID, "orders")
	var stateErr *store.InvalidStateError
	if !errors.As(err, &stateErr) || stateErr.Status != model.StatusPending {
		t.Fatalf("err = %v, want InvalidStateError for pending", err)
	}
	if msg := err.Error(); msg != "cannot replay a request that is pending (allowed: dead, succeeded, canceled, unknown)" {
		t.Errorf("message = %q", msg)
	}

	if _, err := s.ReplayRequest(ctx, model.NewRequestID(), "orders"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}

	audit, err := s.ListAudit(ctx, "request", dead.ID, 10)
	if err != nil || len(audit) != 1 || audit[0].Actor != "orders" || audit[0].Action != "replay" {
		t.Errorf("audit = %+v, err = %v", audit, err)
	}
}

func TestCancelRequest(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	req, _, err := s.CreateRequest(ctx, newRequest())
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := s.CancelRequest(ctx, req.ID, "ops")
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Status != model.StatusCanceled || canceled.CompletedAt == nil || canceled.NextAttemptAt != nil ||
		canceled.LastError == nil || canceled.LastError.Code != "canceled" || canceled.LastError.Message != "canceled by ops" {
		t.Errorf("canceled request: %+v", canceled)
	}
	if claims, _ := s.ClaimDue(ctx, 10, time.Minute); len(claims) != 0 {
		t.Error("canceled requests must not be delivered")
	}

	// Finished and in-flight requests can't be canceled.
	done := finish(t, s, newRequest(), model.StatusSucceeded, 200, "")
	var stateErr *store.InvalidStateError
	if _, err := s.CancelRequest(ctx, done.ID, "ops"); !errors.As(err, &stateErr) {
		t.Errorf("cancel succeeded request: %v", err)
	}
	inFlight, _, _ := s.CreateRequest(ctx, newRequest())
	if _, err := s.ClaimDue(ctx, 10, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelRequest(ctx, inFlight.ID, "ops"); !errors.As(err, &stateErr) || stateErr.Status != model.StatusInFlight {
		t.Errorf("cancel in-flight request: %v", err)
	}

	// A canceled request can be replayed.
	if _, err := s.ReplayRequest(ctx, canceled.ID, "ops"); err != nil {
		t.Errorf("replay canceled request: %v", err)
	}
}

func TestDLQSummaryAndBulkReplay(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	onUpstream := func(name string) store.NewRequest {
		return newRequest(func(in *store.NewRequest) { in.Upstream = name })
	}
	for range 3 {
		finish(t, s, onUpstream("courier-x"), model.StatusDead, 503, "http_status")
	}
	finish(t, s, onUpstream("courier-x"), model.StatusDead, 400, "http_status")
	timeout := finish(t, s, onUpstream("payments-y"), model.StatusDead, 0, "timeout")
	finish(t, s, onUpstream("payments-y"), model.StatusSucceeded, 200, "")

	groups, total, err := s.DLQSummary(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(groups) != 3 {
		t.Fatalf("total=%d groups=%+v", total, groups)
	}
	if g := groups[0]; g.Upstream != "courier-x" || g.ErrorCode != "http_status" || *g.StatusCode != 503 || g.Count != 3 {
		t.Errorf("largest group = %+v", g)
	}
	if _, n, _ := s.DLQSummary(ctx, "payments-y"); n != 1 {
		t.Errorf("filtered total = %d, want 1", n)
	}

	code503 := 503
	f := store.DLQFilter{Upstream: "courier-x", StatusCode: &code503}
	matched, replayed, err := s.ReplayDead(ctx, f, true, "ops")
	if err != nil || matched != 3 || replayed != 0 {
		t.Fatalf("dry run: matched=%d replayed=%d err=%v", matched, replayed, err)
	}
	matched, replayed, err = s.ReplayDead(ctx, f, false, "ops")
	if err != nil || matched != 3 || replayed != 3 {
		t.Fatalf("replay: matched=%d replayed=%d err=%v", matched, replayed, err)
	}
	// Idempotent: nothing left to match.
	if matched, _, _ := s.ReplayDead(ctx, f, false, "ops"); matched != 0 {
		t.Errorf("second replay matched %d", matched)
	}

	// Filter by IDs and error code.
	matched, _, err = s.ReplayDead(ctx, store.DLQFilter{ErrorCode: "timeout", IDs: []string{timeout.ID}}, false, "ops")
	if err != nil || matched != 1 {
		t.Errorf("replay by id: matched=%d err=%v", matched, err)
	}

	if _, total, _ := s.DLQSummary(ctx, ""); total != 1 {
		t.Errorf("remaining dead = %d, want only the 400", total)
	}
	audit, err := s.ListAudit(ctx, "dlq", "", 10)
	if err != nil || len(audit) != 2 {
		t.Errorf("dlq audit entries = %d (%v), want 2 (the empty replay is not recorded)", len(audit), err)
	}
}
