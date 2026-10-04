package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/retry"
	"github.com/tanvir001728/hookyard/internal/store"
	"github.com/tanvir001728/hookyard/internal/version"
)

// MaxResponseBody is how much of an upstream response body is stored.
const MaxResponseBody = 64 << 10

// Delivery error codes, as documented in api/openapi.yaml.
const (
	ErrCodeTimeout    = "timeout"
	ErrCodeConnection = "connection"
	ErrCodeHTTPStatus = "http_status"
	ErrCodeInternal   = "internal"
)

func newHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 32
	return &http.Client{
		Transport: transport,
		// A redirected POST silently becomes a GET, so redirects are never
		// followed: the 3xx response is recorded as the outcome instead.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// AttemptResult is what happened during one HTTP attempt.
type AttemptResult struct {
	StatusCode int // 0 if no response was received
	Headers    http.Header
	Error      *model.DeliveryError
}

// deliver makes one attempt for a claimed request and records the outcome.
func (e *Engine) deliver(ctx context.Context, c store.Claim) {
	req := c.Request
	number := req.AttemptCount + 1
	log := e.log.With("id", req.ID, "upstream", req.Upstream, "attempt", number)

	started := e.now()
	result, resp := e.send(ctx, req, number)
	finished := e.now()

	d := e.decide(req, number, result, finished)

	// A 429 with Retry-After throttles the whole upstream, not just this
	// request, so other requests don't run into the same limit.
	if result.StatusCode == http.StatusTooManyRequests && result.Headers != nil {
		if wait, ok := retry.ParseRetryAfter(result.Headers.Get("Retry-After"), finished); ok && wait > 0 {
			e.limits.block(req.Upstream, finished.Add(wait))
			log.Info("upstream rate limited; pausing its deliveries", "until", finished.Add(wait))
		}
	}
	attempt := model.Attempt{
		RequestID: req.ID,
		Number:    number,
		StartedAt: started,
		Duration:  finished.Sub(started),
		Outcome:   d.Outcome,
		Error:     result.Error,
		Response:  resp,
		RetryAt:   d.RetryAt,
	}
	if result.StatusCode != 0 {
		attempt.StatusCode = &result.StatusCode
	}

	err := e.store.RecordAttempt(ctx, store.AttemptRecord{
		Attempt:        attempt,
		Status:         d.Status,
		NextAttemptAt:  d.RetryAt,
		LeaseExpiresAt: c.LeaseExpiresAt,
		LastError:      d.LastError,
	})
	switch {
	case errors.Is(err, store.ErrLeaseLost):
		log.Warn("delivery outcome discarded: the lease expired and the request was handed to another worker")
		return
	case err != nil:
		// The lease will expire and the request will be delivered again.
		log.Error("recording delivery outcome failed", "error", err)
		return
	}

	if e.cfg.Observer != nil {
		e.cfg.Observer(req.Upstream, finished, attempt.Duration, d.Outcome, d.Status)
	}

	attrs := []any{"status", d.Status, "http_status", result.StatusCode, "duration", attempt.Duration}
	if result.Error != nil {
		attrs = append(attrs, "error", result.Error.Message)
	}
	if d.LastError != nil {
		attrs = append(attrs, "reason", d.LastError.Message)
	}
	switch d.Status {
	case model.StatusSucceeded:
		log.Debug("delivered", attrs...)
	case model.StatusFailed:
		log.Info("delivery failed, retry scheduled", append(attrs, "retry_at", d.RetryAt)...)
	default:
		log.Warn("delivery failed permanently, moved to the dead-letter queue", attrs...)
	}
}

// send performs the HTTP call. The returned response is nil if none arrived.
func (e *Engine) send(ctx context.Context, req model.Request, attempt int) (AttemptResult, *model.AttemptResponse) {
	up, ok := e.upstreams.Get(req.Upstream)
	if !ok {
		// The upstream was removed from the config after the request was
		// enqueued. Retrying lets an operator restore it.
		return AttemptResult{Error: &model.DeliveryError{
			Code:    ErrCodeInternal,
			Message: fmt.Sprintf("upstream %q is no longer configured", req.Upstream),
		}}, nil
	}

	body, contentType, err := encodeBody(req.Body)
	if err != nil {
		return AttemptResult{Error: &model.DeliveryError{Code: ErrCodeInternal, Message: err.Error()}}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, up.BaseURL.String()+req.Path, body)
	if err != nil {
		return AttemptResult{Error: &model.DeliveryError{Code: ErrCodeInternal, Message: "build request: " + err.Error()}}, nil
	}
	httpReq.Header.Set("User-Agent", "Hookyard/"+version.Version)
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	// Upstream headers usually carry credentials, so they win.
	for k, v := range up.Headers {
		httpReq.Header.Set(k, v)
	}
	// Lets vendors deduplicate retries of the same request.
	httpReq.Header.Set("Hookyard-Request-Id", req.ID)
	httpReq.Header.Set("Hookyard-Attempt", strconv.Itoa(attempt))

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return AttemptResult{Error: transportError(err, req.Timeout)}, nil
	}
	defer resp.Body.Close()

	data, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBody+1))
	stored := &model.AttemptResponse{
		Headers:       flattenHeaders(resp.Header),
		Body:          string(data[:min(len(data), MaxResponseBody)]),
		BodyTruncated: len(data) > MaxResponseBody,
	}
	result := AttemptResult{StatusCode: resp.StatusCode, Headers: resp.Header}
	if readErr != nil {
		stored.BodyTruncated = true
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		result.Error = &model.DeliveryError{
			Code:    ErrCodeHTTPStatus,
			Message: fmt.Sprintf("upstream responded with %d %s", resp.StatusCode, http.StatusText(resp.StatusCode)),
		}
	}
	return result, stored
}

// encodeBody turns the stored JSON value into the bytes to send. A JSON string
// is sent verbatim, with no Content-Type; anything else is sent as JSON.
func encodeBody(raw json.RawMessage) (io.Reader, string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, "", nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, "", fmt.Errorf("decode string body: %w", err)
		}
		return bytes.NewReader([]byte(s)), "", nil
	}
	return bytes.NewReader(raw), "application/json", nil
}

func transportError(err error, timeout time.Duration) *model.DeliveryError {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &model.DeliveryError{Code: ErrCodeTimeout, Message: fmt.Sprintf("no response within %s", model.FormatDuration(timeout))}
	}
	return &model.DeliveryError{Code: ErrCodeConnection, Message: err.Error()}
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}
