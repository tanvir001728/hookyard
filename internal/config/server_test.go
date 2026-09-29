package config

import (
	"reflect"
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
	if !reflect.DeepEqual(cfg, DefaultServer()) {
		t.Errorf("got %+v, want defaults %+v", cfg, DefaultServer())
	}
}

func TestServerFromEnvOverrides(t *testing.T) {
	cfg, err := ServerFromEnv(env(map[string]string{
		"HOOKYARD_ADDR":             "127.0.0.1:9000",
		"HOOKYARD_LOG_LEVEL":        "debug",
		"HOOKYARD_LOG_FORMAT":       "JSON",
		"HOOKYARD_SHUTDOWN_TIMEOUT": "5s",
		"HOOKYARD_DATABASE_URL":     "postgres://localhost/hookyard",
		"HOOKYARD_AUTO_MIGRATE":     "false",
		"HOOKYARD_CONFIG":           "/etc/hookyard.yaml",
		"HOOKYARD_MAX_BODY_BYTES":   "2048",
		"HOOKYARD_WORKERS":          "8",
		"HOOKYARD_POLL_INTERVAL":    "250ms",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Server{
		Addr: "127.0.0.1:9000", LogLevel: "debug", LogFormat: logging.FormatJSON, ShutdownTimeout: 5 * time.Second,
		DatabaseURL: "postgres://localhost/hookyard", AutoMigrate: false, ConfigFile: "/etc/hookyard.yaml",
		MaxBodyBytes: 2048, Workers: 8, PollInterval: 250 * time.Millisecond,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

func TestServerFromEnvTokens(t *testing.T) {
	cfg, err := ServerFromEnv(env(map[string]string{"HOOKYARD_API_TOKENS": "orders:0123456789abcdef"}))
	if err != nil || len(cfg.APITokens) != 1 || cfg.APITokens[0].Name != "orders" {
		t.Fatalf("tokens = %+v, err = %v", cfg.APITokens, err)
	}
	if _, err := ServerFromEnv(env(map[string]string{"HOOKYARD_API_TOKENS": "short"})); err == nil || !strings.Contains(err.Error(), "HOOKYARD_API_TOKENS") {
		t.Errorf("invalid tokens should name the variable: %v", err)
	}
}

func TestServerValidateReportsAllErrors(t *testing.T) {
	err := Server{Addr: " ", LogLevel: "loud", LogFormat: "xml", ShutdownTimeout: 0, MaxBodyBytes: 1, Workers: 0, PollInterval: time.Hour}.ValidateServe()
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"listen address", "log level", "log format", "shutdown timeout", "HOOKYARD_DATABASE_URL", "HOOKYARD_API_TOKENS", "HOOKYARD_MAX_BODY_BYTES", "HOOKYARD_WORKERS", "HOOKYARD_POLL_INTERVAL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestServerFromEnvBadValues(t *testing.T) {
	for key, val := range map[string]string{"HOOKYARD_SHUTDOWN_TIMEOUT": "soon", "HOOKYARD_AUTO_MIGRATE": "maybe", "HOOKYARD_MAX_BODY_BYTES": "lots", "HOOKYARD_WORKERS": "many", "HOOKYARD_POLL_INTERVAL": "often"} {
		_, err := ServerFromEnv(env(map[string]string{key: val}))
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s=%s: expected error naming the variable, got %v", key, val, err)
		}
	}
}

func TestValidateLevels(t *testing.T) {
	cfg := DefaultServer()
	if err := cfg.Validate(); err == nil {
		t.Fatal("defaults must not validate without a database URL")
	}

	cfg.DatabaseURL = "postgres://localhost/hookyard"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a database URL is enough for non-server commands such as migrate: %v", err)
	}
	if err := cfg.ValidateServe(); err == nil || !strings.Contains(err.Error(), "HOOKYARD_API_TOKENS") {
		t.Fatalf("serve must require an API token: %v", err)
	}

	cfg.APITokens = []APIToken{{Name: "a", Secret: "0123456789abcdef"}}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("defaults with a database URL and token should be valid: %v", err)
	}
}
