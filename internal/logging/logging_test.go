package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"debug", slog.LevelDebug, false},
		{"INFO", slog.LevelInfo, false},
		{"", slog.LevelInfo, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"verbose", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseLevel(tt.in)
		if (err != nil) != tt.wantErr {
			t.Fatalf("ParseLevel(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestNewJSON(t *testing.T) {
	var buf bytes.Buffer
	log, err := New(&buf, FormatJSON, "info")
	if err != nil {
		t.Fatal(err)
	}
	log.Debug("hidden")
	log.Info("hello", "upstream", "courier-x")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want 1 (debug should be filtered): %q", len(lines), buf.String())
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if rec["msg"] != "hello" || rec["upstream"] != "courier-x" {
		t.Errorf("unexpected record: %v", rec)
	}
}

func TestNewUnknownFormat(t *testing.T) {
	if _, err := New(&bytes.Buffer{}, "xml", "info"); err == nil {
		t.Fatal("expected error for unknown format")
	}
}
