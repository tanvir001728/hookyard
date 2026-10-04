package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tanvir001728/hookyard/internal/config"
	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/retry"
	"github.com/tanvir001728/hookyard/internal/store"
)

// Request validation limits, mirrored in api/openapi.yaml.
const (
	maxPathLength      = 2048
	maxDedupeKeyLength = 256
	maxTags            = 20
	maxTagKeyLength    = 64
	maxTagValueLength  = 256
	maxScheduleAhead   = 30 * 24 * time.Hour
)

var allowedMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// createRequestBody is the JSON body of POST /v1/requests.
type createRequestBody struct {
	Upstream  string            `json:"upstream"`
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	Headers   map[string]string `json:"headers"`
	Body      json.RawMessage   `json:"body"`
	DedupeKey *string           `json:"dedupe_key"`
	DeliverAt *time.Time        `json:"deliver_at"`
	Timeout   *string           `json:"timeout"`
	Retry     *retry.Spec       `json:"retry"`
	Tags      map[string]string `json:"tags"`
	// CallbackURL overrides the upstream's default; "" turns callbacks off.
	CallbackURL *string `json:"callback_url"`
	OnResult    *string `json:"on_result"`
}

// maxOnResultLength limits on_result, mirrored in api/openapi.yaml.
const maxOnResultLength = 128

func (s *Server) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	var in createRequestBody
	if !s.decodeJSON(w, r, &in) {
		return
	}

	upstream, ok := s.v1.Config.Upstreams.Get(in.Upstream)
	if in.Upstream != "" && !ok {
		writeError(w, http.StatusUnprocessableEntity, codeUnknownUpstream, s.v1.Config.Upstreams.UnknownUpstreamMessage(in.Upstream))
		return
	}

	nr, problems := buildNewRequest(in, upstream, s.callbackOptions(), time.Now())
	if len(problems) > 0 {
		writeValidationError(w, problems)
		return
	}

	req, created, err := s.v1.Store.CreateRequest(r.Context(), nr)
	if err != nil {
		s.internalError(w, r, "create request", err)
		return
	}

	if !created {
		w.Header().Set("Hookyard-Deduplicated", "true")
		writeJSON(w, http.StatusOK, toRequestJSON(req))
		return
	}
	s.notify()
	s.log.Info("request enqueued", "id", req.ID, "upstream", req.Upstream, "status", req.Status, "actor", actorFrom(r.Context()))
	w.Header().Set("Location", "/v1/requests/"+req.ID)
	writeJSON(w, http.StatusAccepted, toRequestJSON(req))
}

// buildNewRequest validates the body and resolves it against the upstream's
// configuration. It reports every invalid field.
func buildNewRequest(in createRequestBody, up config.Upstream, cb callbackOptions, now time.Time) (store.NewRequest, []fieldError) {
	var problems []fieldError
	add := func(field, format string, args ...any) {
		problems = append(problems, fieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if in.Upstream == "" {
		add("upstream", "is required")
	}

	method := strings.ToUpper(in.Method)
	switch {
	case in.Method == "":
		add("method", "is required")
	case !slices.Contains(allowedMethods, method):
		add("method", "must be one of %s", strings.Join(allowedMethods, ", "))
	}

	if msg := validatePath(in.Path); msg != "" {
		add("path", "%s", msg)
	}

	headers := make(map[string]string, len(in.Headers))
	for k, v := range in.Headers {
		if err := config.ValidateHeader(k, v); err != nil {
			add("headers."+k, "%s", err)
			continue
		}
		headers[config.CanonicalHeaderKey(k)] = v
	}

	var dedupeKey string
	if in.DedupeKey != nil {
		dedupeKey = *in.DedupeKey
		if n := utf8.RuneCountInString(dedupeKey); n < 1 || n > maxDedupeKeyLength {
			add("dedupe_key", "must be between 1 and %d characters", maxDedupeKeyLength)
		}
	}

	if in.DeliverAt != nil && in.DeliverAt.After(now.Add(maxScheduleAhead)) {
		add("deliver_at", "must be at most %s in the future", model.FormatDuration(maxScheduleAhead))
	}

	timeout := up.Timeout
	if in.Timeout != nil {
		d, err := model.ParseDuration(*in.Timeout)
		switch {
		case err != nil:
			add("timeout", "%s", err)
		case d <= 0 || d > config.MaxTimeout:
			add("timeout", "must be greater than 0 and at most %s", model.FormatDuration(config.MaxTimeout))
		default:
			timeout = d
		}
	}

	policy := up.Retry
	if in.Retry != nil {
		p, err := retry.Resolve(up.Retry, *in.Retry)
		for _, fe := range retry.FieldErrors(err) {
			field := "retry"
			if fe.Field != "" {
				field += "." + fe.Field
			}
			add(field, "%s", fe.Message)
		}
		if err == nil {
			policy = p
		}
	}

	if len(in.Tags) > maxTags {
		add("tags", "at most %d tags are allowed", maxTags)
	}
	for k, v := range in.Tags {
		if n := utf8.RuneCountInString(k); n < 1 || n > maxTagKeyLength {
			add("tags."+k, "keys must be between 1 and %d characters", maxTagKeyLength)
		}
		if utf8.RuneCountInString(v) > maxTagValueLength {
			add("tags."+k, "values must be at most %d characters", maxTagValueLength)
		}
	}

	callbackURL := up.CallbackURL
	if in.CallbackURL != nil {
		callbackURL = *in.CallbackURL
		if callbackURL != "" {
			if err := cb.Config.CheckCallbackURL(callbackURL); err != nil {
				add("callback_url", "%s", err)
			}
		}
	}
	if callbackURL != "" && !cb.Enabled {
		add("callback_url", "callbacks are disabled: set HOOKYARD_CALLBACK_SECRETS on the server to sign them")
	}
	var onResult string
	if in.OnResult != nil {
		onResult = *in.OnResult
		if n := utf8.RuneCountInString(onResult); n < 1 || n > maxOnResultLength {
			add("on_result", "must be between 1 and %d characters", maxOnResultLength)
		}
	}

	slices.SortStableFunc(problems, func(a, b fieldError) int { return strings.Compare(a.Field, b.Field) })
	return store.NewRequest{
		CallbackURL:  callbackURL,
		OnResult:     onResult,
		Upstream:     in.Upstream,
		Method:       method,
		Path:         in.Path,
		Headers:      headers,
		Body:         in.Body,
		DedupeKey:    dedupeKey,
		DedupeWindow: up.DedupeWindow,
		Retry:        policy,
		Timeout:      timeout,
		Tags:         in.Tags,
		DeliverAt:    in.DeliverAt,
	}, problems
}

// validatePath returns a problem description, or "" if path is acceptable.
func validatePath(path string) string {
	switch {
	case path == "":
		return "is required"
	case !strings.HasPrefix(path, "/"):
		return `must start with "/"`
	case strings.HasPrefix(path, "//"):
		return `must not start with "//"`
	case len(path) > maxPathLength:
		return fmt.Sprintf("must be at most %d characters", maxPathLength)
	case strings.ContainsFunc(path, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		return "must not contain spaces or control characters (percent-encode them)"
	case strings.Contains(path, "#"):
		return "must not contain a fragment"
	}
	if u, err := url.Parse(path); err != nil || u.Scheme != "" || u.Host != "" {
		return "must be a path relative to the upstream's base URL, not an absolute URL"
	}
	return ""
}

// decodeJSON decodes the request body into v, rejecting unknown fields and
// oversized bodies. It writes an error response and returns false on failure.
//
// The body is always parsed as JSON regardless of Content-Type: rejecting
// curl's default form content type would break copy-pasted examples, and a
// non-JSON body fails to parse anyway.
func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.v1.MaxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, fmt.Sprintf("request body exceeds %d bytes", tooLarge.Limit))
		case errors.Is(err, io.EOF):
			writeError(w, http.StatusBadRequest, codeBadRequest, "request body is empty")
		default:
			writeError(w, http.StatusBadRequest, codeBadRequest, "invalid JSON: "+strings.TrimPrefix(err.Error(), "json: "))
		}
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid JSON: unexpected data after the object")
		return false
	}
	return true
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, op string, err error) {
	s.log.Error("request failed", "op", op, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, codeInternal, "internal server error")
}

