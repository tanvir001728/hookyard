// Package callback builds and signs the events Hookyard sends to an
// application when one of its requests finishes. Signatures follow the
// Standard Webhooks specification (https://www.standardwebhooks.com/), so any
// compatible library can verify them.
package callback

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
)

// Event types, one per final request status.
const (
	TypeSucceeded = "request.succeeded"
	TypeDead      = "request.dead"
	TypeUnknown   = "request.unknown"
	TypeCanceled  = "request.canceled"
)

// TypeFor returns the event type for a final status, or "" for others.
func TypeFor(s model.Status) string {
	switch s {
	case model.StatusSucceeded:
		return TypeSucceeded
	case model.StatusDead:
		return TypeDead
	case model.StatusUnknown:
		return TypeUnknown
	case model.StatusCanceled:
		return TypeCanceled
	}
	return ""
}

// Event is the JSON body of a callback.
type Event struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Data      EventData `json:"data"`
}

// EventData describes the request as of the moment it finished.
type EventData struct {
	RequestID    string            `json:"request_id"`
	Upstream     string            `json:"upstream"`
	Method       string            `json:"method"`
	Path         string            `json:"path"`
	Status       model.Status      `json:"status"`
	OnResult     *string           `json:"on_result"`
	Tags         map[string]string `json:"tags"`
	DedupeKey    *string           `json:"dedupe_key"`
	AttemptCount int               `json:"attempt_count"`
	LastError    *Error            `json:"last_error"`
	Response     *Response         `json:"response"`
	CompletedAt  *time.Time        `json:"completed_at"`
}

// Error is why the request failed.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Response is the upstream's last response, if any.
type Response struct {
	StatusCode    int               `json:"status_code"`
	Headers       map[string]string `json:"headers"`
	Body          string            `json:"body"`
	BodyTruncated bool              `json:"body_truncated"`
}

// Build returns the event for a request that just reached a final state,
// with the last attempt if there was one.
func Build(req model.Request, last *model.Attempt, now time.Time) Event {
	d := EventData{
		RequestID:    req.ID,
		Upstream:     req.Upstream,
		Method:       req.Method,
		Path:         req.Path,
		Status:       req.Status,
		Tags:         req.Tags,
		AttemptCount: req.AttemptCount,
		CompletedAt:  req.CompletedAt,
	}
	if d.Tags == nil {
		d.Tags = map[string]string{}
	}
	if req.OnResult != "" {
		d.OnResult = &req.OnResult
	}
	if req.DedupeKey != "" {
		d.DedupeKey = &req.DedupeKey
	}
	if req.LastError != nil {
		d.LastError = &Error{Code: req.LastError.Code, Message: req.LastError.Message}
	}
	if last != nil && last.Response != nil && last.StatusCode != nil {
		d.Response = &Response{StatusCode: *last.StatusCode, Headers: last.Response.Headers, Body: last.Response.Body, BodyTruncated: last.Response.BodyTruncated}
		if d.Response.Headers == nil {
			d.Response.Headers = map[string]string{}
		}
	}
	return Event{Type: TypeFor(req.Status), Timestamp: now.UTC(), Data: d}
}

// Secret is a Standard Webhooks signing secret ("whsec_" + base64).
type Secret struct{ key []byte }

// ParseSecrets parses a comma-separated list of secrets. The first one is
// the newest; all of them sign, so receivers can rotate without downtime.
func ParseSecrets(s string) ([]Secret, error) {
	var out []Secret
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		raw, ok := strings.CutPrefix(part, "whsec_")
		if !ok {
			return nil, errors.New(`callback secrets must start with "whsec_" (generate one with: echo "whsec_$(openssl rand -base64 32)")`)
		}
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(key) < 24 {
			return nil, errors.New("callback secrets must be whsec_ followed by at least 24 bytes of base64")
		}
		out = append(out, Secret{key: key})
	}
	return out, nil
}

// Sign returns the webhook-signature header value for a message: one
// "v1,<base64 HMAC-SHA256>" per secret, separated by spaces.
func Sign(secrets []Secret, id string, ts time.Time, body []byte) string {
	sigs := make([]string, len(secrets))
	content := id + "." + strconv.FormatInt(ts.Unix(), 10) + "." + string(body)
	for i, s := range secrets {
		mac := hmac.New(sha256.New, s.key)
		mac.Write([]byte(content))
		sigs[i] = "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}
	return strings.Join(sigs, " ")
}

// Verify checks a signature header against the secrets and the timestamp
// tolerance. It is used by tests and documents the receiving side.
func Verify(secrets []Secret, id, timestamp, signature string, body []byte, now time.Time, tolerance time.Duration) error {
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid webhook-timestamp")
	}
	ts := time.Unix(sec, 0)
	if now.Sub(ts) > tolerance || ts.Sub(now) > tolerance {
		return fmt.Errorf("webhook-timestamp is outside the %s tolerance", tolerance)
	}
	expected := strings.Fields(Sign(secrets, id, ts, body))
	for _, got := range strings.Fields(signature) {
		for _, want := range expected {
			if hmac.Equal([]byte(got), []byte(want)) {
				return nil
			}
		}
	}
	return errors.New("no matching signature")
}

// Marshal encodes an event as the callback body.
func Marshal(e Event) ([]byte, error) { return json.Marshal(e) }
