// Package logging builds the structured logger used across Hookyard.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Format selects the log output encoding.
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// New returns a slog.Logger writing to w with the given format and level.
func New(w io.Writer, format Format, level string) (*slog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lvl}

	switch format {
	case FormatJSON:
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	case FormatText, "":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("unknown log format %q (want %q or %q)", format, FormatText, FormatJSON)
	}
}

// ParseLevel converts a level name (debug, info, warn, error) into a slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", s)
	}
}
