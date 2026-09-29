package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
)

// enqueue creates a request through the API.
func (a *testAPI) enqueue(body string) requestJSON {
	a.t.Helper()
	var got requestJSON
	if rec := a.do(http.MethodPost, "/v1/requests", body, &got); rec.Code != http.StatusAccepted {
		a.t.Fatalf("enqueue: %d %s", rec.Code, rec.Body)
	}
	return got
}

// deliverAll plays the worker: every due request gets one attempt with the
// given outcome.
func (a *testAPI) deliverAll(status model.Status, code int) {
	a.t.Helper()
	ctx := context.Background()
	claims, err := a.srv.v1.Store.ClaimDue(ctx, 100, time.Minute)
	if err != nil {
		a.t.Fatal(err)
	}
	for _, c := range claims {
		att := model.Attempt{
			RequestID: c.Request.ID, Number: c.Request.AttemptCount + 1, StartedAt: time.Now(),
			Duration: 42 * time.Millisecond, Outcome: model.OutcomeSuccess, StatusCode: &code,
			Response: &model.AttemptResponse{Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"ok":true}`},
		}
		if status == model.StatusDead {
			att.Outcome = model.OutcomePermanentFailure
			att.Error = &model.DeliveryError{Code: "http_status", Message: "upstream responded with 400 Bad Request"}
		}
		if err := a.srv.v1.Store.RecordAttempt(ctx, store.AttemptRecord{Attempt: att, Status: status, LeaseExpiresAt: c.LeaseExpiresAt}); err != nil {
			a.t.Fatal(err)
		}
	}
}

func TestGetRequestAndAttempts(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	req := a.enqueue(`{"upstream":"courier-x","method":"POST","path":"/shipments"}`)
	a.deliverAll(model.StatusSucceeded, 201)

	var got requestJSON
	if rec := a.do(http.MethodGet, "/v1/requests/"+req.ID, "", &got); rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	if got.Status != model.StatusSucceeded || got.AttemptCount != 1 || *got.LastStatusCode != 201 || got.CompletedAt == nil {
		t.Errorf("request = %+v", got)
	}

	var attempts struct {
		Data []attemptJSON `json:"data"`
	}
	a.do(http.MethodGet, "/v1/requests/"+req.ID+"/attempts", "", &attempts)
	if len(attempts.Data) != 1 {
		t.Fatalf("attempts = %+v", attempts)
	}
	at := attempts.Data[0]
	if at.Number != 1 || at.Outcome != model.OutcomeSuccess || at.DurationMS != 42 || *at.StatusCode != 201 ||
		at.Response == nil || at.Response.Body != `{"ok":true}` {
		t.Errorf("attempt = %+v", at)
	}

	for _, path := range []string{"/v1/requests/req_01J9ZK3Q4W5E6R7T8Y9U0I1O2P", "/v1/requests/nope", "/v1/requests/nope/attempts"} {
		var e apiError
		if rec := a.do(http.MethodGet, path, "", &e); rec.Code != http.StatusNotFound || e.Error.Code != codeNotFound {
			t.Errorf("%s: %d %+v", path, rec.Code, e)
		}
	}
}

func TestListRequests(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	for i := range 3 {
		up := "courier-x"
		if i == 2 {
			up = "payments-y"
		}
		a.enqueue(`{"upstream":"` + up + `","method":"GET","path":"/","tags":{"app":"orders"}}`)
	}

	type page struct {
		Data       []requestJSON `json:"data"`
		NextCursor *string       `json:"next_cursor"`
	}
	var p page
	a.do(http.MethodGet, "/v1/requests?upstream=courier-x&status=pending,failed&tag=app:orders", "", &p)
	if len(p.Data) != 2 || p.NextCursor != nil {
		t.Errorf("filtered list: %d requests, cursor %v", len(p.Data), p.NextCursor)
	}

	a.do(http.MethodGet, "/v1/requests?limit=2", "", &p)
	if len(p.Data) != 2 || p.NextCursor == nil {
		t.Fatalf("first page: %d requests, cursor %v", len(p.Data), p.NextCursor)
	}
	var p2 page
	a.do(http.MethodGet, "/v1/requests?limit=2&cursor="+*p.NextCursor, "", &p2)
	if len(p2.Data) != 1 || p2.NextCursor != nil || p2.Data[0].Upstream != "courier-x" {
		t.Errorf("second page: %+v", p2)
	}

	for _, q := range []string{"limit=0", "limit=1000", "status=lost", "created_after=yesterday", "tag=nocolon", "cursor=bogus"} {
		var e apiError
		if rec := a.do(http.MethodGet, "/v1/requests?"+q, "", &e); rec.Code != http.StatusBadRequest || e.Error.Code != codeBadRequest {
			t.Errorf("%s: %d %+v", q, rec.Code, e)
		}
	}
}

func TestReplayAndCancel(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	req := a.enqueue(`{"upstream":"courier-x","method":"POST","path":"/shipments"}`)

	// Pending: can't be replayed.
	var e apiError
	if rec := a.do(http.MethodPost, "/v1/requests/"+req.ID+"/replay", "", &e); rec.Code != http.StatusConflict || e.Error.Code != codeInvalidState ||
		!strings.Contains(e.Error.Message, "cannot replay a request that is pending") {
		t.Errorf("replay pending: %d %+v", rec.Code, e)
	}

	a.deliverAll(model.StatusDead, 400)
	var replayed requestJSON
	if rec := a.do(http.MethodPost, "/v1/requests/"+req.ID+"/replay", "", &replayed); rec.Code != http.StatusAccepted || replayed.Status != model.StatusPending {
		t.Fatalf("replay dead: %d %+v", rec.Code, replayed)
	}

	var canceled requestJSON
	if rec := a.do(http.MethodPost, "/v1/requests/"+req.ID+"/cancel", "", &canceled); rec.Code != http.StatusOK ||
		canceled.Status != model.StatusCanceled || canceled.LastError.Message != "canceled by orders" {
		t.Fatalf("cancel: %d %+v", rec.Code, canceled)
	}
	if rec := a.do(http.MethodPost, "/v1/requests/"+req.ID+"/cancel", "", &e); rec.Code != http.StatusConflict {
		t.Errorf("cancel twice: %d", rec.Code)
	}
}

func TestDLQ(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	for range 3 {
		a.enqueue(`{"upstream":"courier-x","method":"POST","path":"/shipments"}`)
	}
	a.deliverAll(model.StatusDead, 400)
	ok := a.enqueue(`{"upstream":"payments-y","method":"POST","path":"/pay"}`)
	a.deliverAll(model.StatusSucceeded, 200)

	var summary struct {
		Total  int `json:"total"`
		Groups []struct {
			Upstream   string `json:"upstream"`
			ErrorCode  string `json:"error_code"`
			StatusCode int    `json:"status_code"`
			Count      int    `json:"count"`
		} `json:"groups"`
	}
	a.do(http.MethodGet, "/v1/dlq", "", &summary)
	if summary.Total != 3 || len(summary.Groups) != 1 || summary.Groups[0].StatusCode != 400 || summary.Groups[0].Count != 3 {
		t.Errorf("summary = %+v", summary)
	}

	var result struct {
		Matched  int  `json:"matched"`
		Replayed int  `json:"replayed"`
		DryRun   bool `json:"dry_run"`
	}
	a.do(http.MethodPost, "/v1/dlq/replay", `{"upstream":"courier-x","dry_run":true}`, &result)
	if result.Matched != 3 || result.Replayed != 0 || !result.DryRun {
		t.Errorf("dry run = %+v", result)
	}
	a.do(http.MethodPost, "/v1/dlq/replay", `{"upstream":"courier-x","error_code":"http_status","status_code":400}`, &result)
	if result.Matched != 3 || result.Replayed != 3 {
		t.Errorf("replay = %+v", result)
	}
	a.do(http.MethodGet, "/v1/dlq", "", &summary)
	if summary.Total != 0 {
		t.Errorf("DLQ should be empty after replay: %+v", summary)
	}

	var e apiError
	rec := a.do(http.MethodPost, "/v1/dlq/replay", `{"error_code":"oops","status_code":42,"ids":["nope","`+ok.ID+`"]}`, &e)
	if rec.Code != http.StatusUnprocessableEntity || len(e.Error.Details) != 3 {
		t.Errorf("invalid replay filter: %d %+v", rec.Code, e)
	}
}

func TestUpstreams(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)

	var list struct {
		Data []upstreamJSON `json:"data"`
	}
	a.do(http.MethodGet, "/v1/upstreams", "", &list)
	if len(list.Data) != 2 || list.Data[0].Name != "courier-x" || list.Data[0].Retry.Preset != "patient" || list.Data[0].Timeout.Std() != 15*time.Second {
		t.Errorf("upstreams = %+v", list.Data)
	}

	var one upstreamJSON
	rec := a.do(http.MethodGet, "/v1/upstreams/courier-x", "", &one)
	if len(one.HeaderNames) != 1 || one.HeaderNames[0] != "Authorization" || strings.Contains(rec.Body.String(), "secret-never-exposed") {
		t.Errorf("header values must never be exposed: %s", rec.Body)
	}

	var e apiError
	if rec := a.do(http.MethodGet, "/v1/upstreams/courier-y", "", &e); rec.Code != http.StatusNotFound || !strings.Contains(e.Error.Message, `did you mean "courier-x"`) {
		t.Errorf("unknown upstream: %d %+v", rec.Code, e)
	}
}
