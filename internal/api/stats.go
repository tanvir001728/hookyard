package api

import (
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/stats"
	"github.com/tanvir001728/hookyard/internal/store"
)

// Limits for stats queries.
const (
	maxStatsWindow  = 30 * 24 * time.Hour
	maxSeriesPoints = 1500
)

type latencyJSON struct {
	P50 *float64 `json:"p50"`
	P95 *float64 `json:"p95"`
	P99 *float64 `json:"p99"`
}

func toLatencyJSON(h map[int]int64) latencyJSON {
	round := func(v *float64) *float64 {
		if v == nil {
			return nil
		}
		r := math.Round(*v*10) / 10
		return &r
	}
	return latencyJSON{
		P50: round(stats.Percentile(h, 0.50)),
		P95: round(stats.Percentile(h, 0.95)),
		P99: round(stats.Percentile(h, 0.99)),
	}
}

type upstreamStatsJSON struct {
	Upstream                string      `json:"upstream"`
	Succeeded               int64       `json:"succeeded"`
	FailedAttempts          int64       `json:"failed_attempts"`
	Dead                    int64       `json:"dead"`
	SuccessRate             *float64    `json:"success_rate"`
	ThroughputPerMin        float64     `json:"throughput_per_min"`
	LatencyMS               latencyJSON `json:"latency_ms"`
	QueueDepth              int64       `json:"queue_depth"`
	OldestPendingAgeSeconds *int64      `json:"oldest_pending_age_seconds"`
	DLQSize                 int64       `json:"dlq_size"`
}

func (s *Server) handleStatsOverview(w http.ResponseWriter, r *http.Request) {
	window := time.Hour
	if v := r.URL.Query().Get("window"); v != "" {
		d, err := model.ParseDuration(v)
		if err != nil || d < time.Minute || d > maxStatsWindow {
			writeError(w, http.StatusBadRequest, codeBadRequest,
				fmt.Sprintf("invalid query parameters: window: must be a duration between 1m and %s", model.FormatDuration(maxStatsWindow)))
			return
		}
		window = d
	}

	now := time.Now()
	totals, err := s.v1.Store.StatsTotals(r.Context(), now.Add(-window))
	if err != nil {
		s.internalError(w, r, "stats totals", err)
		return
	}
	queues, err := s.v1.Store.QueueStats(r.Context())
	if err != nil {
		s.internalError(w, r, "queue stats", err)
		return
	}

	// Every configured upstream appears, even without traffic, plus any
	// upstream that still has data after being removed from the config.
	names := s.v1.Config.Upstreams.Names()
	for u := range totals {
		names = append(names, u)
	}
	for u := range queues {
		names = append(names, u)
	}
	slices.Sort(names)
	names = slices.Compact(names)

	data := make([]upstreamStatsJSON, 0, len(names))
	for _, u := range names {
		t := totals[u]
		if t == nil {
			t = &store.StatsTotal{}
		}
		q := queues[u]
		row := upstreamStatsJSON{
			Upstream:         u,
			Succeeded:        t.Succeeded,
			FailedAttempts:   t.FailedAttempts,
			Dead:             t.Dead,
			ThroughputPerMin: math.Round(float64(t.Attempts)/window.Minutes()*100) / 100,
			LatencyMS:        toLatencyJSON(t.Latency),
			QueueDepth:       q.Waiting,
			DLQSize:          q.Dead,
		}
		if finished := t.Succeeded + t.Dead; finished > 0 {
			rate := math.Round(float64(t.Succeeded)/float64(finished)*10000) / 10000
			row.SuccessRate = &rate
		}
		if q.OldestWaitingSince != nil {
			age := int64(max(now.Sub(*q.OldestWaitingSince), 0).Seconds())
			row.OldestPendingAgeSeconds = &age
		}
		data = append(data, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"window": model.Duration(window), "data": data})
}

func (s *Server) handleStatsTimeseries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var problems []string
	now := time.Now()

	to := now
	if t := parseTimeParam(q, "to", &problems); t != nil {
		to = *t
	}
	from := to.Add(-time.Hour)
	if t := parseTimeParam(q, "from", &problems); t != nil {
		from = *t
	}
	step := time.Minute
	if v := q.Get("step"); v != "" {
		d, err := model.ParseDuration(v)
		if err != nil || d <= 0 {
			problems = append(problems, "step: must be a positive duration such as 1m or 1h")
		} else {
			// Rollups have one-minute resolution: round up to whole minutes.
			step = time.Duration(math.Ceil(d.Minutes())) * time.Minute
		}
	}
	if len(problems) == 0 {
		switch {
		case !from.Before(to):
			problems = append(problems, "from: must be before to")
		case to.Sub(from)/step > maxSeriesPoints:
			problems = append(problems, fmt.Sprintf("step: the range would have more than %d points; use a larger step", maxSeriesPoints))
		}
	}
	if len(problems) > 0 {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid query parameters: "+strings.Join(problems, "; "))
		return
	}

	upstream := q.Get("upstream")
	series, err := s.v1.Store.StatsSeries(r.Context(), upstream, from, to, step)
	if err != nil {
		s.internalError(w, r, "stats series", err)
		return
	}

	type bucketJSON struct {
		Start          time.Time   `json:"start"`
		Succeeded      int64       `json:"succeeded"`
		FailedAttempts int64       `json:"failed_attempts"`
		Dead           int64       `json:"dead"`
		LatencyMS      latencyJSON `json:"latency_ms"`
	}
	data := make([]bucketJSON, len(series))
	for i, b := range series {
		data[i] = bucketJSON{b.Start.UTC(), b.Succeeded, b.FailedAttempts, b.Dead, toLatencyJSON(b.Latency)}
	}
	var up *string
	if upstream != "" {
		up = &upstream
	}
	writeJSON(w, http.StatusOK, map[string]any{"upstream": up, "step": model.Duration(step), "data": data})
}
