package api

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
)

type fakeMonitor struct {
	live    map[string]model.UpstreamLive
	changed atomic.Int32
}

func (f *fakeMonitor) UpstreamLive(name string) (model.UpstreamLive, bool) {
	l, ok := f.live[name]
	return l, ok
}

func (f *fakeMonitor) UpstreamsChanged() { f.changed.Add(1) }

func TestUpstreamLiveState(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	since := time.Now().Add(-time.Minute)
	mon := &fakeMonitor{live: map[string]model.UpstreamLive{
		"courier-x":  {Breaker: "open", BreakerSince: since, InFlight: 0, Tokens: 4},
		"payments-y": {Breaker: "closed", InFlight: 2, Tokens: -1},
	}}
	a.srv.v1.Monitor = mon

	var list struct {
		Data []upstreamJSON `json:"data"`
	}
	a.do(http.MethodGet, "/v1/upstreams", "", &list)
	cx, py := list.Data[0].State, list.Data[1].State
	if cx.Status != "breaker_open" || cx.Breaker != "open" || cx.BreakerSince == nil || *cx.AvailableTokens != 4 {
		t.Errorf("courier-x state = %+v", cx)
	}
	if py.Status != "active" || py.InFlight != 2 || py.AvailableTokens != nil {
		t.Errorf("payments-y state = %+v", py)
	}
}

func TestPauseResume(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	mon := &fakeMonitor{}
	a.srv.v1.Monitor = mon

	var up upstreamJSON
	rec := a.do(http.MethodPost, "/v1/upstreams/courier-x/pause", `{"reason":"vendor maintenance","duration":"2h"}`, &up)
	if rec.Code != http.StatusOK || up.State.Status != "paused" || up.State.Pause == nil ||
		up.State.Pause.Reason != "vendor maintenance" || up.State.Pause.By != "orders" || up.State.Pause.Until == nil {
		t.Fatalf("pause: %d %+v", rec.Code, up.State)
	}
	if d := time.Until(*up.State.Pause.Until); d < 119*time.Minute || d > 121*time.Minute {
		t.Errorf("pause ends in %s, want 2h", d)
	}
	if mon.changed.Load() != 1 {
		t.Error("the engine must be told about the pause")
	}

	var got upstreamJSON
	a.do(http.MethodGet, "/v1/upstreams/courier-x", "", &got)
	if got.State.Status != "paused" {
		t.Errorf("get after pause: %+v", got.State)
	}

	rec = a.do(http.MethodPost, "/v1/upstreams/courier-x/resume", "", &up)
	if rec.Code != http.StatusOK || up.State.Status != "active" || up.State.Pause != nil {
		t.Fatalf("resume: %d %+v", rec.Code, up.State)
	}
	var e apiError
	if rec := a.do(http.MethodPost, "/v1/upstreams/courier-x/resume", `{"reason":"again"}`, &e); rec.Code != http.StatusConflict || e.Error.Code != codeInvalidState {
		t.Errorf("resume twice: %d %+v", rec.Code, e)
	}

	var events struct {
		Data []struct {
			Kind, Actor, Reason string
		} `json:"data"`
	}
	a.do(http.MethodGet, "/v1/upstreams/courier-x/events?limit=10", "", &events)
	if len(events.Data) != 2 || events.Data[0].Kind != "resumed" || events.Data[1].Kind != "paused" || events.Data[1].Reason != "vendor maintenance" {
		t.Errorf("events = %+v", events.Data)
	}
}

func TestPauseValidation(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	tests := map[string]string{
		"both":         `{"until":"2099-01-01T00:00:00Z","duration":"1h"}`,
		"past":         `{"until":"` + past + `"}`,
		"too long":     `{"duration":"1000h"}`,
		"bad duration": `{"duration":"soon"}`,
		"long reason":  `{"reason":"` + strings.Repeat("x", 501) + `"}`,
	}
	for name, body := range tests {
		var e apiError
		if rec := a.do(http.MethodPost, "/v1/upstreams/courier-x/pause", body, &e); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %+v", name, rec.Code, e)
		}
	}
	var e apiError
	if rec := a.do(http.MethodPost, "/v1/upstreams/courier-y/pause", `{}`, &e); rec.Code != http.StatusNotFound || !strings.Contains(e.Error.Message, "did you mean") {
		t.Errorf("unknown upstream: %d %+v", rec.Code, e)
	}
	if rec := a.do(http.MethodGet, "/v1/upstreams/courier-x/events?limit=0", "", &e); rec.Code != http.StatusBadRequest {
		t.Errorf("bad limit: %d", rec.Code)
	}
}
