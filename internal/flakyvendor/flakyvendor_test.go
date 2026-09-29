package flakyvendor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func call(t *testing.T, srv http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestSucceedsByDefaultAndEchoes(t *testing.T) {
	s := New()
	rec := call(t, s, http.MethodPost, "/orders", `{"id":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Status   string   `json:"status"`
		Received Received `json:"received"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "OK" || resp.Received.Body != `{"id":1}` || resp.Received.Attempt != 1 {
		t.Errorf("unexpected echo: %+v", resp)
	}
}

func TestFailFirst(t *testing.T) {
	s := New()
	want := []int{503, 503, 200, 200}
	for i, code := range want {
		if rec := call(t, s, http.MethodPost, "/orders?fail_first=2", ""); rec.Code != code {
			t.Errorf("request %d: status = %d, want %d", i+1, rec.Code, code)
		}
	}
	// Counters are per key: another path starts over.
	if rec := call(t, s, http.MethodPost, "/other?fail_first=1", ""); rec.Code != 503 {
		t.Errorf("new key should fail first: %d", rec.Code)
	}
	// An explicit key groups different paths.
	call(t, s, http.MethodPost, "/a?fail_first=1&key=k", "")
	if rec := call(t, s, http.MethodPost, "/b?fail_first=1&key=k", ""); rec.Code != 200 {
		t.Errorf("shared key: second request should succeed, got %d", rec.Code)
	}
}

func TestStatusRetryAfterAndFakeError(t *testing.T) {
	s := New()
	rec := call(t, s, http.MethodGet, "/x?status=429&retry_after=7", "")
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "7" {
		t.Errorf("got %d Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}

	rec = call(t, s, http.MethodGet, "/x?fake_error=1", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"FAILED"`) {
		t.Errorf("fake error: %d %s", rec.Code, rec.Body)
	}

	rec = call(t, s, http.MethodGet, "/y?fail_first=1&fail_status=500", "")
	if rec.Code != 500 {
		t.Errorf("fail_status: %d", rec.Code)
	}
}

func TestFailRate(t *testing.T) {
	s := New()
	values := []float64{0.1, 0.9}
	s.rand = func() float64 { v := values[0]; values = values[1:]; return v }
	if rec := call(t, s, http.MethodGet, "/x?fail_rate=0.5", ""); rec.Code != 503 {
		t.Errorf("roll 0.1 < 0.5 should fail, got %d", rec.Code)
	}
	if rec := call(t, s, http.MethodGet, "/x?fail_rate=0.5", ""); rec.Code != 200 {
		t.Errorf("roll 0.9 >= 0.5 should succeed, got %d", rec.Code)
	}
}

func TestLatencyAndHang(t *testing.T) {
	srv := httptest.NewServer(New())
	defer srv.Close()

	start := time.Now()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/x?latency=100ms", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if time.Since(start) < 100*time.Millisecond {
		t.Error("latency not applied")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/x?hang=1", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("hang should make the client time out")
	}
}

func TestRecordedRequests(t *testing.T) {
	s := New()
	call(t, s, http.MethodPost, "/a?status=500", "one")
	call(t, s, http.MethodPut, "/b", "two")

	got := s.Requests()
	if len(got) != 2 || got[0].Status != 500 || got[1].Method != http.MethodPut || got[1].Body != "two" {
		t.Fatalf("recorded = %+v", got)
	}

	rec := call(t, s, http.MethodGet, "/_requests", "")
	var list struct {
		Data []Received `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Data) != 2 {
		t.Fatalf("GET /_requests: %v %s", err, rec.Body)
	}

	if rec := call(t, s, http.MethodDelete, "/_requests", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d", rec.Code)
	}
	if len(s.Requests()) != 0 {
		t.Error("reset should clear requests")
	}
	if rec := call(t, s, http.MethodPost, "/orders?fail_first=1", ""); rec.Code != 503 {
		t.Error("reset should clear fail_first counters")
	}
}

func TestInvalidParams(t *testing.T) {
	s := New()
	for _, q := range []string{"status=42", "fail_rate=2", "latency=soon", "fail_first=-1"} {
		rec := call(t, s, http.MethodGet, "/x?"+q, "")
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(string(body), "flakyvendor:") {
			t.Errorf("%s: got %d %s", q, rec.Code, body)
		}
	}
}
