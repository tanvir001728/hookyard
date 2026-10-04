package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/tanvir001728/hookyard/internal/callback"
	"github.com/tanvir001728/hookyard/internal/model"
)

// MaxCallbackURLLength limits callback URLs.
const MaxCallbackURLLength = 2048

// Callbacks configures completion callbacks to applications.
type Callbacks struct {
	// Allow lists the URLs callbacks may be sent to. Empty allows any http or
	// https URL.
	Allow []URLPattern
	// Timeout bounds one callback attempt.
	Timeout time.Duration
	// MaxAttempts is how many times a callback is tried before it fails.
	MaxAttempts int
}

type callbacksSchema struct {
	Allow       []string        `yaml:"allow"`
	Timeout     *model.Duration `yaml:"timeout"`
	MaxAttempts *int            `yaml:"max_attempts"`
}

// URLPattern matches callback URLs: the same scheme, the same host (or any
// subdomain for "*.example.com") and port, and a path under the pattern's
// path.
type URLPattern struct {
	raw          string
	scheme, host string
	wildcard     bool
	path         string
}

func (p URLPattern) String() string { return p.raw }

// ParseURLPattern parses an allow-list entry such as
// "https://orders.internal/hooks/" or "http://*.svc.cluster.local".
func ParseURLPattern(s string) (URLPattern, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return URLPattern{}, errors.New(`must be an http or https URL such as "https://orders.internal/hooks/"`)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return URLPattern{}, errors.New("must not contain credentials, a query or a fragment")
	}
	p := URLPattern{raw: s, scheme: u.Scheme, host: strings.ToLower(u.Host), path: u.Path}
	if rest, ok := strings.CutPrefix(p.host, "*."); ok {
		p.wildcard, p.host = true, rest
	}
	if strings.Contains(p.host, "*") {
		return URLPattern{}, errors.New(`"*" is only allowed as the first label of the host, as in "*.example.com"`)
	}
	return p, nil
}

// Match reports whether u is allowed by the pattern.
func (p URLPattern) Match(u *url.URL) bool {
	if u.Scheme != p.scheme {
		return false
	}
	host := strings.ToLower(u.Host)
	if p.wildcard {
		if !strings.HasSuffix(host, "."+p.host) {
			return false
		}
	} else if host != p.host {
		return false
	}
	if p.path == "" || p.path == "/" {
		return true
	}
	return u.Path == p.path || strings.HasPrefix(u.Path, strings.TrimSuffix(p.path, "/")+"/")
}

// CheckCallbackURL validates a callback URL against the allow-list.
func (c Callbacks) CheckCallbackURL(s string) error {
	if len(s) > MaxCallbackURLLength {
		return fmt.Errorf("must be at most %d characters", MaxCallbackURLLength)
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must be an absolute http or https URL")
	}
	if u.User != nil || u.Fragment != "" {
		return errors.New("must not contain credentials or a fragment")
	}
	if len(c.Allow) == 0 {
		return nil
	}
	for _, p := range c.Allow {
		if p.Match(u) {
			return nil
		}
	}
	return errors.New("is not allowed by callbacks.allow in hookyard.yaml")
}

func resolveCallbacks(raw callbacksSchema, add func(string, string, ...any)) Callbacks {
	c := Callbacks{Timeout: callback.DefaultTimeout, MaxAttempts: callback.DefaultMaxAttempts}
	for i, s := range raw.Allow {
		p, err := ParseURLPattern(s)
		if err != nil {
			add(fmt.Sprintf("callbacks.allow[%d]", i), "%s", err)
			continue
		}
		c.Allow = append(c.Allow, p)
	}
	if raw.Timeout != nil {
		c.Timeout = raw.Timeout.Std()
		if c.Timeout <= 0 || c.Timeout > time.Minute {
			add("callbacks.timeout", "must be greater than 0 and at most 1m")
		}
	}
	if raw.MaxAttempts != nil {
		c.MaxAttempts = *raw.MaxAttempts
		if c.MaxAttempts < 1 || c.MaxAttempts > 50 {
			add("callbacks.max_attempts", "must be between 1 and 50")
		}
	}
	return c
}