// requestJSON is the API representation of a request.
type requestJSON struct {
	ID             string            `json:"id"`
	Upstream       string            `json:"upstream"`
	Method         string            `json:"method"`
	Path           string            `json:"path"`
	Headers        map[string]string `json:"headers"`
	Body           json.RawMessage   `json:"body"`
	DedupeKey      *string           `json:"dedupe_key"`
	Status         model.Status      `json:"status"`
	AttemptCount   int               `json:"attempt_count"`
	Retry          retryPolicyJSON   `json:"retry"`
	Timeout        model.Duration    `json:"timeout"`
	Tags           map[string]string `json:"tags"`
	DeliverAt      *time.Time        `json:"deliver_at"`
	NextAttemptAt  *time.Time        `json:"next_attempt_at"`
	LastError      *deliveryError    `json:"last_error"`
	LastStatusCode *int              `json:"last_status_code"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	CompletedAt    *time.Time        `json:"completed_at"`
	CallbackURL    *string           `json:"callback_url"`
	OnResult       *string           `json:"on_result"`
}

type retryPolicyJSON struct {
	Preset          string         `json:"preset,omitempty"`
	MaxAttempts     int            `json:"max_attempts"`
	InitialInterval model.Duration `json:"initial_interval"`
	MaxInterval     model.Duration `json:"max_interval"`
	Multiplier      float64        `json:"multiplier"`
	MaxAge          model.Duration `json:"max_age"`
}

type deliveryError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func toRequestJSON(r model.Request) requestJSON {
	out := requestJSON{
		ID:             r.ID,
		Upstream:       r.Upstream,
		Method:         r.Method,
		Path:           r.Path,
		Headers:        nonNil(r.Headers),
		Body:           r.Body,
		Status:         r.Status,
		AttemptCount:   r.AttemptCount,
		Retry:          toRetryJSON(r.Retry),
		Timeout:        model.Duration(r.Timeout),
		Tags:           nonNil(r.Tags),
		DeliverAt:      utcPtr(r.DeliverAt),
		NextAttemptAt:  utcPtr(r.NextAttemptAt),
		LastStatusCode: r.LastStatusCode,
		CreatedAt:      r.CreatedAt.UTC(),
		UpdatedAt:      r.UpdatedAt.UTC(),
		CompletedAt:    utcPtr(r.CompletedAt),
	}
	if r.DedupeKey != "" {
		out.DedupeKey = &r.DedupeKey
	}
	if r.CallbackURL != "" {
		out.CallbackURL = &r.CallbackURL
	}
	if r.OnResult != "" {
		out.OnResult = &r.OnResult
	}
	if r.LastError != nil {
		out.LastError = &deliveryError{Code: r.LastError.Code, Message: r.LastError.Message}
	}
	return out
}

func toRetryJSON(p model.RetryPolicy) retryPolicyJSON {
	return retryPolicyJSON{
		Preset:          p.Preset,
		MaxAttempts:     p.MaxAttempts,
		InitialInterval: model.Duration(p.InitialInterval),
		MaxInterval:     model.Duration(p.MaxInterval),
		Multiplier:      p.Multiplier,
		MaxAge:          model.Duration(p.MaxAge),
	}
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
