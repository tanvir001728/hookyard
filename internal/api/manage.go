package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tanvir001728/hookyard/internal/config"
	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
)

// maxReplayIDs bounds the ids list of a bulk replay.
const maxReplayIDs = 1000

// deliveryErrorCodes are the documented values of DeliveryErrorCode.
var deliveryErrorCodes = []string{"timeout", "connection", "http_status", "max_age_exceeded", "canceled", "internal"}

func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f store.RequestFilter
	var problems []string

	f.Upstream = q.Get("upstream")
	if v := q.Get("status"); v != "" {
		for _, st := range strings.Split(v, ",") {
			status := model.Status(strings.TrimSpace(st))
			if !status.Valid() {
				problems = append(problems, fmt.Sprintf("status: unknown status %q", st))
				continue
			}
			f.Statuses = append(f.Statuses, status)
		}
	}
	for _, tag := range q["tag"] {
		k, v, ok := strings.Cut(tag, ":")
		if !ok || k == "" {
			problems = append(problems, fmt.Sprintf("tag: %q must be key:value", tag))
			continue
		}
		if f.Tags == nil {
			f.Tags = map[string]string{}
		}
		f.Tags[k] = v
	}
	f.DedupeKey = q.Get("dedupe_key")
	f.CreatedAfter = parseTimeParam(q, "created_after", &problems)
	f.CreatedBefore = parseTimeParam(q, "created_before", &problems)
	f.Limit = parseLimit(q, &problems)
	f.Cursor = q.Get("cursor")

	if len(problems) > 0 {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid query parameters: "+strings.Join(problems, "; "))
		return
	}

	page, err := s.v1.Store.ListRequests(r.Context(), f)
	if errors.Is(err, store.ErrInvalidCursor) {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid cursor: pass next_cursor from a previous response unchanged")
		return
	}
	if err != nil {
		s.internalError(w, r, "list requests", err)
		return
	}

	resp := struct {
		Data       []requestJSON `json:"data"`
		NextCursor *string       `json:"next_cursor"`
	}{Data: make([]requestJSON, len(page.Requests))}
	for i, req := range page.Requests {
		resp.Data[i] = toRequestJSON(req)
	}
	if page.NextCursor != "" {
		resp.NextCursor = &page.NextCursor
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	req, err := s.v1.Store.GetRequest(r.Context(), id)
	if s.storeError(w, r, "get request", id, err) {
		return
	}
	writeJSON(w, http.StatusOK, toRequestJSON(req))
}

func (s *Server) handleListAttempts(w http.ResponseWriter, r *http.Request) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	attempts, err := s.v1.Store.ListAttempts(r.Context(), id)
	if s.storeError(w, r, "list attempts", id, err) {
		return
	}
	resp := struct {
		Data []attemptJSON `json:"data"`
	}{Data: make([]attemptJSON, len(attempts))}
	for i, a := range attempts {
		resp.Data[i] = toAttemptJSON(a)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleReplayRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	req, err := s.v1.Store.ReplayRequest(r.Context(), id, actorFrom(r.Context()))
	if s.storeError(w, r, "replay request", id, err) {
		return
	}
	s.notify()
	s.log.Info("request replayed", "id", id, "actor", actorFrom(r.Context()))
	writeJSON(w, http.StatusAccepted, toRequestJSON(req))
}

func (s *Server) handleCancelRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	req, err := s.v1.Store.CancelRequest(r.Context(), id, actorFrom(r.Context()))
	if s.storeError(w, r, "cancel request", id, err) {
		return
	}
	s.log.Info("request canceled", "id", id, "actor", actorFrom(r.Context()))
	writeJSON(w, http.StatusOK, toRequestJSON(req))
}

func (s *Server) handleDLQSummary(w http.ResponseWriter, r *http.Request) {
	groups, total, err := s.v1.Store.DLQSummary(r.Context(), r.URL.Query().Get("upstream"))
	if err != nil {
		s.internalError(w, r, "dlq summary", err)
		return
	}
	type groupJSON struct {
		Upstream     string    `json:"upstream"`
		ErrorCode    string    `json:"error_code"`
		StatusCode   *int      `json:"status_code"`
		Count        int       `json:"count"`
		OldestDeadAt time.Time `json:"oldest_dead_at"`
		NewestDeadAt time.Time `json:"newest_dead_at"`
	}
	resp := struct {
		Total  int         `json:"total"`
		Groups []groupJSON `json:"groups"`
	}{Total: total, Groups: make([]groupJSON, len(groups))}
	for i, g := range groups {
		resp.Groups[i] = groupJSON{g.Upstream, g.ErrorCode, g.StatusCode, g.Count, g.OldestDeadAt.UTC(), g.NewestDeadAt.UTC()}
	}
	writeJSON(w, http.StatusOK, resp)
}

type dlqReplayBody struct {
	Upstream   string     `json:"upstream"`
	ErrorCode  string     `json:"error_code"`
	StatusCode *int       `json:"status_code"`
	DeadAfter  *time.Time `json:"dead_after"`
	DeadBefore *time.Time `json:"dead_before"`
	IDs        []string   `json:"ids"`
	DryRun     bool       `json:"dry_run"`
}

