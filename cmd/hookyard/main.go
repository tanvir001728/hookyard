// Command hookyard runs the Hookyard server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tanvir001728/hookyard/internal/api"
	"github.com/tanvir001728/hookyard/internal/config"
	"github.com/tanvir001728/hookyard/internal/logging"
	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/retention"
	"github.com/tanvir001728/hookyard/internal/stats"
	"github.com/tanvir001728/hookyard/internal/store"
	"github.com/tanvir001728/hookyard/internal/version"
	"github.com/tanvir001728/hookyard/internal/worker"
	"github.com/tanvir001728/hookyard/web"
)

const usage = `Hookyard: reliable delivery for outbound API calls.

Usage:
  hookyard <command> [flags]

Commands:
  serve     Run the Hookyard server
  migrate   Apply database migrations, or show their status
  validate  Check a hookyard.yaml config file
  healthcheck  Exit 0 if a running Hookyard is ready (for container health checks)
  version   Print version information

Run "hookyard <command> -h" for command flags.
`

func main() {
	if err := mainErr(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func mainErr() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, os.Args[1:], os.Stdout, os.Stderr)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("no command given")
	}
	switch args[0] {
	case "serve":
		return serve(ctx, args[1:], stderr)
	case "migrate":
		return migrate(ctx, args[1:], stdout, stderr)
	case "validate":
		return validate(args[1:], stdout, stderr)
	case "healthcheck":
		return healthcheck(ctx, args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version.String())
		return nil
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// loadConfig reads settings from the environment, then applies flags shared
// by all commands plus any command-specific flags registered by extra. The
// result is checked with Validate; serve additionally calls ValidateServe.
func loadConfig(name string, args []string, stderr io.Writer, extra func(*flag.FlagSet, *config.Server)) (config.Server, *flag.FlagSet, error) {
	cfg, err := config.ServerFromEnv(nil)
	if err != nil {
		return cfg, nil, fmt.Errorf("invalid configuration: %w", err)
	}

	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.DatabaseURL, "database-url", cfg.DatabaseURL, "Postgres connection string (env HOOKYARD_DATABASE_URL)")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level: debug, info, warn, error (env HOOKYARD_LOG_LEVEL)")
	fs.Func("log-format", "log format: text or json (env HOOKYARD_LOG_FORMAT)", func(v string) error {
		cfg.LogFormat = logging.Format(v)
		return nil
	})
	if extra != nil {
		extra(fs, &cfg)
	}
	if err := fs.Parse(args); err != nil {
		return cfg, fs, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fs, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, fs, nil
}

func openStore(ctx context.Context, cfg config.Server) (*store.Store, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return store.Open(ctx, cfg.DatabaseURL, store.Options{})
}

func serve(ctx context.Context, args []string, stderr io.Writer) error {
	cfg, _, err := loadConfig("serve", args, stderr, func(fs *flag.FlagSet, cfg *config.Server) {
		fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "listen address (env HOOKYARD_ADDR)")
		fs.DurationVar(&cfg.ShutdownTimeout, "shutdown-timeout", cfg.ShutdownTimeout, "graceful shutdown timeout (env HOOKYARD_SHUTDOWN_TIMEOUT)")
		fs.BoolVar(&cfg.AutoMigrate, "auto-migrate", cfg.AutoMigrate, "apply database migrations on startup (env HOOKYARD_AUTO_MIGRATE)")
		fs.StringVar(&cfg.ConfigFile, "config", cfg.ConfigFile, "path to hookyard.yaml (env HOOKYARD_CONFIG)")
	})
	if err != nil {
		return err
	}
	if err := cfg.ValidateServe(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	log, err := logging.New(stderr, cfg.LogFormat, cfg.LogLevel)
	if err != nil {
		return err
	}

	file, err := config.LoadFile(cfg.ConfigFile, nil)
	if err != nil {
		return err
	}
	if file.Path == "" {
		log.Warn("no config file found; no upstreams are configured", "hint", "create hookyard.yaml or set HOOKYARD_CONFIG")
	} else {
		log.Info("loaded config", "file", file.Path, "upstreams", file.Upstreams.Names())
	}

	db, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	if cfg.AutoMigrate {
		if err := applyMigrations(ctx, db, log); err != nil {
			return err
		}
	}

	collector := stats.NewCollector(log, db)
	engine := worker.New(log, db, file.Upstreams, worker.Config{
		Workers:      cfg.Workers,
		PollInterval: cfg.PollInterval,
		LeaseMargin:  cfg.LeaseMargin,
		DrainTimeout: cfg.ShutdownTimeout,
		Observer:     collector.Observe,
	})
	engineCtx, stopEngine := context.WithCancel(ctx)
	defer stopEngine()
	engineDone := make(chan error, 1)
	go func() { engineDone <- engine.Run(engineCtx) }()

	// Background maintenance stops with the process; the collector flushes
	// once more after the engine has drained.
	collectorCtx, stopCollector := context.WithCancel(context.WithoutCancel(ctx))
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		collector.Run(collectorCtx, 10*time.Second)
	}()
	go retention.Run(engineCtx, log, db, retention.Config{Requests: cfg.RequestRetention})

	apiOpts := []api.Option{
		api.WithReadinessCheck("database", db.Ping),
		api.WithV1(api.V1{Store: db, Config: file, Tokens: cfg.APITokens, MaxBody: cfg.MaxBodyBytes, Notify: engine.Notify, Monitor: engine}),
	}
	if cfg.Metrics {
		apiOpts = append(apiOpts, api.WithMetrics(collector.MetricsHandler(upstreamGauges(db, file.Upstreams, engine))))
	}
	if cfg.Dashboard {
		apiOpts = append(apiOpts, api.WithDashboard(web.Handler()))
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(log, apiOpts...),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("hookyard listening", "addr", cfg.Addr, "version", version.Version)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		stopEngine()
		<-engineDone
		stopCollector()
		<-collectorDone
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	log.Info("shutting down", "timeout", cfg.ShutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()
	// The engine drains in-flight deliveries (bounded by the same timeout)
	// while the HTTP server finishes open requests.
	httpErr := srv.Shutdown(shutdownCtx)
	engineErr := <-engineDone
	stopCollector()
	<-collectorDone
	if err := errors.Join(httpErr, engineErr); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("shutdown complete")
	return nil
}

// upstreamGauges combines queue sizes and pauses from the database with the
// engine's live breaker state, for the Prometheus endpoint.
func upstreamGauges(db *store.Store, upstreams *config.Registry, engine *worker.Engine) stats.QueueGauges {
	return func(ctx context.Context) (map[string]stats.QueueGauge, error) {
		q, err := db.QueueStats(ctx)
		if err != nil {
			return nil, err
		}
		pauses, err := db.ListPauses(ctx)
		if err != nil {
			return nil, err
		}
		out := make(map[string]stats.QueueGauge, upstreams.Len())
		for _, name := range upstreams.Names() {
			g := stats.QueueGauge{Waiting: q[name].Waiting, Dead: q[name].Dead}
			_, g.Paused = pauses[name]
			if live, ok := engine.UpstreamLive(name); ok {
				switch live.Breaker {
				case "open":
					g.BreakerOpen = 1
				case "half_open":
					g.BreakerOpen = 0.5
				}
			}
			out[name] = g
		}
		return out, nil
	}
}

func applyMigrations(ctx context.Context, db *store.Store, log *slog.Logger) error {
	applied, err := db.Migrate(ctx)
	if err != nil {
		return err
	}
	if len(applied) > 0 {
		log.Info("applied database migrations", "versions", applied)
	} else {
		log.Debug("database schema is up to date")
	}
	return nil
}

const migrateUsage = `Usage:
  hookyard migrate [flags] [up|status]

  up       Apply all pending migrations (default)
  status   List migrations and whether they are applied

Flags:
`

func migrate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cfg, fs, err := loadConfig("migrate", args, stderr, func(fs *flag.FlagSet, _ *config.Server) {
		fs.Usage = func() {
			fmt.Fprint(fs.Output(), migrateUsage)
			fs.PrintDefaults()
		}
	})
	if err != nil {
		return err
	}

	action := "up"
	if fs.NArg() > 0 {
		action = fs.Arg(0)
	}
	if fs.NArg() > 1 || (action != "up" && action != "status") {
		fs.Usage()
		return fmt.Errorf("unknown migrate action %q", strings.Join(fs.Args(), " "))
	}

	log, err := logging.New(stderr, cfg.LogFormat, cfg.LogLevel)
	if err != nil {
		return err
	}
	db, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	if action == "up" {
		applied, err := db.Migrate(ctx)
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			fmt.Fprintln(stdout, "Database schema is up to date.")
			return nil
		}
		for _, v := range applied {
			fmt.Fprintf(stdout, "Applied migration %05d\n", v)
		}
		return nil
	}

	statuses, err := db.MigrationStatuses(ctx)
	if err != nil {
		return err
	}
	for _, st := range statuses {
		state := "pending"
		if st.Applied {
			state = "applied"
		}
		fmt.Fprintf(stdout, "%05d  %-8s  %s\n", st.Version, state, st.Name)
	}
	log.Debug("migration status listed", "count", len(statuses))
	return nil
}

