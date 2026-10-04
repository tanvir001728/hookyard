package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tanvir001728/hookyard/internal/config"
	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
)

// UpstreamMonitor exposes the delivery engine's live upstream state.
type UpstreamMonitor interface {
	UpstreamLive(name string) (model.UpstreamLive, bool)
	// UpstreamsChanged is called after an upstream is paused or resumed.
	UpstreamsChanged()
}

const (
	maxPauseReason = 500
	maxPauseFor    = 30 * 24 * time.Hour
)

type pauseJSON struct {
	Since  time.Time  `json:"since"`
	Until  *time.Time `json:"until"`
	Reason string     `json:"reason"`
	By     string     `json:"by"`
}

type upstreamStateJSON struct {
	Status          string     `json:"status"`
	Breaker         string     `json:"breaker"`
	BreakerSince    *time.Time `json:"breaker_since"`
	Pause           *pauseJSON `json:"pause"`
	InFlight        int        `json:"in_flight"`
	AvailableTokens *int       `json:"available_tokens"`
	ThrottledUntil  *time.Time `json:"throttled_until"`
}

// upstreamState combines the live engine state with the stored pause.
func (s *Server) upstreamState(u config.Upstream, pause *store.Pause) upstreamStateJSON {
	st := upstreamStateJSON{Status: "active", Breaker: "off"}
	if s.v1.Monitor != nil {
		if live, ok := s.v1.Monitor.UpstreamLive(u.Name); ok {
			st.Breaker = live.Breaker
			if live.Breaker != "off" && !live.BreakerSince.IsZero() {
				since := live.BreakerSince.UTC()
				st.BreakerSince = &since
			}
			st.InFlight = live.InFlight
			if live.Tokens >= 0 {
				st.AvailableTokens = &live.Tokens
			}
			if !live.ThrottledUntil.IsZero() {
				until := live.ThrottledUntil.UTC()
				st.ThrottledUntil = &until
			}
		}
	}
	switch {
	case pause != nil:
		st.Status = "paused"
		st.Pause = &pauseJSON{Since: pause.PausedAt.UTC(), Until: utcPtr(pause.Until), Reason: pause.Reason, By: pause.Actor}
	case st.Breaker == "open":
		st.Status = "breaker_open"
	case st.Breaker == "half_open":
		st.Status = "breaker_half_open"
	case st.ThrottledUntil != nil:
		st.Status = "throttled"
	}
	return st
}

