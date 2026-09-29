// Package store persists requests, attempts and audit records in Postgres.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// Store is a Postgres-backed data store. It is safe for concurrent use.
type Store struct {
	pool *pgxpool.Pool
}

// Options tunes the connection pool. Zero values use pgx defaults.
type Options struct {
	MaxConns        int32
	MaxConnLifetime time.Duration
}

// Open connects to the database at url and verifies the connection.
func Open(ctx context.Context, url string, opts Options) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	}
	if opts.MaxConnLifetime > 0 {
		cfg.MaxConnLifetime = opts.MaxConnLifetime
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases all connections.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping checks that the database is reachable. It is used as a readiness check.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}
