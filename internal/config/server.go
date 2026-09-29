// Package config loads Hookyard's configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tanvir001728/hookyard/internal/logging"
)

// Server holds process-level settings. Values come from HOOKYARD_* environment
// variables and can be overridden by command-line flags.
type Server struct {
	// Addr is the address the HTTP server listens on.
	Addr string
	// LogLevel is one of debug, info, warn or error.
	LogLevel string
	// LogFormat is text or json.
	LogFormat logging.Format
	// ShutdownTimeout bounds how long graceful shutdown may take.
	ShutdownTimeout time.Duration
}

// DefaultServer returns the built-in defaults.
func DefaultServer() Server {
	return Server{
		Addr:            ":8080",
		LogLevel:        "info",
		LogFormat:       logging.FormatText,
		ShutdownTimeout: 30 * time.Second,
	}
}

// ServerFromEnv returns the defaults overridden by environment variables.
// lookup is usually os.LookupEnv; it is injectable for tests.
func ServerFromEnv(lookup func(string) (string, bool)) (Server, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	cfg := DefaultServer()
	if v, ok := lookup("HOOKYARD_ADDR"); ok {
		cfg.Addr = v
	}
	if v, ok := lookup("HOOKYARD_LOG_LEVEL"); ok {
		cfg.LogLevel = v
	}
	if v, ok := lookup("HOOKYARD_LOG_FORMAT"); ok {
		cfg.LogFormat = logging.Format(strings.ToLower(v))
	}
	if v, ok := lookup("HOOKYARD_SHUTDOWN_TIMEOUT"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return cfg, fmt.Errorf("HOOKYARD_SHUTDOWN_TIMEOUT: %w", err)
		}
		cfg.ShutdownTimeout = d
	}
	return cfg, cfg.Validate()
}

// Validate reports every invalid setting at once.
func (s Server) Validate() error {
	var errs []error
	if strings.TrimSpace(s.Addr) == "" {
		errs = append(errs, errors.New("listen address must not be empty"))
	}
	if _, err := logging.ParseLevel(s.LogLevel); err != nil {
		errs = append(errs, err)
	}
	if s.LogFormat != logging.FormatText && s.LogFormat != logging.FormatJSON {
		errs = append(errs, fmt.Errorf("unknown log format %q (want text or json)", s.LogFormat))
	}
	if s.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("shutdown timeout must be positive"))
	}
	return errors.Join(errs...)
}