func (s *Server) handleListUpstreams(w http.ResponseWriter, r *http.Request) {
	pauses, err := s.v1.Store.ListPauses(r.Context())
	if err != nil {
		s.internalError(w, r, "list pauses", err)
		return
	}
	ups := s.v1.Config.Upstreams.All()
	resp := struct {
		Data []upstreamJSON `json:"data"`
	}{Data: make([]upstreamJSON, len(ups))}
	for i, u := range ups {
		resp.Data[i] = s.toUpstreamJSON(u, pauseFor(pauses, u.Name))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetUpstream(w http.ResponseWriter, r *http.Request) {
	u, ok := s.upstream(w, r)
	if !ok {
		return
	}
	pauses, err := s.v1.Store.ListPauses(r.Context())
	if err != nil {
		s.internalError(w, r, "list pauses", err)
		return
	}
	writeJSON(w, http.StatusOK, s.toUpstreamJSON(u, pauseFor(pauses, u.Name)))
}

func (s *Server) handlePauseUpstream(w http.ResponseWriter, r *http.Request) {
	u, ok := s.upstream(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason   string     `json:"reason"`
		Until    *time.Time `json:"until"`
		Duration *string    `json:"duration"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}

	var problems []fieldError
	if utf8.RuneCountInString(in.Reason) > maxPauseReason {
		problems = append(problems, fieldError{"reason", fmt.Sprintf("must be at most %d characters", maxPauseReason)})
	}
	until := in.Until
	switch {
	case in.Until != nil && in.Duration != nil:
		problems = append(problems, fieldError{"until", "set either until or duration, not both"})
	case in.Duration != nil:
		d, err := model.ParseDuration(*in.Duration)
		if err != nil || d <= 0 || d > maxPauseFor {
			problems = append(problems, fieldError{"duration", "must be a positive duration of at most " + model.FormatDuration(maxPauseFor)})
		} else {
			t := time.Now().Add(d)
			until = &t
		}
	case in.Until != nil:
		if !in.Until.After(time.Now()) || time.Until(*in.Until) > maxPauseFor {
			problems = append(problems, fieldError{"until", "must be in the future and at most " + model.FormatDuration(maxPauseFor) + " away"})
		}
	}
	if len(problems) > 0 {
		writeValidationError(w, problems)
		return
	}

	pause, err := s.v1.Store.PauseUpstream(r.Context(), store.Pause{Upstream: u.Name, Until: until, Reason: strings.TrimSpace(in.Reason), Actor: actorFrom(r.Context())})
	if err != nil {
		s.internalError(w, r, "pause upstream", err)
		return
	}
	if s.v1.Monitor != nil {
		s.v1.Monitor.UpstreamsChanged()
	}
	s.log.Info("upstream paused", "upstream", u.Name, "until", until, "actor", actorFrom(r.Context()))
	writeJSON(w, http.StatusOK, s.toUpstreamJSON(u, &pause))
}

func (s *Server) handleResumeUpstream(w http.ResponseWriter, r *http.Request) {
	u, ok := s.upstream(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	// A body is optional.
	if r.ContentLength != 0 && !s.decodeJSON(w, r, &in) {
		return
	}
	resumed, err := s.v1.Store.ResumeUpstream(r.Context(), u.Name, actorFrom(r.Context()), strings.TrimSpace(in.Reason))
	if err != nil {
		s.internalError(w, r, "resume upstream", err)
		return
	}
	if !resumed {
		writeError(w, http.StatusConflict, codeInvalidState, fmt.Sprintf("upstream %q is not paused", u.Name))
		return
	}
	if s.v1.Monitor != nil {
		s.v1.Monitor.UpstreamsChanged()
	}
	s.log.Info("upstream resumed", "upstream", u.Name, "actor", actorFrom(r.Context()))
	writeJSON(w, http.StatusOK, s.toUpstreamJSON(u, nil))
}

func (s *Server) handleUpstreamEvents(w http.ResponseWriter, r *http.Request) {
	u, ok := s.upstream(w, r)
	if !ok {
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeError(w, http.StatusBadRequest, codeBadRequest, "invalid query parameters: limit: must be between 1 and 500")
			return
		}
		limit = n
	}
	events, err := s.v1.Store.ListUpstreamEvents(r.Context(), u.Name, limit)
	if err != nil {
		s.internalError(w, r, "list upstream events", err)
		return
	}
	type eventJSON struct {
		ID      int64          `json:"id"`
		At      time.Time      `json:"at"`
		Kind    string         `json:"kind"`
		Reason  string         `json:"reason"`
		Actor   string         `json:"actor"`
		Details map[string]any `json:"details"`
	}
	resp := struct {
		Data []eventJSON `json:"data"`
	}{Data: make([]eventJSON, len(events))}
	for i, e := range events {
		details := e.Details
		if details == nil {
			details = map[string]any{}
		}
		resp.Data[i] = eventJSON{e.ID, e.At.UTC(), e.Kind, e.Reason, e.Actor, details}
	}
	writeJSON(w, http.StatusOK, resp)
}

// upstream looks up the {name} path value, writing a 404 with a suggestion if
// it isn't configured.
func (s *Server) upstream(w http.ResponseWriter, r *http.Request) (config.Upstream, bool) {
	name := r.PathValue("name")
	u, ok := s.v1.Config.Upstreams.Get(name)
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, s.v1.Config.Upstreams.UnknownUpstreamMessage(name))
	}
	return u, ok
}

func pauseFor(pauses map[string]store.Pause, name string) *store.Pause {
	if p, ok := pauses[name]; ok {
		return &p
	}
	return nil
}
