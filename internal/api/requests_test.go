package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/config"
	"github.com/tanvir001728/hookyard/internal/store/storetest"
)

const testToken = "test-token-0123456789"

const testConfig = `
upstreams:
  courier-x:
    base_url: https://api.courier-x.example
    timeout: 15s
    retry: patient
    dedupe_window: 1h
  payments-y:
    base_url: https://payments.example
`

type testAPI struct {
	t   *testing.T
	srv *Server
}

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	file, err := config.ParseFile([]byte(testConfig), nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithV1(V1{
		Store:   storetest.New(t),
		Config:  file,
		Tokens:  []config.APIToken{{Name: "orders", Secret: testToken}},
		MaxBody: 4096,
	}))
	return &testAPI{t: t, srv: srv}
}

// do sends a request with the test token and decodes a JSON response into out.
func (a *testAPI) do(method, path, body string, out any) *httptest.ResponseRecorder {
	a.t.Helper()
	req := httptest.NewRequestWithContext(a.t.Context(), method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.srv.ServeHTTP(rec, req)
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			a.t.Fatalf("decode %s response: %v\n%s", path, err, rec.Body.String())
		}
	}
	return rec
}

type apiError struct {
	Error struct {
		Code    string       `json:"code"`
		Message string       `json:"message"`
		Details []fieldError `json:"details"`
	} `json:"error"`
}

func TestCreateRequest(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)

	var got requestJSON
	rec := a.do(http.MethodPost, "/v1/requests", `{
		"upstream": "courier-x",
		"method": "post",
		"path": "/shipments?notify=true",
		"headers": {"x-correlation-id": "abc"},
		"body": {"order_id": 123},
		"tags": {"app": "orders"}
	}`, &got)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if loc := rec.Header().Get("Location"); loc != "/v1/requests/"+got.ID {
		t.Errorf("Location = %q", loc)
	}
	if got.Method != http.MethodPost || got.Status != "pending" || got.Path != "/shipments?notify=true" {
		t.Errorf("unexpected request: %+v", got)
	}
	if got.Headers["X-Correlation-Id"] != "abc" {
		t.Errorf("headers should be canonicalized: %v", got.Headers)
	}
	// The store keeps the body byte for byte; the JSON encoder compacts it in
	// API responses.
	if string(got.Body) != `{"order_id":123}` {
		t.Errorf("body = %s", got.Body)
	}
	// Upstream settings apply when the request doesn't override them.
	if got.Timeout.Std() != 15*time.Second || got.Retry.Preset != "patient" || got.Retry.MaxAttempts != 25 {
		t.Errorf("upstream defaults not applied: timeout=%s retry=%+v", got.Timeout, got.Retry)
	}
	if got.DedupeKey != nil || got.LastError != nil || got.CompletedAt != nil {
		t.Errorf("unexpected fields set: %+v", got)
	}
}

func TestCreateRequestOverrides(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)

	deliverAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	var got requestJSON
	rec := a.do(http.MethodPost, "/v1/requests", `{
		"upstream": "courier-x", "method": "GET", "path": "/status",
		"timeout": "2s",
		"retry": {"preset": "quick", "max_attempts": 3},
		"deliver_at": "`+deliverAt.Format(time.RFC3339)+`"
	}`, &got)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if got.Status != "scheduled" || got.DeliverAt == nil || !got.DeliverAt.Equal(deliverAt) {
		t.Errorf("scheduling: status=%s deliver_at=%v", got.Status, got.DeliverAt)
	}
	if got.Timeout.Std() != 2*time.Second || got.Retry.Preset != "quick" || got.Retry.MaxAttempts != 3 {
		t.Errorf("overrides not applied: timeout=%s retry=%+v", got.Timeout, got.Retry)
	}
	if string(got.Body) != "null" {
		t.Errorf("no body should be null, got %s", got.Body)
	}

	// A preset name alone is accepted too.
	rec = a.do(http.MethodPost, "/v1/requests", `{"upstream":"courier-x","method":"GET","path":"/","retry":"none"}`, &got)
	if rec.Code != http.StatusAccepted || got.Retry.MaxAttempts != 1 {
		t.Errorf("retry preset string: %d %+v", rec.Code, got.Retry)
	}
}

func TestCreateRequestDedupe(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	body := `{"upstream":"courier-x","method":"POST","path":"/shipments","dedupe_key":"order-1"}`

	var first, second requestJSON
	if rec := a.do(http.MethodPost, "/v1/requests", body, &first); rec.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	rec := a.do(http.MethodPost, "/v1/requests", body, &second)
	if rec.Code != http.StatusOK || rec.Header().Get("Hookyard-Deduplicated") != "true" {
		t.Fatalf("duplicate: status=%d header=%q", rec.Code, rec.Header().Get("Hookyard-Deduplicated"))
	}
	if second.ID != first.ID || second.DedupeKey == nil || *second.DedupeKey != "order-1" {
		t.Errorf("duplicate returned %+v, want request %s", second, first.ID)
	}
}

