package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"version"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "hookyard ") {
		t.Errorf("unexpected version output %q", out.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var errOut bytes.Buffer
	err := run(context.Background(), []string{"launch"}, &bytes.Buffer{}, &errOut)
	if err == nil || !strings.Contains(err.Error(), `unknown command "launch"`) {
		t.Fatalf("expected unknown command error, got %v", err)
	}
	if !strings.Contains(errOut.String(), "Usage:") {
		t.Error("usage should be printed for unknown commands")
	}
}

func TestServeRejectsInvalidFlags(t *testing.T) {
	args := []string{"serve", "-database-url", "postgres://localhost/hookyard", "-log-format", "xml"}
	err := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "invalid configuration") {
		t.Fatalf("expected invalid configuration error, got %v", err)
	}
}

func TestServeRequiresDatabaseURL(t *testing.T) {
	t.Setenv("HOOKYARD_DATABASE_URL", "")
	err := run(context.Background(), []string{"serve"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "HOOKYARD_DATABASE_URL") {
		t.Fatalf("expected an error naming HOOKYARD_DATABASE_URL, got %v", err)
	}
}

func TestServeRequiresAPIToken(t *testing.T) {
	t.Setenv("HOOKYARD_API_TOKENS", "")
	args := []string{"serve", "-database-url", "postgres://localhost/hookyard"}
	err := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "HOOKYARD_API_TOKENS") {
		t.Fatalf("expected an error naming HOOKYARD_API_TOKENS, got %v", err)
	}
}

// migrate must not require server-only settings such as API tokens.
func TestMigrateRejectsUnknownAction(t *testing.T) {
	t.Setenv("HOOKYARD_API_TOKENS", "")
	args := []string{"migrate", "-database-url", "postgres://localhost/hookyard", "down"}
	err := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `unknown migrate action "down"`) {
		t.Fatalf("expected unknown action error, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(good, []byte("upstreams:\n  courier-x:\n    base_url: https://api.courier-x.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("upstreams:\n  courier-x:\n    base_url: nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := run(context.Background(), []string{"validate", "-config", good}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "is valid: 1 upstream(s)") || !strings.Contains(out.String(), "courier-x") {
		t.Errorf("unexpected output: %s", out.String())
	}

	err := run(context.Background(), []string{"validate", "-config", bad}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "upstreams.courier-x.base_url") {
		t.Errorf("expected base_url error, got %v", err)
	}
}

func TestHealthcheck(t *testing.T) {
	ready := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	if err := run(t.Context(), []string{"healthcheck", "-url", srv.URL}, &out, &bytes.Buffer{}); err != nil || out.String() != "ready\n" {
		t.Fatalf("ready server: err=%v out=%q", err, out.String())
	}
	ready = false
	if err := run(t.Context(), []string{"healthcheck", "-url", srv.URL}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("unready server: err=%v", err)
	}
	srv.Close()
	if err := run(t.Context(), []string{"healthcheck", "-url", srv.URL, "-timeout", "500ms"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("unreachable server must fail")
	}
}
