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
	"github.com/tanvir001728/hookyard/internal/store"
	"github.com/tanvir001728/hookyard/internal/version"
)

const usage = `Hookyard: reliable delivery for outbound API calls.

Usage:
  hookyard <command> [flags]

Commands:
  serve     Run the Hookyard server
  migrate   Apply database migrations, or show their status
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
// by all commands plus any command-specific flags registered by extra.
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
	})
	if err != nil {
		return err
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

	if cfg.AutoMigrate {
		if err := applyMigrations(ctx, db, log); err != nil {
			return err
		}
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(log, api.WithReadinessCheck("database", db.Ping)),
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
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	log.Info("shutting down", "timeout", cfg.ShutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("shutdown complete")
	return nil
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
