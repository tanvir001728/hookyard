// Command flakyvendor runs a deliberately unreliable HTTP API for testing and
// demonstrating Hookyard. See the flakyvendor package for the query
// parameters that control its behavior.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tanvir001728/hookyard/internal/flakyvendor"
)

const usage = `flakyvendor: a deliberately unreliable HTTP API for testing Hookyard.

Usage:
  flakyvendor [-addr :9090]

Control behavior with query parameters on any path:
  status=N          always respond with status N
  fail_rate=F       fail a fraction F (0..1) of requests
  fail_first=N      fail the first N requests for the same key, then succeed
  key=K             counter key for fail_first (default: method and path)
  fail_status=N     status used for failures (default 503)
  retry_after=S     add Retry-After: S to failures
  fake_error=1      respond 200 OK with an error in the body
  latency=D         wait D before responding (for example 200ms)
  hang=1            never respond

Examples:
  curl -X POST 'localhost:9090/orders?fail_first=2'     # 503, 503, then 200
  curl 'localhost:9090/orders?status=429&retry_after=5'
  curl localhost:9090/_requests                          # list received requests
  curl -X DELETE localhost:9090/_requests                # reset

Flags:
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("flakyvendor", flag.ContinueOnError)
	addr := fs.String("addr", ":9090", "listen address")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: *addr, Handler: flakyvendor.New(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("flakyvendor listening", "addr", *addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
