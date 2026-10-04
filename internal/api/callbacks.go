package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/tanvir001728/hookyard/internal/config"
	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/store"
)

// callbackOptions is what request validation needs to know about callbacks.
type callbackOptions struct {
	Enabled bool
	Config  config.Callbacks
}

func (s *Server) callbackOptions() callbackOptions {
	return callbackOptions{Enabled: s.v1.CallbacksEnabled, Config: s.v1.Config.Callbacks}
}

func (s *Server) notifyCallbacks() {
	if s.v1.NotifyCallbacks != nil {
		s.v1.NotifyCallbacks()
	}
}

// callbackJSON is the API representation of a callback delivery.
type callbackJSON struct {
	ID             string       `json:"id"`
	RequestID      string       `json:"request_id"`
	Type           string       `json:"type"`
	URL            string       `json:"url"`
	RequestStatus  model.Status `json:"request_status"`
	Status         string       `json:"status"`
	AttemptCount   int          `json:"attempt_count"`
	NextAttemptAt  *time.Time   `json:"next_attempt_at"`
	LastStatusCode *int         `json:"last_status_code"`
	LastError      *string      `json:"last_error"`
	LastAttemptAt  *time.Time   `json:"last_attempt_at"`
	CreatedAt      time.Time    `json:"created_at"`
	DeliveredAt    *time.Time   `json:"delivered_at"`
}

func toCallbackJSON(c store.Callback) callbackJSON {
	out := callbackJSON{
		ID:             c.ID,
		RequestID:      c.RequestID,
		Type:           c.EventType,
		URL:            c.URL,
		RequestStatus:  c.RequestStatus,
		Status:         c.Status,
		AttemptCount:   c.AttemptCount,
		LastStatusCode: c.LastStatusCode,
		LastAttemptAt:  utcPtr(c.LastAttemptAt),
		CreatedAt:      c.CreatedAt.UTC(),
		DeliveredAt:    utcPtr(c.DeliveredAt),
	}
	if c.Status == store.CallbackPending {
		out.NextAttemptAt = utcPtr(&c.NextAttemptAt)
	}
	if c.LastError != "" {
		out.LastError = &c.LastError
	}
	return out
}

func (s *Server) handleListCallbacks(w http.ResponseWriter, r *http.Request) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	callbacks, err := s.v1.Store.ListCallbacks(r.Context(), id)
	if s.storeError(w, r, "list callbacks", id, err) {
		return
	}
	resp := struct {
		Data []callbackJSON `json:"data"`
	}{Data: make([]callbackJSON, len(callbacks))}
	for i, c := range callbacks {
		resp.Data[i] = toCallbackJSON(c)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRetryCallback(w http.ResponseWriter, r *http.Request) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	cbID := r.PathValue("callback_id")
	c, err := s.v1.Store.RetryCallback(r.Context(), id, cbID, actorFrom(r.Context()))
	var stateErr *store.CallbackStateError
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, fmt.Sprintf("callback %q of request %q not found", cbID, id))
		return
	case errors.As(err, &stateErr):
		writeError(w, http.StatusConflict, codeInvalidState, stateErr.Error())
		return
	case err != nil:
		s.internalError(w, r, "retry callback", err)
		return
	}
	s.notifyCallbacks()
	s.log.Info("callback retried", "id", cbID, "request_id", id, "actor", actorFrom(r.Context()))
	writeJSON(w, http.StatusAccepted, toCallbackJSON(c))
}
