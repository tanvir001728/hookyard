package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
)

func TestStatsOverview(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	now := time.Now().UTC().Truncate(time.Minute)
	err := a.srv.v1.Store.AddStats(t.Context(), []store.StatsRow{{
		Bucket: now, Upstream: "courier-x", Attempts: 60, FailedAttempts: 20, Succeeded: 45, Dead: 5,
		Latency: map[int]int64{50: 30, 100: 30},
	}})
	if err != nil {
		t.Fatal(err)
	}
	a.enqueue(`{"upstream":"courier-x","method":"GET","path":"/"}`)
	a.enqueue(`{"upstream":"payments-y","method":"GET","path":"/"}`)
	a.deliverAll(model.StatusDead, 400) // both go to the DLQ
	a.enqueue(`{"upstream":"courier-x","method":"GET","path":"/"}`)

	var resp struct {
		Window string              `json:"window"`
		Data   []upstreamStatsJSON `json:"data"`
	}
	if rec := a.do(http.MethodGet, "/v1/stats/overview?window=30m", "", &resp); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if resp.Window != "30m" || len(resp.Data) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
	cx := resp.Data[0]
	if cx.Upstream != "courier-x" || cx.Succeeded != 45 || cx.Dead != 5 || *cx.SuccessRate != 0.9 || cx.ThroughputPerMin != 2 {
		t.Errorf("courier-x = %+v", cx)
	}
	if cx.LatencyMS.P50 == nil || *cx.LatencyMS.P50 != 50 || *cx.LatencyMS.P95 != 95 {
		t.Errorf("latency = %+v", cx.LatencyMS)
	}
	if cx.QueueDepth != 1 || cx.DLQSize != 1 || cx.OldestPendingAgeSeconds == nil {
		t.Errorf("queue: depth=%d dlq=%d oldest=%v", cx.QueueDepth, cx.DLQSize, cx.OldestPendingAgeSeconds)
	}
	// An upstream with no traffic in the window still appears.
	py := resp.Data[1]
	if py.Upstream != "payments-y" || py.SuccessRate != nil || py.LatencyMS.P50 != nil || py.DLQSize != 1 {
		t.Errorf("payments-y = %+v", py)
	}

	var e apiError
	for _, q := range []string{"window=30s", "window=1y", "window=forever"} {
		if rec := a.do(http.MethodGet, "/v1/stats/overview?"+q, "", &e); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
}

func TestStatsTimeseries(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	base := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	err := a.srv.v1.Store.AddStats(t.Context(), []store.StatsRow{
		{Bucket: base, Upstream: "courier-x", Attempts: 1, Succeeded: 1, Latency: map[int]int64{10: 1}},
		{Bucket: base.Add(3 * time.Minute), Upstream: "courier-x", Attempts: 2, FailedAttempts: 2},
	})
	if err != nil {
		t.Fatal(err)
	}

	var resp struct {
		Upstream *string `json:"upstream"`
		Step     string  `json:"step"`
		Data     []struct {
			Start          time.Time   `json:"start"`
			Succeeded      int64       `json:"succeeded"`
			FailedAttempts int64       `json:"failed_attempts"`
			LatencyMS      latencyJSON `json:"latency_ms"`
		} `json:"data"`
	}
	// A 90s step rounds up to 2m.
	q := "/v1/stats/timeseries?upstream=courier-x&from=2026-09-29T10:00:00Z&to=2026-09-29T10:06:00Z&step=90s"
	if rec := a.do(http.MethodGet, q, "", &resp); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if resp.Step != "2m" || resp.Upstream == nil || *resp.Upstream != "courier-x" || len(resp.Data) != 3 {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Data[0].Succeeded != 1 || *resp.Data[0].LatencyMS.P50 != 7.5 || resp.Data[1].FailedAttempts != 2 || resp.Data[2].LatencyMS.P50 != nil {
		t.Errorf("buckets = %+v", resp.Data)
	}

	var e apiError
	for _, bad := range []string{
		"from=2026-09-29T11:00:00Z&to=2026-09-29T10:00:00Z",
		"from=2026-01-01T00:00:00Z&to=2026-09-29T00:00:00Z&step=1m",
		"step=soon",
	} {
		if rec := a.do(http.MethodGet, "/v1/stats/timeseries?"+bad, "", &e); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
}
