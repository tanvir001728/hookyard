// Package ratelimit implements per-upstream token buckets.
package ratelimit

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rate is a number of events per second. Zero means unlimited.
type Rate float64

// ParseRate parses "10/s", "600/m" or "3600/h".
func ParseRate(s string) (Rate, error) {
	n, unit, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok {
		return 0, fmt.Errorf("invalid rate %q: use a number per unit, such as \"10/s\", \"600/m\" or \"3600/h\"", s)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
	if err != nil || v <= 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, fmt.Errorf("invalid rate %q: the number must be positive", s)
	}
	switch strings.TrimSpace(unit) {
	case "s":
		return Rate(v), nil
	case "m":
		return Rate(v / 60), nil
	case "h":
		return Rate(v / 3600), nil
	default:
		return 0, fmt.Errorf("invalid rate %q: the unit must be s, m or h", s)
	}
}

// String renders the rate in the most natural unit.
func (r Rate) String() string {
	switch {
	case r == 0:
		return "unlimited"
	case r >= 1:
		return strconv.FormatFloat(float64(r), 'f', -1, 64) + "/s"
	case r*60 >= 1:
		return strconv.FormatFloat(float64(r*60), 'f', -1, 64) + "/m"
	default:
		return strconv.FormatFloat(float64(r*3600), 'f', -1, 64) + "/h"
	}
}

// DefaultBurst is the burst used when none is configured: one second's worth
// of tokens, at least one.
func DefaultBurst(r Rate) int {
	return max(1, int(math.Ceil(float64(r))))
}

// Bucket is a token bucket. It is safe for concurrent use.
type Bucket struct {
	mu           sync.Mutex
	rate         float64 // tokens per second
	burst        float64
	tokens       float64
	last         time.Time
	blockedUntil time.Time
}

// NewBucket returns a full bucket.
func NewBucket(r Rate, burst int, now time.Time) *Bucket {
	return &Bucket{rate: float64(r), burst: float64(burst), tokens: float64(burst), last: now}
}

func (b *Bucket) refill(now time.Time) {
	if now.After(b.last) {
		b.tokens = math.Min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
		b.last = now
	}
}

// Available returns how many whole tokens can be taken now (0 while blocked).
func (b *Bucket) Available(now time.Time) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if now.Before(b.blockedUntil) {
		return 0
	}
	b.refill(now)
	return int(b.tokens)
}

// Take removes n tokens. Callers take at most what Available reported.
func (b *Bucket) Take(n int, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(now)
	b.tokens = math.Max(0, b.tokens-float64(n))
}

// NextAt returns when the next whole token will be available.
func (b *Bucket) NextAt(now time.Time) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if now.Before(b.blockedUntil) {
		return b.blockedUntil
	}
	b.refill(now)
	if b.tokens >= 1 {
		return now
	}
	return now.Add(time.Duration((1 - b.tokens) / b.rate * float64(time.Second)))
}

// BlockUntil stops the bucket from handing out tokens until t, for example
// after the upstream answered 429 with Retry-After. It also empties it, so
// traffic resumes gradually.
func (b *Bucket) BlockUntil(t time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t.After(b.blockedUntil) {
		b.blockedUntil = t
	}
	b.tokens = 0
	b.last = t
}