func (s *Server) handleDLQReplay(w http.ResponseWriter, r *http.Request) {
	var in dlqReplayBody
	if !s.decodeJSON(w, r, &in) {
		return
	}

	var problems []fieldError
	if in.ErrorCode != "" && !slices.Contains(deliveryErrorCodes, in.ErrorCode) {
		problems = append(problems, fieldError{"error_code", "must be one of " + strings.Join(deliveryErrorCodes, ", ")})
	}
	if in.StatusCode != nil && (*in.StatusCode < 100 || *in.StatusCode > 599) {
		problems = append(problems, fieldError{"status_code", "must be an HTTP status code"})
	}
	if len(in.IDs) > maxReplayIDs {
		problems = append(problems, fieldError{"ids", fmt.Sprintf("at most %d ids are allowed", maxReplayIDs)})
	}
	for i, id := range in.IDs {
		if !model.IsRequestID(id) {
			problems = append(problems, fieldError{fmt.Sprintf("ids[%d]", i), fmt.Sprintf("%q is not a request id", id)})
		}
	}
	if len(problems) > 0 {
		writeValidationError(w, problems)
		return
	}

	f := store.DLQFilter{
		Upstream: in.Upstream, ErrorCode: in.ErrorCode, StatusCode: in.StatusCode,
		DeadAfter: in.DeadAfter, DeadBefore: in.DeadBefore, IDs: in.IDs,
	}
	matched, replayed, err := s.v1.Store.ReplayDead(r.Context(), f, in.DryRun, actorFrom(r.Context()))
	if err != nil {
		s.internalError(w, r, "replay dlq", err)
		return
	}
	if replayed > 0 {
		s.notify()
		s.log.Info("dead requests replayed", "count", replayed, "actor", actorFrom(r.Context()))
	}
	writeJSON(w, http.StatusOK, map[string]any{"matched": matched, "replayed": replayed, "dry_run": in.DryRun})
}

func (s *Server) handleListUpstreams(w http.ResponseWriter, _ *http.Request) {
	ups := s.v1.Config.Upstreams.All()
	resp := struct {
		Data []upstreamJSON `json:"data"`
	}{Data: make([]upstreamJSON, len(ups))}
	for i, u := range ups {
		resp.Data[i] = toUpstreamJSON(u)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetUpstream(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	u, ok := s.v1.Config.Upstreams.Get(name)
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, s.v1.Config.Upstreams.UnknownUpstreamMessage(name))
		return
	}
	writeJSON(w, http.StatusOK, toUpstreamJSON(u))
}

// upstreamJSON never includes header values: they usually hold credentials.
type upstreamJSON struct {
	Name        string          `json:"name"`
	BaseURL     string          `json:"base_url"`
	Timeout     model.Duration  `json:"timeout"`
	Retry       retryPolicyJSON `json:"retry"`
	HeaderNames []string        `json:"header_names"`
}

func toUpstreamJSON(u config.Upstream) upstreamJSON {
	names := make([]string, 0, len(u.Headers))
	for k := range u.Headers {
		names = append(names, k)
	}
	slices.Sort(names)
	return upstreamJSON{
		Name:        u.Name,
		BaseURL:     u.BaseURL.String(),
		Timeout:     model.Duration(u.Timeout),
		Retry:       toRetryJSON(u.Retry),
		HeaderNames: names,
	}
}

type attemptJSON struct {
	Number     int                  `json:"number"`
	StartedAt  time.Time            `json:"started_at"`
	DurationMS int64                `json:"duration_ms"`
	Outcome    model.AttemptOutcome `json:"outcome"`
	StatusCode *int                 `json:"status_code"`
	Error      *deliveryError       `json:"error"`
	Response   *attemptResponseJSON `json:"response"`
	RetryAt    *time.Time           `json:"retry_at"`
}

type attemptResponseJSON struct {
	Headers       map[string]string `json:"headers"`
	Body          string            `json:"body"`
	BodyTruncated bool              `json:"body_truncated"`
}

func toAttemptJSON(a model.Attempt) attemptJSON {
	out := attemptJSON{
		Number:     a.Number,
		StartedAt:  a.StartedAt.UTC(),
		DurationMS: a.Duration.Milliseconds(),
		Outcome:    a.Outcome,
		StatusCode: a.StatusCode,
		RetryAt:    utcPtr(a.RetryAt),
	}
	if a.Error != nil {
		out.Error = &deliveryError{Code: a.Error.Code, Message: a.Error.Message}
	}
	if a.Response != nil {
		out.Response = &attemptResponseJSON{Headers: nonNil(a.Response.Headers), Body: a.Response.Body, BodyTruncated: a.Response.BodyTruncated}
	}
	return out
}

// requestID reads and checks the {id} path value, writing a 404 if it isn't
// a well-formed request id.
func requestID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !model.IsRequestID(id) {
		writeError(w, http.StatusNotFound, codeNotFound, fmt.Sprintf("request %q not found", id))
		return "", false
	}
	return id, true
}

// storeError writes the response for a store error and reports whether there
// was one.
func (s *Server) storeError(w http.ResponseWriter, r *http.Request, op, id string, err error) bool {
	var stateErr *store.InvalidStateError
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, fmt.Sprintf("request %q not found", id))
	case errors.As(err, &stateErr):
		writeError(w, http.StatusConflict, codeInvalidState, stateErr.Error())
	default:
		s.internalError(w, r, op, err)
	}
	return true
}

func (s *Server) notify() {
	if s.v1.Notify != nil {
		s.v1.Notify()
	}
}

func parseTimeParam(q url.Values, name string, problems *[]string) *time.Time {
	v := q.Get(name)
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s: must be an RFC 3339 timestamp such as 2026-01-02T15:04:05Z", name))
		return nil
	}
	return &t
}

func parseLimit(q url.Values, problems *[]string) int {
	v := q.Get("limit")
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > store.MaxPageSize {
		*problems = append(*problems, fmt.Sprintf("limit: must be between 1 and %d", store.MaxPageSize))
		return 0
	}
	return n
}
