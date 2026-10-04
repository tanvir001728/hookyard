package ratelimit

import (
	"math"
	"testing"
	"time"
)

func TestParseRate(t *testing.T) {
	tests := []struct {
		in   string
		want Rate
	}{{"10/s", 10}, {"600/m", 10}, {"3600/h", 1}, {" 2.5/s ", 2.5}} // surrounding spaces are trimmed
	for _, tt := range tests {
		got, err := ParseRate(tt.in)
		if err != nil || math.Abs(float64(got-tt.want)) > 1e-9 {
			t.Errorf("ParseRate(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"10", "0/s", "-1/s", "10/d", "x/s", ""} {
		if _, err := ParseRate(bad); err == nil {
			t.Errorf("ParseRate(%q) should fail", bad)
		}
	}
}

func TestRateString(t *testing.T) {
	for r, want := range map[Rate]string{0: "unlimited", 10: "10/s", 0.5: "30/m", 1.0 / 3600: "1/h"} {
		if got := r.String(); got != want {
			t.Errorf("%v.String() = %q, want %q", float64(r), got, want)
		}
	}
}

func TestBucket(t *testing.T) {
	t0 := time.Unix(1000, 0)
	b := NewBucket(2, 4, t0) // 2/s, burst 4

	if n := b.Available(t0); n != 4 {
		t.Fatalf("a new bucket starts full: %d", n)
	}
	b.Take(4, t0)
	if n := b.Available(t0); n != 0 {
		t.Fatalf("after taking everything: %d", n)
	}
	if next := b.NextAt(t0); next != t0.Add(500*time.Millisecond) {
		t.Errorf("next token at %v, want +500ms", next.Sub(t0))
	}
	if n := b.Available(t0.Add(time.Second)); n != 2 {
		t.Errorf("after 1s at 2/s: %d tokens, want 2", n)
	}
	if n := b.Available(t0.Add(time.Hour)); n != 4 {
		t.Errorf("refill is capped at burst: %d", n)
	}
}

func TestBucketBlockUntil(t *testing.T) {
	t0 := time.Unix(1000, 0)
	b := NewBucket(10, 10, t0)
	b.BlockUntil(t0.Add(5 * time.Second))

	if n := b.Available(t0.Add(4 * time.Second)); n != 0 {
		t.Errorf("blocked bucket must give nothing: %d", n)
	}
	if next := b.NextAt(t0); next != t0.Add(5*time.Second) {
		t.Errorf("next token while blocked: %v", next.Sub(t0))
	}
	// Traffic resumes gradually after the block.
	if n := b.Available(t0.Add(5*time.Second + 300*time.Millisecond)); n != 3 {
		t.Errorf("0.3s after unblocking at 10/s: %d, want 3", n)
	}
}

func TestDefaultBurst(t *testing.T) {
	for r, want := range map[Rate]int{0.1: 1, 1: 1, 2.5: 3, 50: 50} {
		if got := DefaultBurst(r); got != want {
			t.Errorf("DefaultBurst(%v) = %d, want %d", float64(r), got, want)
		}
	}
}
