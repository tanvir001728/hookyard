package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// MigrationStatus describes one migration and whether it has been applied.
type MigrationStatus struct {
	Version int64
	Name    string
	Applied bool
}

// Migrate applies all pending migrations and returns the versions it applied.
// It holds a Postgres advisory lock, so concurrent Hookyard instances starting
// at the same time migrate safely.
func (s *Store) Migrate(ctx context.Context) ([]int64, error) {
	p, closeDB, err := s.migrationProvider()
	if err != nil {
		return nil, err
	}
	defer closeDB()

	results, err := p.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	applied := make([]int64, 0, len(results))
	for _, r := range results {
		applied = append(applied, r.Source.Version)
	}
	return applied, nil
}

// MigrationStatuses reports every known migration and whether it is applied.
func (s *Store) MigrationStatuses(ctx context.Context) ([]MigrationStatus, error) {
	p, closeDB, err := s.migrationProvider()
	if err != nil {
		return nil, err
	}
	defer closeDB()

	statuses, err := p.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("read migration status: %w", err)
	}
	out := make([]MigrationStatus, 0, len(statuses))
	for _, st := range statuses {
		out = append(out, MigrationStatus{
			Version: st.Source.Version,
			Name:    st.Source.Path,
			Applied: st.State == goose.StateApplied,
		})
	}
	return out, nil
}

func (s *Store) migrationProvider() (*goose.Provider, func(), error) {
	fsys, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, nil, err
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, nil, err
	}
	db := stdlib.OpenDBFromPool(s.pool)
	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("load migrations: %w", err)
	}
	return p, func() { _ = db.Close() }, nil
}

// MigrationsFingerprint returns a short hash of the embedded migration files.
// It changes whenever a migration is added or modified.
func MigrationsFingerprint() string {
	h := sha256.New()
	err := fs.WalkDir(migrationFiles, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := migrationFiles.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", path, len(b))
		h.Write(b)
		return nil
	})
	if err != nil {
		// The files are embedded at build time, so this cannot fail at runtime.
		panic(fmt.Sprintf("store: read embedded migrations: %v", err))
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
