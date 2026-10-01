// Package storetest provides isolated, migrated Postgres databases for tests.
//
// By default it starts a Postgres container with testcontainers (Docker is
// required). Set HOOKYARD_TEST_DATABASE_URL to use an existing server instead;
// the user needs permission to create databases.
//
// Each test gets its own database, cloned from a migrated template, so tests
// can run in parallel without affecting each other.
package storetest

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/tanvir001728/hookyard/internal/store"
)

// EnvDatabaseURL names the variable pointing at an existing Postgres server.
const EnvDatabaseURL = "HOOKYARD_TEST_DATABASE_URL"

var (
	setupOnce sync.Once
	baseURL   string
	template  string
	setupErr  error
)

// New returns a Store backed by a fresh, fully migrated database that is
// dropped when the test ends.
func New(t testing.TB) *store.Store {
	t.Helper()
	s, _ := NewWithURL(t)
	return s
}

// NewWithURL is like New but also returns the database URL, for tests that
// need their own connections.
func NewWithURL(t testing.TB) (*store.Store, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping database test in -short mode")
	}

	dbURL, drop, err := NewDatabase(context.Background())
	if err != nil {
		t.Fatalf("storetest: %v", err)
	}
	s, err := store.Open(context.Background(), dbURL, store.Options{MaxConns: 8})
	if err != nil {
		drop()
		t.Fatalf("storetest: open: %v", err)
	}
	t.Cleanup(func() {
		s.Close()
		drop()
	})
	return s, dbURL
}

// NewDatabase creates a fresh, migrated database and returns its URL and a
// function that drops it. It is for callers without a testing.TB, such as
// TestMain; close every connection before calling drop.
func NewDatabase(ctx context.Context) (string, func(), error) {
	setupOnce.Do(func() { baseURL, template, setupErr = setup() })
	if setupErr != nil {
		return "", nil, setupErr
	}

	name := "test_" + strings.ToLower(ulid.Make().String())
	if err := execAdmin(ctx, baseURL, fmt.Sprintf(`CREATE DATABASE %q TEMPLATE %q`, name, template)); err != nil {
		return "", nil, fmt.Errorf("create database: %w", err)
	}
	drop := func() {
		_ = execAdmin(context.Background(), baseURL, fmt.Sprintf(`DROP DATABASE %q WITH (FORCE)`, name))
	}
	return withDatabase(baseURL, name), drop, nil
}

func setup() (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	base := os.Getenv(EnvDatabaseURL)
	if base == "" {
		ctr, err := postgres.Run(ctx, "postgres:17-alpine",
			postgres.WithDatabase("hookyard"),
			postgres.WithUsername("hookyard"),
			postgres.WithPassword("hookyard"),
			postgres.BasicWaitStrategies(),
		)
		if err != nil {
			return "", "", fmt.Errorf("start postgres container (is Docker running? or set %s): %w", EnvDatabaseURL, err)
		}
		// The testcontainers reaper removes the container when the test binary exits.
		base, err = ctr.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			return "", "", err
		}
	}

	tpl, err := ensureTemplate(ctx, base)
	return base, tpl, err
}

// ensureTemplate creates a migrated template database named after the embedded
// migrations. Test binaries for different packages run concurrently and may
// share a server, so creation is guarded by an advisory lock.
func ensureTemplate(ctx context.Context, base string) (string, error) {
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		return "", fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)

	name := templateName()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey(name)); err != nil {
		return "", err
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, lockKey(name)) }()

	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return name, nil
	}

	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		return "", fmt.Errorf("create template: %w", err)
	}
	s, err := store.Open(ctx, withDatabase(base, name), store.Options{})
	if err != nil {
		return "", err
	}
	_, err = s.Migrate(ctx)
	s.Close()
	if err != nil {
		_, _ = conn.Exec(ctx, fmt.Sprintf(`DROP DATABASE %q`, name))
		return "", err
	}
	return name, nil
}

// templateName is derived from the embedded migrations, so a new or changed
// migration gets a new template instead of reusing a stale one.
func templateName() string {
	return "hookyard_template_" + store.MigrationsFingerprint()
}

func lockKey(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64() >> 1)
}

func execAdmin(ctx context.Context, base, sql string) error {
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, sql)
	return err
}

func withDatabase(base, name string) string {
	u, err := url.Parse(base)
	if err != nil {
		panic(fmt.Sprintf("storetest: invalid database url: %v", err))
	}
	u.Path = "/" + name
	return u.String()
}
