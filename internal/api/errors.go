package api

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Error codes. They are part of the public API (see api/openapi.yaml): never
// rename one.
const (
	codeBadRequest       = "bad_request"
	codeUnauthorized     = "unauthorized"
	codeNotFound         = "not_found"
	codeInvalidState     = "invalid_state"
	codePayloadTooLarge  = "payload_too_large"
	codeValidationFailed = "validation_failed"
	codeUnknownUpstream  = "unknown_upstream"
	codeInternal         = "internal"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Details []fieldError `json:"details,omitempty"`
}

// fieldError describes one invalid field in a validation_failed error.
type fieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

func writeValidationError(w http.ResponseWriter, details []fieldError) {
	msg := "1 field is invalid"
	if len(details) != 1 {
		msg = strconv.Itoa(len(details)) + " fields are invalid"
	}
	writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: errorDetail{
		Code: codeValidationFailed, Message: msg, Details: details,
	}})
}
