package config

import (
	"strings"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/logging"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestServerFromEnvDefaults(t *testing.T) {
	cfg, err := ServerFromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg != DefaultServer() {
		t.Errorf("got %+v, want defaults %+v", cfg, DefaultServer())
	}
}

func TestServerFromEnvOverrides(t *testing.T) {
	cfg, err := ServerFromEnv(env(map[string]string{
		"HOOKYARD_ADDR":             "127.0.0.1:9000",
		"HOOKYARD_LOG_LEVEL":        "debug",
		"HOOKYARD_LOG_FORMAT":       "JSON",
		"HOOKYARD_SHUTDOWN_TIMEOUT": "5s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Server{Addr: "127.0.0.1:9000", LogLevel: "debug", LogFormat: logging.FormatJSON, ShutdownTimeout: 5 * time.Second}
	if cfg != want {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

func TestServerValidateReportsAllErrors(t *testing.T) {
	err := Server{Addr: " ", LogLevel: "loud", LogFormat: "xml", ShutdownTimeout: 0}.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"listen address", "log level", "log format", "shutdown timeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestServerFromEnvBadDuration(t *testing.T) {
	_, err := ServerFromEnv(env(map[string]string{"HOOKYARD_SHUTDOWN_TIMEOUT": "soon"}))
	if err == nil || !strings.Contains(err.Error(), "HOOKYARD_SHUTDOWN_TIMEOUT") {
		t.Fatalf("expected error naming the variable, got %v", err)
	}
}
