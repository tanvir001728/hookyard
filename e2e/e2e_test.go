//go:build e2e

// Package e2e runs the real hookyard and flakyvendor binaries against Postgres
// and drives them over HTTP, including crashing and restarting the server.
//
//	go test -tags e2e ./e2e/
//
// Postgres comes from testcontainers (Docker required) unless
// HOOKYARD_TEST_DATABASE_URL points at a server.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/store/storetest"
)

const token = "e2e-token-0123456789"

var stack struct {
	bin      string // directory with the built binaries
	dbURL    string
	config   string
	addr     string
	vendor   string
	hookyard *exec.Cmd
	logs     *bytes.Buffer
}

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "hookyard-e2e")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	stack.bin = dir

	for _, cmd := range []string{"hookyard", "flakyvendor"} {
		build := exec.CommandContext(context.Background(), "go", "build", "-o", filepath.Join(dir, cmd), "../cmd/"+cmd)
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "build", cmd, err)
			return 1
		}
	}

	dbURL, drop, err := storetest.NewDatabase(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not create a test database:", err)
		return 1
	}
	defer drop() // runs after hookyard is stopped (defers run in reverse order)
	stack.dbURL = dbURL

	stack.vendor = freeAddr()
	vendor := exec.CommandContext(context.Background(), filepath.Join(dir, "flakyvendor"), "-addr", stack.vendor)
	if err := vendor.Start(); err != nil {
		panic(err)
	}
	defer func() { _ = vendor.Process.Kill() }()

	stack.config = filepath.Join(dir, "hookyard.yaml")
	cfg := fmt.Sprintf("upstreams:\n  flaky:\n    base_url: http://%s\n    timeout: 3s\n    retry: { preset: quick, max_attempts: 5, initial_interval: 100ms, max_interval: 200ms }\n", stack.vendor)
	if err := os.WriteFile(stack.config, []byte(cfg), 0o600); err != nil {
		panic(err)
	}
	stack.addr = freeAddr()
	if err := startHookyard(); err != nil {
		fmt.Fprintln(os.Stderr, err, stack.logs)
		return 1
	}
	defer stopHookyard(syscall.SIGKILL)

	code := m.Run()
	if code != 0 {
		fmt.Fprintln(os.Stderr, "--- hookyard logs ---\n", stack.logs)
	}
	return code
}

func startHookyard() error {
	stack.logs = &bytes.Buffer{}
	cmd := exec.CommandContext(context.Background(), filepath.Join(stack.bin, "hookyard"), "serve")
	cmd.Env = append(os.Environ(),
		"HOOKYARD_ADDR="+stack.addr,
		"HOOKYARD_DATABASE_URL="+stack.dbURL,
		"HOOKYARD_CONFIG="+stack.config,
		"HOOKYARD_API_TOKENS=e2e:"+token,
		"HOOKYARD_LEASE_MARGIN=1s",
		"HOOKYARD_POLL_INTERVAL=50ms",
		"HOOKYARD_SHUTDOWN_TIMEOUT=10s",
		"HOOKYARD_DASHBOARD=false",
	)
	cmd.Stdout, cmd.Stderr = stack.logs, stack.logs
	if err := cmd.Start(); err != nil {
		return err
	}
	stack.hookyard = cmd
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if status, _ := get("http://" + stack.addr + "/readyz"); status == http.StatusOK {
			return nil
		}
	}
	return fmt.Errorf("hookyard did not become ready")
}

// stopHookyard sends sig and waits for the process to exit.
func stopHookyard(sig syscall.Signal) {
	if stack.hookyard == nil {
		return
	}
	_ = stack.hookyard.Process.Signal(sig)
	_ = stack.hookyard.Wait()
	stack.hookyard = nil
}

// get fetches url and returns the status code and body.
func get(url string) (int, []byte) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return 0, nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func freeAddr() string {
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return l.Addr().String()
}

type request struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	AttemptCount int    `json:"attempt_count"`
	LastError    *struct {
		Code string `json:"code"`
	} `json:"last_error"`
}

// call sends an authenticated API request, decodes the response into out (if
// set) and returns the status code and headers.
func call(t *testing.T, method, path string, body any, out any) (int, http.Header) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(t.Context(), method, "http://"+stack.addr+path, r)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return resp.StatusCode, resp.Header
}

func enqueue(t *testing.T, body map[string]any) request {
	t.Helper()
	body["upstream"] = "flaky"
	if body["method"] == nil {
		body["method"] = "POST"
	}
	var req request
	if status, _ := call(t, http.MethodPost, "/v1/requests", body, &req); status != http.StatusAccepted {
		t.Fatalf("enqueue: status %d", status)
	}
	return req
}

func waitStatus(t *testing.T, id, want string, timeout time.Duration) request {
	t.Helper()
	var req request
	for deadline := time.Now().Add(timeout); ; time.Sleep(50 * time.Millisecond) {
		call(t, http.MethodGet, "/v1/requests/"+id, nil, &req)
		if req.Status == want {
			return req
		}
		if time.Now().After(deadline) {
			t.Fatalf("request %s is %s after %s, want %s", id, req.Status, timeout, want)
		}
	}
}