func TestCreateRequestValidation(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)

	var e apiError
	rec := a.do(http.MethodPost, "/v1/requests", `{
		"upstream": "courier-x",
		"method": "TRACE",
		"path": "https://evil.example/x",
		"headers": {"Host": "evil.example"},
		"dedupe_key": "",
		"deliver_at": "2099-01-01T00:00:00Z",
		"timeout": "1 minute",
		"retry": {"max_attempts": 0},
		"tags": {"app": "`+strings.Repeat("x", 300)+`"}
	}`, &e)

	if rec.Code != http.StatusUnprocessableEntity || e.Error.Code != codeValidationFailed {
		t.Fatalf("status=%d code=%s", rec.Code, e.Error.Code)
	}
	var fields []string
	for _, d := range e.Error.Details {
		fields = append(fields, d.Field)
	}
	want := "dedupe_key,deliver_at,headers.Host,method,path,retry.max_attempts,tags.app,timeout"
	if got := strings.Join(fields, ","); got != want {
		t.Errorf("invalid fields = %s\nwant %s\n%+v", got, want, e.Error.Details)
	}
	if e.Error.Message != "8 fields are invalid" {
		t.Errorf("message = %q", e.Error.Message)
	}
}

func TestCreateRequestErrors(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{"unknown upstream", `{"upstream":"courier-y","method":"GET","path":"/"}`, 422, codeUnknownUpstream, `did you mean "courier-x"?`},
		{"missing fields", `{}`, 422, codeValidationFailed, "3 fields are invalid"},
		{"unknown field", `{"upstream":"courier-x","method":"GET","path":"/","retries":3}`, 400, codeBadRequest, `unknown field "retries"`},
		{"malformed", `{"upstream":`, 400, codeBadRequest, "invalid JSON"},
		{"empty", ``, 400, codeBadRequest, "request body is empty"},
		{"trailing data", `{"upstream":"courier-x","method":"GET","path":"/"} {}`, 400, codeBadRequest, "unexpected data"},
		{"too large", `{"upstream":"courier-x","method":"POST","path":"/","body":"` + strings.Repeat("x", 5000) + `"}`, 413, codePayloadTooLarge, "exceeds 4096 bytes"},
		{"bad retry preset", `{"upstream":"courier-x","method":"GET","path":"/","retry":"forever"}`, 422, codeValidationFailed, "1 field is invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e apiError
			rec := a.do(http.MethodPost, "/v1/requests", tt.body, &e)
			if rec.Code != tt.wantStatus || e.Error.Code != tt.wantCode || !strings.Contains(e.Error.Message, tt.wantMsg) {
				t.Errorf("got %d %s %q, want %d %s containing %q", rec.Code, e.Error.Code, e.Error.Message, tt.wantStatus, tt.wantCode, tt.wantMsg)
			}
		})
	}
}

func TestCreateRequestIgnoresContentType(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	// curl -d sends application/x-www-form-urlencoded unless told otherwise.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/requests",
		strings.NewReader(`{"upstream":"courier-x","method":"GET","path":"/"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	a.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Errorf("got %d %s", rec.Code, rec.Body)
	}
}

func TestAuth(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)

	for name, header := range map[string]string{
		"missing":      "",
		"wrong token":  "Bearer not-the-token-at-all",
		"wrong scheme": "Basic " + testToken,
		"empty bearer": "Bearer ",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/requests", strings.NewReader(`{}`))
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := httptest.NewRecorder()
			a.srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
				t.Errorf("status=%d WWW-Authenticate=%q", rec.Code, rec.Header().Get("WWW-Authenticate"))
			}
		})
	}

	t.Run("case-insensitive scheme", func(t *testing.T) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/requests", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "bearer "+testToken)
		rec := httptest.NewRecorder()
		a.srv.ServeHTTP(rec, req)
		if rec.Code == http.StatusUnauthorized {
			t.Error("lowercase bearer scheme should be accepted")
		}
	})
}

func TestUnknownV1RouteIsJSON404(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	var e apiError
	rec := a.do(http.MethodGet, "/v1/nope", "", &e)
	if rec.Code != http.StatusNotFound || e.Error.Code != codeNotFound {
		t.Errorf("got %d %+v", rec.Code, e)
	}
}
