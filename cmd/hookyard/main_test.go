package main

import (
	"bytes"
	"context"
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

func TestMigrateRejectsUnknownAction(t *testing.T) {
	args := []string{"migrate", "-database-url", "postgres://localhost/hookyard", "down"}
	err := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `unknown migrate action "down"`) {
		t.Fatalf("expected unknown action error, got %v", err)
	}
}
