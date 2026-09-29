package model

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	tests := map[time.Duration]string{
		0:                                  "0s",
		500 * time.Millisecond:             "500ms",
		30 * time.Second:                   "30s",
		90 * time.Minute:                   "1h30m",
		72 * time.Hour:                     "72h",
		time.Hour + 1500*time.Millisecond:  "1h1s500ms",
		-2 * time.Second:                   "-2s",
		1500 * time.Microsecond:            "1ms500us",
		10*time.Minute + 0*time.Nanosecond: "10m",
	}
	for in, want := range tests {
		if got := FormatDuration(in); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestDurationJSONRoundTrip(t *testing.T) {
	var v struct {
		D Duration `json:"d"`
	}
	if err := json.Unmarshal([]byte(`{"d":"1h30m"}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.D.Std() != 90*time.Minute {
		t.Fatalf("got %v", v.D.Std())
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"d":"1h30m"}` {
		t.Errorf("marshal = %s", out)
	}
}

func TestParseDurationErrors(t *testing.T) {
	for _, s := range []string{"", "30", "soon", "-5s"} {
		if _, err := ParseDuration(s); err == nil {
			t.Errorf("ParseDuration(%q) should fail", s)
		}
	}
}
