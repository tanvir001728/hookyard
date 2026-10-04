package store_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tanvir001728/hookyard/internal/store"
	"github.com/tanvir001728/hookyard/internal/store/storetest"
)

func TestUpstreamEvents(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	for _, kind := range []string{"breaker_opened", "breaker_half_open", "breaker_closed"} {
		if err := s.AddUpstreamEvent(ctx, store.UpstreamEvent{Upstream: "a", Kind: kind, Reason: "test", Details: map[string]any{"n": 1}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddUpstreamEvent(ctx, store.UpstreamEvent{Upstream: "b", Kind: "breaker_opened"}); err != nil {
		t.Fatal(err)
	}

	events, err := s.ListUpstreamEvents(ctx, "a", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Kind != "breaker_closed" || events[1].Kind != "breaker_half_open" ||
		events[0].Actor != "hookyard" || events[0].Details["n"] != float64(1) {
		t.Errorf("events = %+v", events)
	}
}

func TestExtendRetryWindows(t *testing.T) {
	t.Parallel()
	s, dbURL := storetest.NewWithURL(t)
	ctx := t.Context()

	// Set exact windows: one request started before the pause, one during it.
	before, _, _ := s.CreateRequest(ctx, newRequest())
	during, _, _ := s.CreateRequest(ctx, newRequest())
	other, _, _ := s.CreateRequest(ctx, newRequest(func(in *store.NewRequest) { in.Upstream = "payments-y" }))

	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	set := func(id string, ago time.Duration) {
		if _, err := conn.Exec(ctx, `UPDATE requests SET retry_window_start = now() - $2 * interval '1 millisecond' WHERE id = $1`, id, ago.Milliseconds()); err != nil {
			t.Fatal(err)
		}
	}
	set(before.ID, 10*time.Minute)
	set(during.ID, 2*time.Minute)
	set(other.ID, 10*time.Minute)

	// The upstream was paused 5 minutes ago.
	var pausedSince time.Time
	if err := conn.QueryRow(ctx, `SELECT now() - interval '5 minutes'`).Scan(&pausedSince); err != nil {
		t.Fatal(err)
	}
	n, err := s.ExtendRetryWindows(ctx, "courier-x", pausedSince)
	if err != nil || n != 2 {
		t.Fatalf("extended %d, err %v", n, err)
	}

	age := func(id string) time.Duration {
		var d float64
		if err := conn.QueryRow(ctx, `SELECT extract(epoch FROM now() - retry_window_start) FROM requests WHERE id = $1`, id).Scan(&d); err != nil {
			t.Fatal(err)
		}
		return time.Duration(d * float64(time.Second))
	}
	// before: 10 minutes old, paused for the last 5 → its window counts 5 minutes.
	if a := age(before.ID); a < 4*time.Minute+50*time.Second || a > 5*time.Minute+10*time.Second {
		t.Errorf("request from before the pause: window age %s, want 5m", a)
	}
	// during: started 2 minutes ago, all of it paused → its window restarts now.
	if a := age(during.ID); a > 10*time.Second {
		t.Errorf("request from during the pause: window age %s, want ~0", a)
	}
	// Other upstreams are untouched.
	if a := age(other.ID); a < 9*time.Minute {
		t.Errorf("other upstream changed: %s", a)
	}
}
