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
	err := run(context.Background(), []string{"serve", "-log-format", "xml"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "invalid configuration") {
		t.Fatalf("expected invalid configuration error, got %v", err)
	}
}
