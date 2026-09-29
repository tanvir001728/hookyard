package store_test

import (
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
	"github.com/tanvir001728/hookyard/internal/store/storetest"
)

func TestAddStatsIsAdditive(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()
	bucket := time.Now().UTC().Truncate(time.Minute)

	row := store.StatsRow{Bucket: bucket, Upstream: "courier-x", Attempts: 2, FailedAttempts: 1, Succeeded: 1, LatencySumMS: 30, Latency: map[int]int64{10: 1, 25: 1}}
	for range 2 { // two instances writing the same bucket
		if err := s.AddStats(ctx, []store.StatsRow{row}); err != nil {
			t.Fatal(err)
		}
	}

	totals, err := s.StatsTotals(ctx, bucket.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	got := totals["courier-x"]
	if got.Attempts != 4 || got.FailedAttempts != 2 || got.Succeeded != 2 || got.LatencySumMS != 60 || got.Latency[10] != 2 || got.Latency[25] != 2 {
		t.Errorf("totals = %+v", got)
	}

	// Rows before the window are excluded.
	totals, _ = s.StatsTotals(ctx, bucket.Add(time.Minute))
	if len(totals) != 0 {
		t.Errorf("window should exclude older buckets: %+v", totals)
	}
}

func TestStatsSeries(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()
	base := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

	rows := []store.StatsRow{
		{Bucket: base, Upstream: "a", Attempts: 1, Succeeded: 1, Latency: map[int]int64{10: 1}},
		{Bucket: base.Add(time.Minute), Upstream: "a", Attempts: 2, Succeeded: 2, Latency: map[int]int64{10: 2}},
		{Bucket: base.Add(time.Minute), Upstream: "b", Attempts: 5, Dead: 5},
		{Bucket: base.Add(7 * time.Minute), Upstream: "a", Attempts: 1, FailedAttempts: 1},
	}
	if err := s.AddStats(ctx, rows); err != nil {
		t.Fatal(err)
	}

	series, err := s.StatsSeries(ctx, "a", base, base.Add(10*time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 {
		t.Fatalf("got %d buckets, want 2", len(series))
	}
	if series[0].Start != base || series[0].Attempts != 3 || series[0].Succeeded != 3 || series[0].Latency[10] != 3 {
		t.Errorf("bucket 0 = %+v", series[0])
	}
	if series[1].Start != base.Add(5*time.Minute) || series[1].FailedAttempts != 1 {
		t.Errorf("bucket 1 = %+v", series[1])
	}

	// An uneven step (7m) starting off the hour still bins correctly.
	series, err = s.StatsSeries(ctx, "a", base.Add(time.Minute), base.Add(15*time.Minute), 7*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 || series[0].Attempts != 3 || series[1].Attempts != 0 || series[0].Start != base.Add(time.Minute) {
		t.Errorf("7m series = %+v", series)
	}

	// All upstreams combined, minute resolution, empty buckets included.
	series, err = s.StatsSeries(ctx, "", base, base.Add(3*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 3 || series[1].Attempts != 7 || series[1].Dead != 5 || series[2].Attempts != 0 {
		t.Errorf("combined series = %+v", series)
	}
}

func TestQueueStats(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	// finish claims every due request, so it runs before the pending ones exist.
	finish(t, s, newRequest(func(in *store.NewRequest) { in.Upstream = "payments-y" }), model.StatusDead, 400, "http_status")
	for range 2 {
		if _, _, err := s.CreateRequest(ctx, newRequest()); err != nil {
			t.Fatal(err)
		}
	}

	q, err := s.QueueStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cx := q["courier-x"]; cx.Waiting != 2 || cx.Dead != 0 || cx.OldestWaitingSince == nil {
		t.Errorf("courier-x = %+v", cx)
	}
	if py := q["payments-y"]; py.Waiting != 0 || py.Dead != 1 || py.OldestWaitingSince != nil {
		t.Errorf("payments-y = %+v", py)
	}
}

func TestRetentionDeletes(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := t.Context()

	done := finish(t, s, newRequest(), model.StatusSucceeded, 200, "")
	pending, _, _ := s.CreateRequest(ctx, newRequest())

	// Nothing is old enough yet.
	if n, err := s.DeleteFinishedRequestsBefore(ctx, time.Now().Add(-time.Hour), 10); err != nil || n != 0 {
		t.Fatalf("deleted %d, err %v", n, err)
	}
	// Only finished requests are deleted; pending ones are never touched.
	if n, err := s.DeleteFinishedRequestsBefore(ctx, time.Now().Add(time.Hour), 1); err != nil || n != 1 {
		t.Fatalf("deleted %d, err %v", n, err)
	}
	if _, err := s.GetRequest(ctx, done.ID); err == nil {
		t.Error("finished request should be deleted")
	}
	if _, err := s.GetRequest(ctx, pending.ID); err != nil {
		t.Error("pending request must be kept")
	}

	old := time.Now().Add(-40 * 24 * time.Hour).UTC().Truncate(time.Minute)
	if err := s.AddStats(ctx, []store.StatsRow{{Bucket: old, Upstream: "a", Attempts: 1, Latency: map[int]int64{5: 1}}}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.DeleteStatsBefore(ctx, time.Now().Add(-30*24*time.Hour)); err != nil || n != 2 {
		t.Errorf("deleted %d stats rows (want the row and its latency bucket), err %v", n, err)
	}

	withKey := newRequest(func(in *store.NewRequest) { in.DedupeKey, in.DedupeWindow = "k", time.Millisecond })
	if _, _, err := s.CreateRequest(ctx, withKey); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if n, err := s.DeleteExpiredDedupeKeys(ctx); err != nil || n != 1 {
		t.Errorf("deleted %d dedupe keys, err %v", n, err)
	}
}
