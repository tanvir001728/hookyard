package worker

import (
	"sync"
	"time"

	"github.com/tanvir001728/hookyard/internal/breaker"
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
	breaker        *breaker.Breaker  // nil when disabled
	bucket         *ratelimit.Bucket // nil without a rate limit
	maxConcurrency int               // 0 means unlimited
	inFlight       int
	blockedUntil   time.Time
	// held keeps a just-closed breaker's upstream out of claims until the
	// paused time has been credited to its waiting requests.
	held bool
}

func newLimiter(reg *config.Registry, now time.Time) *limiter {
	l := &limiter{upstreams: map[string]*upstreamLimits{}}
	for _, u := range reg.All() {
		ul := &upstreamLimits{maxConcurrency: u.Limits.MaxConcurrency}
		if u.Breaker.Enabled {
			ul.breaker = breaker.New(u.Breaker.Config, now)
		}
		if u.Limits.RateLimit > 0 {
			ul.bucket = ratelimit.NewBucket(u.Limits.RateLimit, u.Limits.Burst, now)
		}
		l.upstreams[u.Name] = ul
	}
	return l
}

// transition is a circuit breaker state change of one upstream.
type transition struct {
	upstream string
	*breaker.Transition
}

// allowance returns which upstreams can't take any request now and how many
// requests each limited upstream may take. Unlimited upstreams appear in
// neither. It also returns breaker transitions caused by time passing.
func (l *limiter) allowance(now time.Time) (exclude []string, caps map[string]int, changes []transition) {
	l.mu.Lock()
	defer l.mu.Unlock()
	caps = map[string]int{}
	for name, u := range l.upstreams {
		if u.held || now.Before(u.blockedUntil) {
			exclude = append(exclude, name)
			continue
		}
		allow := -1 // unlimited
		if u.breaker != nil {
			n, t := u.breaker.Allowance(now)
			if t != nil {
				changes = append(changes, transition{name, t})
			}
			allow = n
		}
		if u.bucket != nil {
			if avail := u.bucket.Available(now); allow < 0 || avail < allow {
				allow = avail
			}
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
	return exclude, caps, changes
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
		if u.breaker != nil {
			u.breaker.Started(now)
		}
	}
}

// record reports an attempt's outcome to the upstream's circuit breaker. Only
// outcomes that say something about the upstream's health should be
// reported: successes and retryable failures.
func (l *limiter) record(upstream string, success bool, now time.Time) *transition {
	l.mu.Lock()
	u := l.upstreams[upstream]
	l.mu.Unlock()
	if u == nil || u.breaker == nil {
		return nil
	}
	if t := u.breaker.Record(success, now); t != nil {
		if t.To == breaker.Closed {
			// Don't claim this upstream's requests until their retry windows
			// have been extended; a claim would carry the stale window.
			l.mu.Lock()
			u.held = true
			l.mu.Unlock()
		}
		return &transition{upstream, t}
	}
	return nil
}

// unhold lets a held upstream be claimed again.
func (l *limiter) unhold(upstream string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if u := l.upstreams[upstream]; u != nil {
		u.held = false
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
		if u.breaker != nil {
			consider(u.breaker.CooldownEnds())
		}
		if u.bucket != nil && (u.maxConcurrency == 0 || u.inFlight < u.maxConcurrency) {
			consider(u.bucket.NextAt(now))
		}
	}
	return next
}
