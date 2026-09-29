// Command hookyard runs the Hookyard server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tanvir001728/hookyard/internal/api"
	"github.com/tanvir001728/hookyard/internal/config"
	"github.com/tanvir001728/hookyard/internal/logging"
	"github.com/tanvir001728/hookyard/internal/version"
)

const usage = `Hookyard: reliable delivery for outbound API calls.

Usage:
  hookyard <command> [flags]

Commands:
  serve     Run the Hookyard server
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

func serve(ctx context.Context, args []string, stderr io.Writer) error {
	cfg, err := config.ServerFromEnv(nil)
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "listen address (env HOOKYARD_ADDR)")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level: debug, info, warn, error (env HOOKYARD_LOG_LEVEL)")
	fs.Func("log-format", "log format: text or json (env HOOKYARD_LOG_FORMAT)", func(v string) error {
		cfg.LogFormat = logging.Format(v)
		return nil
	})
	fs.DurationVar(&cfg.ShutdownTimeout, "shutdown-timeout", cfg.ShutdownTimeout, "graceful shutdown timeout (env HOOKYARD_SHUTDOWN_TIMEOUT)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	log, err := logging.New(stderr, cfg.LogFormat, cfg.LogLevel)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(log),
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
