package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
	"github.com/tanvir001728/hookyard/internal/store/storetest"
)

func TestCallbacksQueuedWhenRequestsFinish(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	withCallback := newRequest()
	withCallback.CallbackURL = "http://orders.internal/hooks/hookyard"
	withCallback.OnResult = "order.shipment"

	// No callback URL, no callback.
	plain := finish(t, s, newRequest(), model.StatusSucceeded, 200, "")
	if cbs, err := s.ListCallbacks(ctx, plain.ID); err != nil || len(cbs) != 0 {
		t.Fatalf("callbacks without a URL: %+v, %v", cbs, err)
	}

	done := finish(t, s, withCallback, model.StatusSucceeded, 201, "")
	if done.CallbackURL != withCallback.CallbackURL || done.OnResult != "order.shipment" {
		t.Errorf("request = %+v", done)
	}
	cbs, err := s.ListCallbacks(ctx, done.ID)
	if err != nil || len(cbs) != 1 {
		t.Fatalf("callbacks = %+v, %v", cbs, err)
	}
	cb := cbs[0]
	if cb.EventType != "request.succeeded" || cb.RequestStatus != model.StatusSucceeded || cb.RequestAttempts != 1 ||
		cb.Status != store.CallbackPending || cb.URL != withCallback.CallbackURL || cb.Payload != "" {
		t.Errorf("callback = %+v", cb)
	}

	// Canceling also queues one, and so does each later final state.
	pending, _, err := s.CreateRequest(ctx, withCallback)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelRequest(ctx, pending.ID, "ops"); err != nil {
		t.Fatal(err)
	}
	if cbs, _ := s.ListCallbacks(ctx, pending.ID); len(cbs) != 1 || cbs[0].EventType != "request.canceled" || cbs[0].RequestAttempts != 0 {
		t.Errorf("cancel callbacks = %+v", cbs)
	}

	if _, err := s.ListCallbacks(ctx, model.NewRequestID()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown request: %v", err)
	}
}

func TestCallbackDeliveryLifecycle(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	in := newRequest()
	in.CallbackURL = "http://orders.internal/hooks"
	req := finish(t, s, in, model.StatusDead, 400, "http_status")

	claims, err := s.ClaimCallbacks(ctx, 10, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims = %+v, %v", claims, err)
	}
	c := claims[0]
	if c.Callback.Status != store.CallbackDelivering || c.Callback.AttemptCount != 1 || c.Callback.EventType != "request.dead" {
		t.Errorf("claimed = %+v", c.Callback)
	}
	if again, _ := s.ClaimCallbacks(ctx, 10, time.Minute); len(again) != 0 {
		t.Fatal("a claimed callback must not be claimed again")
	}

	// A failed attempt schedules a retry and keeps the payload.
	code := 500
	retryAt := time.Now().Add(-time.Second)
	err = s.RecordCallbackAttempt(ctx, store.CallbackResult{
		ID: c.Callback.ID, LeaseExpiresAt: c.LeaseExpiresAt,
		Payload: `{"v":1}`, StatusCode: &code, Error: "responded 500", NextAttemptAt: &retryAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The old lease no longer works.
	if err := s.RecordCallbackAttempt(ctx, store.CallbackResult{ID: c.Callback.ID, LeaseExpiresAt: c.LeaseExpiresAt, Delivered: true}); !errors.Is(err, store.ErrLeaseLost) {
		t.Errorf("stale lease: %v", err)
	}

	claims, _ = s.ClaimCallbacks(ctx, 10, time.Minute)
	if len(claims) != 1 || claims[0].Callback.Payload != `{"v":1}` || claims[0].Callback.AttemptCount != 2 ||
		claims[0].Callback.LastError != "responded 500" {
		t.Fatalf("second claim = %+v", claims)
	}
	// The payload is fixed after the first attempt.
	c = claims[0]
	if err := s.RecordCallbackAttempt(ctx, store.CallbackResult{ID: c.Callback.ID, LeaseExpiresAt: c.LeaseExpiresAt, Payload: `{"v":2}`, Error: "timeout"}); err != nil {
		t.Fatal(err)
	}
	cbs, _ := s.ListCallbacks(ctx, req.ID)
	if cbs[0].Status != store.CallbackFailed || cbs[0].Payload != `{"v":1}` || cbs[0].LastStatusCode != nil {
		t.Errorf("failed callback = %+v", cbs[0])
	}

	// Only failed callbacks can be retried; retrying starts a fresh budget.
	retried, err := s.RetryCallback(ctx, req.ID, c.Callback.ID, "ops")
	if err != nil || retried.Status != store.CallbackPending || retried.AttemptCount != 0 {
		t.Fatalf("retry = %+v, %v", retried, err)
	}
	var stateErr *store.CallbackStateError
	if _, err := s.RetryCallback(ctx, req.ID, c.Callback.ID, "ops"); !errors.As(err, &stateErr) {
		t.Errorf("retry pending: %v", err)
	}
	if _, err := s.RetryCallback(ctx, req.ID, "evt_nope", "ops"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("retry unknown: %v", err)
	}

	claims, _ = s.ClaimCallbacks(ctx, 10, time.Minute)
	c = claims[0]
	if err := s.RecordCallbackAttempt(ctx, store.CallbackResult{ID: c.Callback.ID, LeaseExpiresAt: c.LeaseExpiresAt, Delivered: true}); err != nil {
		t.Fatal(err)
	}
	cbs, _ = s.ListCallbacks(ctx, req.ID)
	if cbs[0].Status != store.CallbackDelivered || cbs[0].DeliveredAt == nil || cbs[0].LastError != "" {
		t.Errorf("delivered callback = %+v", cbs[0])
	}
	if audit, _ := s.ListAudit(ctx, "request", req.ID, 10); len(audit) != 1 || audit[0].Action != "callback.retry" {
		t.Errorf("audit = %+v", audit)
	}
}

func TestRecoverExpiredCallbackLeases(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	in := newRequest()
	in.CallbackURL = "http://orders.internal/hooks"
	finish(t, s, in, model.StatusSucceeded, 200, "")
	if claims, _ := s.ClaimCallbacks(ctx, 10, time.Millisecond); len(claims) != 1 {
		t.Fatal("not claimed")
	}
	time.Sleep(20 * time.Millisecond)
	if n, err := s.RecoverExpiredCallbackLeases(ctx); err != nil || n != 1 {
		t.Fatalf("recovered %d, %v", n, err)
	}
	if claims, _ := s.ClaimCallbacks(ctx, 10, time.Minute); len(claims) != 1 || claims[0].Callback.AttemptCount != 2 {
		t.Errorf("reclaim = %+v", claims)
	}
}