// vendorAttempts returns when flakyvendor received each request for key.
func vendorAttempts(t *testing.T, key string) []time.Time {
	t.Helper()
	status, body := get("http://" + stack.vendor + "/_requests")
	if status != http.StatusOK {
		t.Fatalf("flakyvendor /_requests: status %d", status)
	}
	var list struct {
		Data []struct {
			At    time.Time `json:"at"`
			Query string    `json:"query"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	var out []time.Time
	for _, r := range list.Data {
		if bytes.Contains([]byte(r.Query), []byte("key="+key)) {
			out = append(out, r.At)
		}
	}
	return out
}

func unique(t *testing.T) string {
	return t.Name() + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func TestRetryUntilSuccess(t *testing.T) {
	key := unique(t)
	req := enqueue(t, map[string]any{"path": "/orders?fail_first=2&key=" + key})
	done := waitStatus(t, req.ID, "succeeded", 10*time.Second)
	if done.AttemptCount != 3 || len(vendorAttempts(t, key)) != 3 {
		t.Errorf("attempts: hookyard %d, vendor %d; want 3", done.AttemptCount, len(vendorAttempts(t, key)))
	}
}

func TestRetryAfterIsHonored(t *testing.T) {
	key := unique(t)
	req := enqueue(t, map[string]any{"path": "/orders?fail_first=1&retry_after=2&key=" + key})
	waitStatus(t, req.ID, "succeeded", 10*time.Second)
	at := vendorAttempts(t, key)
	if len(at) != 2 || at[1].Sub(at[0]) < 1900*time.Millisecond {
		t.Errorf("attempts at %v: want the retry at least 2s after the first (Retry-After)", at)
	}
}

func TestDeadLetterAndReplay(t *testing.T) {
	key := unique(t)
	// Fails 3 times; max 2 attempts per run, so it dies, and the replay's
	// fresh budget (attempts 3 and 4) gets it through.
	req := enqueue(t, map[string]any{
		"path":  "/orders?fail_first=3&key=" + key,
		"retry": map[string]any{"max_attempts": 2, "initial_interval": "100ms", "max_interval": "200ms"},
	})
	dead := waitStatus(t, req.ID, "dead", 10*time.Second)
	if dead.AttemptCount != 2 || dead.LastError == nil || dead.LastError.Code != "http_status" {
		t.Fatalf("dead request: %+v", dead)
	}

	var result struct{ Matched, Replayed int }
	call(t, http.MethodPost, "/v1/dlq/replay", map[string]any{"ids": []string{req.ID}}, &result)
	if result.Replayed != 1 {
		t.Fatalf("replay: %+v", result)
	}
	done := waitStatus(t, req.ID, "succeeded", 10*time.Second)
	if done.AttemptCount != 4 {
		t.Errorf("attempt_count = %d, want 4 (2 before and 2 after the replay)", done.AttemptCount)
	}
}

func TestPermanentFailureGoesStraightToDLQ(t *testing.T) {
	req := enqueue(t, map[string]any{"path": "/orders?status=422&key=" + unique(t)})
	if dead := waitStatus(t, req.ID, "dead", 10*time.Second); dead.AttemptCount != 1 {
		t.Errorf("a 422 must not be retried: %d attempts", dead.AttemptCount)
	}
}

func TestDedupe(t *testing.T) {
	key := unique(t)
	body := map[string]any{"upstream": "flaky", "method": "POST", "path": "/orders?key=" + key, "dedupe_key": key}
	var first, second request
	call(t, http.MethodPost, "/v1/requests", body, &first)
	status, header := call(t, http.MethodPost, "/v1/requests", body, &second)
	if status != http.StatusOK || header.Get("Hookyard-Deduplicated") != "true" || second.ID != first.ID {
		t.Fatalf("duplicate enqueue: status %d, ids %s/%s", status, first.ID, second.ID)
	}
	waitStatus(t, first.ID, "succeeded", 10*time.Second)
	if n := len(vendorAttempts(t, key)); n != 1 {
		t.Errorf("vendor received %d requests, want 1", n)
	}
}

// TestCrashRecovery kills Hookyard with SIGKILL while a delivery is in
// progress. After a restart, the request's lease expires and it is delivered
// again: nothing is lost (delivery is at-least-once).
func TestCrashRecovery(t *testing.T) {
	key := unique(t)
	req := enqueue(t, map[string]any{"path": "/orders?latency=2s&key=" + key, "timeout": "2500ms"})

	for deadline := time.Now().Add(5 * time.Second); len(vendorAttempts(t, key)) == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("delivery never started")
		}
	}
	stopHookyard(syscall.SIGKILL)

	// The lease is the timeout (2.5s) plus the 1s margin.
	time.Sleep(4 * time.Second)
	if err := startHookyard(); err != nil {
		t.Fatal(err)
	}
	done := waitStatus(t, req.ID, "succeeded", 15*time.Second)
	if done.AttemptCount != 1 {
		t.Errorf("attempt_count = %d, want 1: the interrupted attempt was never recorded", done.AttemptCount)
	}
	if n := len(vendorAttempts(t, key)); n != 2 {
		t.Errorf("vendor received %d requests, want 2 (the interrupted one and the redelivery)", n)
	}
}

// TestGracefulShutdown sends SIGTERM during a delivery: the process waits for
// it to finish and records the result before exiting.
func TestGracefulShutdown(t *testing.T) {
	key := unique(t)
	req := enqueue(t, map[string]any{"path": "/orders?latency=1s&key=" + key})
	for deadline := time.Now().Add(5 * time.Second); len(vendorAttempts(t, key)) == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("delivery never started")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	exited := make(chan struct{})
	go func() {
		stopHookyard(syscall.SIGTERM)
		close(exited)
	}()
	select {
	case <-exited:
	case <-ctx.Done():
		t.Fatal("hookyard did not exit after SIGTERM")
	}

	if err := startHookyard(); err != nil {
		t.Fatal(err)
	}
	var got request
	call(t, http.MethodGet, "/v1/requests/"+req.ID, nil, &got)
	if got.Status != "succeeded" || len(vendorAttempts(t, key)) != 1 {
		t.Errorf("after graceful shutdown: status %s, vendor received %d (want succeeded, 1)", got.Status, len(vendorAttempts(t, key)))
	}
}
