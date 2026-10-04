package worker

import (
	"sync"
	"time"

	"github.com/tanvir001728/hookyard/internal/config"
	"github.com/tanvir001728/hookyard/internal/ratelimit"
)

// limiter tracks per-upstream rate limits, concurrency and temporary blocks
// (after 429 + Retry-After) for this process.
type limiter struct {
	mu        sync.Mutex
	upstreams map[string]*upstreamLimits
}

type upstreamLimits struct {
	bucket         *ratelimit.Bucket // nil without a rate limit
	maxConcurrency int               // 0 means unlimited
	inFlight       int
	blockedUntil   time.Time
}

func newLimiter(reg *config.Registry, now time.Time) *limiter {
	l := &limiter{upstreams: map[string]*upstreamLimits{}}
	for _, u := range reg.All() {
		ul := &upstreamLimits{maxConcurrency: u.Limits.MaxConcurrency}
		if u.Limits.RateLimit > 0 {
			ul.bucket = ratelimit.NewBucket(u.Limits.RateLimit, u.Limits.Burst, now)
		}
		l.upstreams[u.Name] = ul
	}
	return l
}

// allowance returns which upstreams can't take any request now and how many
// requests each limited upstream may take. Unlimited upstreams appear in
// neither.
func (l *limiter) allowance(now time.Time) (exclude []string, caps map[string]int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	caps = map[string]int{}
	for name, u := range l.upstreams {
		if now.Before(u.blockedUntil) {
			exclude = append(exclude, name)
			continue
		}
		allow := -1 // unlimited
		if u.bucket != nil {
			allow = u.bucket.Available(now)
		}
		if u.maxConcurrency > 0 {
			free := max(0, u.maxConcurrency-u.inFlight)
			if allow < 0 || free < allow {
				allow = free
			}
		}
		switch {
		case allow == 0:
			exclude = append(exclude, name)
		case allow > 0:
			caps[name] = allow
		}
	}
	return exclude, caps
}

// acquire records that a request to upstream was claimed.
func (l *limiter) acquire(upstream string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if u := l.upstreams[upstream]; u != nil {
		u.inFlight++
		if u.bucket != nil {
			u.bucket.Take(1, now)
		}
	}
}

// release records that a delivery to upstream finished.
func (l *limiter) release(upstream string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if u := l.upstreams[upstream]; u != nil && u.inFlight > 0 {
		u.inFlight--
	}
}

// block stops deliveries to upstream until t (after 429 + Retry-After).
func (l *limiter) block(upstream string, until time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	u := l.upstreams[upstream]
	if u == nil || !until.After(u.blockedUntil) {
		return
	}
	u.blockedUntil = until
	if u.bucket != nil {
		u.bucket.BlockUntil(until)
	}
}

// nextChange returns the earliest time a throttled upstream can take a
// request again because of time passing (a new token or a block ending), or
// the zero time if none is waiting on time.
func (l *limiter) nextChange(now time.Time) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	var next time.Time
	consider := func(t time.Time) {
		if t.After(now) && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	for _, u := range l.upstreams {
		consider(u.blockedUntil)
		if u.bucket != nil && (u.maxConcurrency == 0 || u.inFlight < u.maxConcurrency) {
			consider(u.bucket.NextAt(now))
		}
	}
	return next
}
