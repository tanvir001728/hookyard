package api

import (
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/config"
	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
	"github.com/tanvir001728/hookyard/internal/store/storetest"
)

func newCallbackAPI(t *testing.T, enabled bool) *testAPI {
	t.Helper()
	file, err := config.ParseFile([]byte(`
callbacks:
  allow: ["http://orders.internal/hooks/"]
upstreams:
  courier-x:
    base_url: https://api.courier-x.example
    callback_url: http://orders.internal/hooks/courier
  payments-y:
    base_url: https://payments.example
`), nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithV1(V1{
		Store:            storetest.New(t),
		Config:           file,
		Tokens:           []config.APIToken{{Name: "orders", Secret: testToken}},
		MaxBody:          4096,
		CallbacksEnabled: enabled,
	}))
	return &testAPI{t: t, srv: srv}
}

func TestCreateRequestCallbacks(t *testing.T) {
	t.Parallel()
	a := newCallbackAPI(t, true)

	// The upstream's default applies.
	got := a.enqueue(`{"upstream":"courier-x","method":"POST","path":"/x","on_result":"order.shipment"}`)
	if got.CallbackURL == nil || *got.CallbackURL != "http://orders.internal/hooks/courier" || got.OnResult == nil || *got.OnResult != "order.shipment" {
		t.Errorf("default: %+v", got)
	}
	// A request can override it, or turn it off.
	got = a.enqueue(`{"upstream":"courier-x","method":"POST","path":"/x","callback_url":"http://orders.internal/hooks/other"}`)
	if got.CallbackURL == nil || *got.CallbackURL != "http://orders.internal/hooks/other" {
		t.Errorf("override: %+v", got)
	}
	got = a.enqueue(`{"upstream":"courier-x","method":"POST","path":"/x","callback_url":""}`)
	if got.CallbackURL != nil {
		t.Errorf("off: %+v", got)
	}
	got = a.enqueue(`{"upstream":"payments-y","method":"POST","path":"/x"}`)
	if got.CallbackURL != nil || got.OnResult != nil {
		t.Errorf("none: %+v", got)
	}

	var e apiError
	rec := a.do(http.MethodPost, "/v1/requests", `{"upstream":"payments-y","method":"POST","path":"/x","callback_url":"http://evil.example/steal","on_result":""}`, &e)
	if rec.Code != http.StatusUnprocessableEntity || len(e.Error.Details) != 2 ||
		e.Error.Details[0].Field != "callback_url" || e.Error.Details[0].Message != "is not allowed by callbacks.allow in hookyard.yaml" ||
		e.Error.Details[1].Field != "on_result" {
		t.Errorf("validation: %d %+v", rec.Code, e)
	}
}

func TestCreateRequestCallbacksDisabled(t *testing.T) {
	t.Parallel()
	a := newCallbackAPI(t, false)
	var e apiError
	rec := a.do(http.MethodPost, "/v1/requests", `{"upstream":"payments-y","method":"POST","path":"/x","callback_url":"http://orders.internal/hooks/x"}`, &e)
	if rec.Code != http.StatusUnprocessableEntity || len(e.Error.Details) != 1 ||
		e.Error.Details[0].Message != "callbacks are disabled: set HOOKYARD_CALLBACK_SECRETS on the server to sign them" {
		t.Errorf("disabled: %d %+v", rec.Code, e)
	}
	// Without a callback URL, requests still work.
	a.enqueue(`{"upstream":"payments-y","method":"POST","path":"/x"}`)
}

func TestListAndRetryCallbacks(t *testing.T) {
	t.Parallel()
	a := newCallbackAPI(t, true)
	req := a.enqueue(`{"upstream":"courier-x","method":"POST","path":"/x"}`)
	a.deliverAll(model.StatusDead, 400)

	var list struct{ Data []callbackJSON }
	if rec := a.do(http.MethodGet, "/v1/requests/"+req.ID+"/callbacks", "", &list); rec.Code != http.StatusOK || len(list.Data) != 1 {
		t.Fatalf("list: %d %+v", rec.Code, list)
	}
	cb := list.Data[0]
	if cb.Type != "request.dead" || cb.Status != "pending" || cb.RequestStatus != model.StatusDead || cb.NextAttemptAt == nil || cb.URL != "http://orders.internal/hooks/courier" {
		t.Errorf("callback = %+v", cb)
	}

	// Only failed callbacks can be retried.
	var e apiError
	if rec := a.do(http.MethodPost, "/v1/requests/"+req.ID+"/callbacks/"+cb.ID+"/retry", "", &e); rec.Code != http.StatusConflict || e.Error.Code != "invalid_state" {
		t.Errorf("retry pending: %d %+v", rec.Code, e)
	}
	st := a.srv.v1.Store
	claims, _ := st.ClaimCallbacks(t.Context(), 10, time.Minute)
	if err := st.RecordCallbackAttempt(t.Context(), store.CallbackResult{ID: claims[0].Callback.ID, LeaseExpiresAt: claims[0].LeaseExpiresAt, Error: "refused"}); err != nil {
		t.Fatal(err)
	}
	var retried callbackJSON
	if rec := a.do(http.MethodPost, "/v1/requests/"+req.ID+"/callbacks/"+cb.ID+"/retry", "", &retried); rec.Code != http.StatusAccepted || retried.Status != "pending" || retried.AttemptCount != 0 {
		t.Errorf("retry failed: %d %+v", rec.Code, retried)
	}

	if rec := a.do(http.MethodPost, "/v1/requests/"+req.ID+"/callbacks/evt_nope/retry", "", &e); rec.Code != http.StatusNotFound {
		t.Errorf("unknown callback: %d", rec.Code)
	}
	if rec := a.do(http.MethodGet, "/v1/requests/"+model.NewRequestID()+"/callbacks", "", &e); rec.Code != http.StatusNotFound {
		t.Errorf("unknown request: %d", rec.Code)
	}
}
