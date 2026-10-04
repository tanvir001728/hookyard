//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/callback"
)

// TestSignedCallback: the app is told when its request finishes, with a
// verifiable signature, and a failing app endpoint is retried until it
// accepts.
func TestSignedCallback(t *testing.T) {
	var (
		mu    sync.Mutex
		calls []*http.Request
		body  []byte
	)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r)
		body = b
		if len(calls) == 1 {
			w.WriteHeader(http.StatusBadGateway) // the app is briefly down
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer app.Close()

	req := enqueue(t, map[string]any{
		"path":         "/orders?fail_first=1&key=" + unique(t),
		"callback_url": app.URL + "/hooks/hookyard",
		"on_result":    "order.shipment",
	})
	waitStatus(t, req.ID, "succeeded", 10*time.Second)

	var list struct {
		Data []struct {
			ID           string `json:"id"`
			Status       string `json:"status"`
			AttemptCount int    `json:"attempt_count"`
		} `json:"data"`
	}
	// The first callback attempt fails; the retry comes after the backoff.
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		call(t, http.MethodGet, "/v1/requests/"+req.ID+"/callbacks", nil, &list)
		if len(list.Data) == 1 && list.Data[0].Status == "delivered" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("callback not delivered: %+v", list)
		}
	}
	if list.Data[0].AttemptCount != 2 {
		t.Errorf("attempts = %d, want 2", list.Data[0].AttemptCount)
	}

	mu.Lock()
	defer mu.Unlock()
	last := calls[len(calls)-1]
	secrets, _ := callback.ParseSecrets(callbackSecret)
	if err := callback.Verify(secrets, last.Header.Get("webhook-id"), last.Header.Get("webhook-timestamp"), last.Header.Get("webhook-signature"), body, time.Now(), 5*time.Minute); err != nil {
		t.Errorf("signature: %v", err)
	}
	if last.Header.Get("webhook-id") != list.Data[0].ID || calls[0].Header.Get("webhook-id") != list.Data[0].ID {
		t.Error("every attempt must carry the callback's id")
	}
	var e callback.Event
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatal(err)
	}
	if e.Type != "request.succeeded" || e.Data.RequestID != req.ID || e.Data.AttemptCount != 2 || *e.Data.OnResult != "order.shipment" || e.Data.Response.StatusCode != http.StatusOK {
		t.Errorf("event = %s", body)
	}
}
