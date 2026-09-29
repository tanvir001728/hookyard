// Package config loads Hookyard's configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
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
	// DatabaseURL is the Postgres connection string.
	DatabaseURL string
	// AutoMigrate applies pending database migrations on startup.
	AutoMigrate bool
	// ConfigFile is the path of hookyard.yaml. Empty means DefaultConfigFile
	// if it exists.
	ConfigFile string
}

// DefaultServer returns the built-in defaults.
func DefaultServer() Server {
	return Server{
		Addr:            ":8080",
		LogLevel:        "info",
		LogFormat:       logging.FormatText,
		ShutdownTimeout: 30 * time.Second,
		AutoMigrate:     true,
	}
}

// ServerFromEnv returns the defaults overridden by environment variables. It
// does not validate the result, so flags can still fill in missing values;
// call Validate afterwards. lookup is usually os.LookupEnv.
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
	if v, ok := lookup("HOOKYARD_CONFIG"); ok {
		cfg.ConfigFile = v
	}
	if v, ok := lookup("HOOKYARD_DATABASE_URL"); ok {
		cfg.DatabaseURL = v
	}
	if v, ok := lookup("HOOKYARD_AUTO_MIGRATE"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("HOOKYARD_AUTO_MIGRATE: %w", err)
		}
		cfg.AutoMigrate = b
	}
	return cfg, nil
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
	if strings.TrimSpace(s.DatabaseURL) == "" {
		errs = append(errs, errors.New("database url is required: set HOOKYARD_DATABASE_URL, for example postgres://user:pass@localhost:5432/hookyard"))
	}
	return errors.Join(errs...)
}
