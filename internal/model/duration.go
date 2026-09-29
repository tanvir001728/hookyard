package model

import (
	"fmt"
	"strings"
	"time"
)

// Duration is a time.Duration written as a human-friendly string such as
// "500ms", "30s" or "1h30m" in JSON and YAML.
type Duration time.Duration

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String formats d compactly: "1h30m" rather than "1h30m0s".
func (d Duration) String() string { return FormatDuration(time.Duration(d)) }

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler. Negative values are rejected.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := ParseDuration(string(b))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// ParseDuration parses a non-negative duration such as "30s" or "1h30m".
func ParseDuration(s string) (time.Duration, error) {
	v, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: use a number with a unit, such as \"500ms\", \"30s\", \"5m\" or \"2h\"", s)
	}
	if v < 0 {
		return 0, fmt.Errorf("invalid duration %q: must not be negative", s)
	}
	return v, nil
}

// FormatDuration renders d using the largest exact units, for example "2h",
// "1h30m", "45s" or "250ms".
func FormatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	var b strings.Builder
	if d < 0 {
		b.WriteByte('-')
		d = -d
	}
	units := []struct {
		size time.Duration
		name string
	}{{time.Hour, "h"}, {time.Minute, "m"}, {time.Second, "s"}, {time.Millisecond, "ms"}, {time.Microsecond, "us"}, {1, "ns"}}
	for _, u := range units {
		if n := d / u.size; n > 0 {
			fmt.Fprintf(&b, "%d%s", n, u.name)
			d -= n * u.size
		}
	}
	return b.String()
}
