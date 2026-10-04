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

func TestPauseAndResume(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	until := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	p, err := s.PauseUpstream(ctx, store.Pause{Upstream: "courier-x", Reason: "vendor maintenance", Until: &until, Actor: "ops"})
	if err != nil || p.Actor != "ops" || !p.Until.Equal(until) {
		t.Fatalf("pause: %+v %v", p, err)
	}
	// Pausing again updates the reason but keeps when it started.
	again, err := s.PauseUpstream(ctx, store.Pause{Upstream: "courier-x", Reason: "still down", Actor: "ops2"})
	if err != nil || !again.PausedAt.Equal(p.PausedAt) || again.Reason != "still down" || again.Until != nil {
		t.Fatalf("re-pause: %+v %v", again, err)
	}

	pauses, err := s.ListPauses(ctx)
	if err != nil || len(pauses) != 1 || pauses["courier-x"].Reason != "still down" {
		t.Fatalf("pauses = %+v %v", pauses, err)
	}

	if ok, err := s.ResumeUpstream(ctx, "courier-x", "ops", "back up"); err != nil || !ok {
		t.Fatalf("resume: %v %v", ok, err)
	}
	if ok, _ := s.ResumeUpstream(ctx, "courier-x", "ops", ""); ok {
		t.Error("resuming an upstream that isn't paused reports false")
	}

	events, _ := s.ListUpstreamEvents(ctx, "courier-x", 10)
	if len(events) != 3 || events[0].Kind != "resumed" || events[0].Actor != "ops" || events[2].Kind != "paused" {
		t.Errorf("events = %+v", events)
	}
	audit, _ := s.ListAudit(ctx, "upstream", "courier-x", 10)
	if len(audit) != 3 {
		t.Errorf("audit entries = %d, want 3 (pause, pause, resume)", len(audit))
	}
}

func TestResumeExpiredPauses(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	past := time.Now().Add(-time.Second)
	future := time.Now().Add(time.Hour)
	for name, until := range map[string]*time.Time{"ended": &past, "later": &future, "forever": nil} {
		if _, err := s.PauseUpstream(ctx, store.Pause{Upstream: name, Until: until, Actor: "ops"}); err != nil {
			t.Fatal(err)
		}
	}
	resumed, err := s.ResumeExpiredPauses(ctx)
	if err != nil || len(resumed) != 1 || resumed[0] != "ended" {
		t.Fatalf("resumed = %v, err %v", resumed, err)
	}
	if again, _ := s.ResumeExpiredPauses(ctx); len(again) != 0 {
		t.Errorf("an expired pause must be resumed only once: %v", again)
	}
	pauses, _ := s.ListPauses(ctx)
	if len(pauses) != 2 {
		t.Errorf("remaining pauses = %v", pauses)
	}
}
