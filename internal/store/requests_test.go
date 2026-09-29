package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
	"github.com/tanvir001728/hookyard/internal/store/storetest"
)

var standardRetry = model.RetryPolicy{
	Preset:          "standard",
	MaxAttempts:     10,
	InitialInterval: 5 * time.Second,
	MaxInterval:     10 * time.Minute,
	Multiplier:      2,
	MaxAge:          24 * time.Hour,
}

func newRequest(mod ...func(*store.NewRequest)) store.NewRequest {
	in := store.NewRequest{
		Upstream: "courier-x",
		Method:   http.MethodPost,
		Path:     "/shipments",
		Headers:  map[string]string{"X-Correlation-Id": "abc"},
		Body:     json.RawMessage(`{"order_id": 123, "items": [1, 2]}`),
		Retry:    standardRetry,
		Timeout:  15 * time.Second,
		Tags:     map[string]string{"app": "orders"},
	}
	for _, m := range mod {
		m(&in)
	}
	return in
}

func TestCreateAndGetRequest(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	created, isNew, err := s.CreateRequest(ctx, newRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !isNew {
		t.Fatal("expected a new request")
	}
	if !model.IsRequestID(created.ID) {
		t.Errorf("invalid id %q", created.ID)
	}
	if created.Status != model.StatusPending {
		t.Errorf("status = %s, want pending", created.Status)
	}
	if created.NextAttemptAt == nil {
		t.Error("pending request must have next_attempt_at")
	}

	got, err := s.GetRequest(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Upstream != "courier-x" || got.Method != http.MethodPost || got.Path != "/shipments" {
		t.Errorf("unexpected request: %+v", got)
	}
	// The body is preserved byte for byte (json, not jsonb).
	if string(got.Body) != `{"order_id": 123, "items": [1, 2]}` {
		t.Errorf("body = %s", got.Body)
	}
	if got.Headers["X-Correlation-Id"] != "abc" || got.Tags["app"] != "orders" {
		t.Errorf("headers/tags not round-tripped: %v %v", got.Headers, got.Tags)
	}
	if got.Retry != standardRetry {
		t.Errorf("retry = %+v, want %+v", got.Retry, standardRetry)
	}
	if got.Timeout != 15*time.Second {
		t.Errorf("timeout = %s", got.Timeout)
	}
}

func TestCreateRequestWithoutBody(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)

	for _, body := range []json.RawMessage{nil, json.RawMessage("null")} {
		req, _, err := s.CreateRequest(t.Context(), newRequest(func(in *store.NewRequest) {
			in.Method = http.MethodGet
			in.Body = body
			in.Headers = nil
			in.Tags = nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if req.Body != nil {
			t.Errorf("body = %s, want nil", req.Body)
		}
		if req.Headers == nil || req.Tags == nil {
			t.Error("headers and tags should decode to empty maps")
		}
	}
}

func TestCreateScheduledRequest(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)

	future := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	req, _, err := s.CreateRequest(t.Context(), newRequest(func(in *store.NewRequest) { in.DeliverAt = &future }))
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != model.StatusScheduled {
		t.Errorf("status = %s, want scheduled", req.Status)
	}
	if req.NextAttemptAt == nil || !req.NextAttemptAt.Equal(future) {
		t.Errorf("next_attempt_at = %v, want %v", req.NextAttemptAt, future)
	}

	past := time.Now().Add(-time.Hour)
	req, _, err = s.CreateRequest(t.Context(), newRequest(func(in *store.NewRequest) { in.DeliverAt = &past }))
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != model.StatusPending {
		t.Errorf("deliver_at in the past: status = %s, want pending", req.Status)
	}
}

func TestDedupe(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()
	withKey := func(key, upstream string) store.NewRequest {
		return newRequest(func(in *store.NewRequest) {
			in.DedupeKey, in.DedupeWindow, in.Upstream = key, time.Hour, upstream
		})
	}

	first, created, err := s.CreateRequest(ctx, withKey("order-1", "courier-x"))
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}

	again, created, err := s.CreateRequest(ctx, withKey("order-1", "courier-x"))
	if err != nil {
		t.Fatal(err)
	}
	if created || again.ID != first.ID {
		t.Errorf("duplicate key created a new request (created=%v, id=%s, first=%s)", created, again.ID, first.ID)
	}

	// Keys are scoped per upstream.
	other, created, err := s.CreateRequest(ctx, withKey("order-1", "payments-y"))
	if err != nil || !created || other.ID == first.ID {
		t.Errorf("same key on another upstream should create a request (created=%v err=%v)", created, err)
	}

	// The losing insert is rolled back, so only two requests exist.
	page, err := s.ListRequests(ctx, store.RequestFilter{DedupeKey: "order-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Requests) != 2 {
		t.Errorf("got %d requests with the key, want 2", len(page.Requests))
	}
}

func TestDedupeKeyReusableAfterWindow(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()
	in := newRequest(func(in *store.NewRequest) { in.DedupeKey, in.DedupeWindow = "order-2", 50*time.Millisecond })

	first, _, err := s.CreateRequest(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	second, created, err := s.CreateRequest(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !created || second.ID == first.ID {
		t.Errorf("expired key should allow a new request (created=%v)", created)
	}
}

func TestDedupeConcurrent(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	in := newRequest(func(in *store.NewRequest) { in.DedupeKey, in.DedupeWindow = "order-3", time.Hour })

	const n = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ids     = map[string]bool{}
		created int
	)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, isNew, err := s.CreateRequest(context.Background(), in)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			ids[req.ID] = true
			if isNew {
				created++
			}
		}()
	}
	wg.Wait()

	if created != 1 || len(ids) != 1 {
		t.Errorf("concurrent creates with one key: created=%d distinct ids=%d, want 1 and 1", created, len(ids))
	}
}

func TestCreateRequestRequiresDedupeWindow(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	_, _, err := s.CreateRequest(t.Context(), newRequest(func(in *store.NewRequest) { in.DedupeKey = "k" }))
	if err == nil {
		t.Fatal("expected an error when the dedupe window is missing")
	}
}

func TestGetRequestNotFound(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	if _, err := s.GetRequest(t.Context(), model.NewRequestID()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListRequestsFiltersAndPagination(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	var ids []string
	for i := range 5 {
		req, _, err := s.CreateRequest(ctx, newRequest(func(in *store.NewRequest) {
			if i%2 == 1 {
				in.Upstream = "payments-y"
				in.Tags = map[string]string{"app": "billing", "tenant": "acme"}
			}
		}))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, req.ID)
	}

	t.Run("newest first", func(t *testing.T) {
		page, err := s.ListRequests(ctx, store.RequestFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Requests) != 5 || page.Requests[0].ID != ids[4] || page.NextCursor != "" {
			t.Errorf("unexpected page: %d requests, first %s, cursor %q", len(page.Requests), page.Requests[0].ID, page.NextCursor)
		}
	})

	t.Run("filters", func(t *testing.T) {
		tests := []struct {
			name   string
			filter store.RequestFilter
			want   int
		}{
			{"upstream", store.RequestFilter{Upstream: "payments-y"}, 2},
			{"tag", store.RequestFilter{Tags: map[string]string{"app": "billing"}}, 2},
			{"two tags", store.RequestFilter{Tags: map[string]string{"app": "billing", "tenant": "other"}}, 0},
			{"status", store.RequestFilter{Statuses: []model.Status{model.StatusPending}}, 5},
			{"status none", store.RequestFilter{Statuses: []model.Status{model.StatusDead, model.StatusFailed}}, 0},
			{"created before", store.RequestFilter{CreatedBefore: ptr(time.Now().Add(-time.Hour))}, 0},
			{"created after", store.RequestFilter{CreatedAfter: ptr(time.Now().Add(-time.Hour))}, 5},
		}
		for _, tt := range tests {
			page, err := s.ListRequests(ctx, tt.filter)
			if err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			if len(page.Requests) != tt.want {
				t.Errorf("%s: got %d requests, want %d", tt.name, len(page.Requests), tt.want)
			}
		}
	})

	t.Run("pagination", func(t *testing.T) {
		var seen []string
		cursor := ""
		for pages := 0; ; pages++ {
			if pages > 5 {
				t.Fatal("pagination did not terminate")
			}
			page, err := s.ListRequests(ctx, store.RequestFilter{Limit: 2, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range page.Requests {
				seen = append(seen, r.ID)
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if len(seen) != 5 {
			t.Fatalf("paged through %d requests, want 5", len(seen))
		}
		for i, id := range seen {
			if id != ids[4-i] {
				t.Errorf("position %d: got %s, want %s", i, id, ids[4-i])
			}
		}
	})

	t.Run("invalid cursor", func(t *testing.T) {
		_, err := s.ListRequests(ctx, store.RequestFilter{Cursor: "not-a-cursor"})
		if !errors.Is(err, store.ErrInvalidCursor) {
			t.Fatalf("err = %v, want ErrInvalidCursor", err)
		}
	})
}

func TestListAttempts(t *testing.T) {
	t.Parallel()
	s, dbURL := storetest.NewWithURL(t)
	ctx := t.Context()

	req, _, err := s.CreateRequest(ctx, newRequest())
	if err != nil {
		t.Fatal(err)
	}

	attempts, err := s.ListAttempts(ctx, req.ID)
	if err != nil || len(attempts) != 0 {
		t.Fatalf("new request: attempts=%v err=%v", attempts, err)
	}
	if _, err := s.ListAttempts(ctx, model.NewRequestID()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown request: err = %v, want ErrNotFound", err)
	}

	// Recording attempts arrives with the delivery workers (#6); insert rows
	// directly to exercise the read path.
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `
		INSERT INTO attempts (request_id, number, started_at, duration_ms, outcome, status_code,
			error_code, error_message, response_headers, response_body, response_body_truncated, retry_at)
		VALUES
			($1, 2, now(), 80, 'success', 201, NULL, NULL, '{"Content-Type":"application/json"}', '{"ok":true}', false, NULL),
			($1, 1, now(), 1500, 'retryable_failure', 503, 'http_status', 'upstream responded with 503', NULL, NULL, false, now())`,
		req.ID)
	if err != nil {
		t.Fatal(err)
	}

	attempts, err = s.ListAttempts(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].Number != 1 || attempts[1].Number != 2 {
		t.Fatalf("attempts not ordered by number: %+v", attempts)
	}
	failed, ok := attempts[0], attempts[1]
	if failed.Outcome != model.OutcomeRetryableFailure || failed.Error == nil || failed.Error.Code != "http_status" ||
		failed.RetryAt == nil || failed.Response != nil || failed.Duration != 1500*time.Millisecond {
		t.Errorf("unexpected failed attempt: %+v", failed)
	}
	if ok.Outcome != model.OutcomeSuccess || ok.Response == nil || ok.Response.Body != `{"ok":true}` ||
		ok.Response.Headers["Content-Type"] != "application/json" || *ok.StatusCode != 201 {
		t.Errorf("unexpected successful attempt: %+v", ok)
	}
}

func ptr[T any](v T) *T { return &v }