func validate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", os.Getenv("HOOKYARD_CONFIG"), "path to hookyard.yaml (env HOOKYARD_CONFIG)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage:\n  hookyard validate [-config hookyard.yaml]\n\nChecks a config file, including that referenced environment variables are set.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		*path = config.DefaultConfigFile
	}

	file, err := config.LoadFile(*path, nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s is valid: %d upstream(s)\n", file.Path, file.Upstreams.Len())
	for _, u := range file.Upstreams.All() {
		fmt.Fprintf(stdout, "  %-20s %s (timeout %s, retry %s)\n", u.Name, u.BaseURL, model.FormatDuration(u.Timeout), retryLabel(u.Retry))
	}
	return nil
}

func retryLabel(p model.RetryPolicy) string {
	if p.Preset != "" {
		return fmt.Sprintf("%s, %d attempts", p.Preset, p.MaxAttempts)
	}
	return fmt.Sprintf("custom, %d attempts", p.MaxAttempts)
}

// healthcheck queries /readyz of a running server. Container images without
// curl use it for their health check.
func healthcheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	defaultURL := "http://127.0.0.1:8080/readyz"
	if addr := os.Getenv("HOOKYARD_ADDR"); addr != "" {
		host, port, err := net.SplitHostPort(addr)
		if err == nil {
			if host == "" || host == "0.0.0.0" || host == "::" {
				host = "127.0.0.1"
			}
			defaultURL = "http://" + net.JoinHostPort(host, port) + "/readyz"
		}
	}
	url := fs.String("url", defaultURL, "readiness URL to check")
	timeout := fs.Duration("timeout", 3*time.Second, "how long to wait for a response")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("not ready: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("not ready: %s returned %d", *url, resp.StatusCode)
	}
	fmt.Fprintln(stdout, "ready")
	return nil
}
