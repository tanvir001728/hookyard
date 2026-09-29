package store_test

import (
	"testing"

	"github.com/tanvir001728/hookyard/internal/store"
	"github.com/tanvir001728/hookyard/internal/store/storetest"
)

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	s := storetest.New(t) // already migrated from the template

	applied, err := s.Migrate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Errorf("re-running migrations applied %v, want none", applied)
	}

	statuses, err := s.MigrationStatuses(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) == 0 {
		t.Fatal("no migrations found")
	}
	for _, st := range statuses {
		if !st.Applied {
			t.Errorf("migration %d (%s) not applied", st.Version, st.Name)
		}
	}

	// The pool must still work after the migration's sql.DB wrapper is closed.
	if err := s.Ping(t.Context()); err != nil {
		t.Fatalf("store unusable after Migrate: %v", err)
	}
}

func TestMigrationsFingerprintIsStable(t *testing.T) {
	a, b := store.MigrationsFingerprint(), store.MigrationsFingerprint()
	if a != b || len(a) != 12 {
		t.Fatalf("fingerprint unstable or wrong length: %q %q", a, b)
	}
}
