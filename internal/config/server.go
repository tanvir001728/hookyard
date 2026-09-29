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
	// APITokens authenticate API clients. At least one is required.
	APITokens []APIToken
	// MaxBodyBytes limits the size of API request bodies.
	MaxBodyBytes int64
	// Workers is the maximum number of concurrent deliveries.
	Workers int
	// PollInterval is how often the queue is checked when idle.
	PollInterval time.Duration
}

// DefaultMaxBodyBytes is the default limit for API request bodies (1 MiB).
const DefaultMaxBodyBytes = 1 << 20

// DefaultServer returns the built-in defaults.
func DefaultServer() Server {
	return Server{
		Addr:            ":8080",
		LogLevel:        "info",
		LogFormat:       logging.FormatText,
		ShutdownTimeout: 30 * time.Second,
		AutoMigrate:     true,
		MaxBodyBytes:    DefaultMaxBodyBytes,
		Workers:         32,
		PollInterval:    time.Second,
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
	if v, ok := lookup("HOOKYARD_API_TOKENS"); ok {
		tokens, err := ParseAPITokens(v)
		if err != nil {
			return cfg, fmt.Errorf("HOOKYARD_API_TOKENS: %w", err)
		}
		cfg.APITokens = tokens
	}
	if v, ok := lookup("HOOKYARD_MAX_BODY_BYTES"); ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return cfg, fmt.Errorf("HOOKYARD_MAX_BODY_BYTES: %w", err)
		}
		cfg.MaxBodyBytes = n
	}
	if v, ok := lookup("HOOKYARD_WORKERS"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return cfg, fmt.Errorf("HOOKYARD_WORKERS: %w", err)
		}
		cfg.Workers = n
	}
	if v, ok := lookup("HOOKYARD_POLL_INTERVAL"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return cfg, fmt.Errorf("HOOKYARD_POLL_INTERVAL: %w", err)
		}
		cfg.PollInterval = d
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

// Validate checks the settings every command needs: the database and logging.
// It reports every problem at once.
func (s Server) Validate() error {
	var errs []error
	if _, err := logging.ParseLevel(s.LogLevel); err != nil {
		errs = append(errs, err)
	}
	if s.LogFormat != logging.FormatText && s.LogFormat != logging.FormatJSON {
		errs = append(errs, fmt.Errorf("unknown log format %q (want text or json)", s.LogFormat))
	}
	if strings.TrimSpace(s.DatabaseURL) == "" {
		errs = append(errs, errors.New("database url is required: set HOOKYARD_DATABASE_URL, for example postgres://user:pass@localhost:5432/hookyard"))
	}
	return errors.Join(errs...)
}

// ValidateServe checks everything Validate does plus the settings only the
// server needs, such as the listen address and API tokens.
func (s Server) ValidateServe() error {
	errs := []error{s.Validate()}
	if strings.TrimSpace(s.Addr) == "" {
		errs = append(errs, errors.New("listen address must not be empty"))
	}
	if s.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("shutdown timeout must be positive"))
	}
	if len(s.APITokens) == 0 {
		errs = append(errs, errors.New("at least one API token is required: set HOOKYARD_API_TOKENS, for example HOOKYARD_API_TOKENS=\"orders:$(openssl rand -hex 32)\""))
	}
	if s.Workers < 1 || s.Workers > 1024 {
		errs = append(errs, errors.New("workers must be between 1 and 1024 (HOOKYARD_WORKERS)"))
	}
	if s.PollInterval < 10*time.Millisecond || s.PollInterval > time.Minute {
		errs = append(errs, errors.New("poll interval must be between 10ms and 1m (HOOKYARD_POLL_INTERVAL)"))
	}
	if s.MaxBodyBytes < 1024 || s.MaxBodyBytes > 64<<20 {
		errs = append(errs, errors.New("max body size must be between 1 KiB and 64 MiB (HOOKYARD_MAX_BODY_BYTES)"))
	}
	return errors.Join(errs...)
}
