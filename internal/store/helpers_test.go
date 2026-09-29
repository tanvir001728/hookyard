package store_test

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

// expireLeases makes every current lease look expired, simulating a worker
// that stalled or crashed.
func expireLeases(t *testing.T, dbURL string) {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	if _, err := conn.Exec(t.Context(), `UPDATE requests SET lease_expires_at = now() - interval '1 second' WHERE status = 'in_flight'`); err != nil {
		t.Fatal(err)
	}
}
