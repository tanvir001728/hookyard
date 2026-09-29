package config

import (
	"errors"
	"fmt"
	"net/textproto"
	"slices"
	"strings"
)

// Registry holds the configured upstreams. It is immutable and safe for
// concurrent use.
type Registry struct {
	byName map[string]Upstream
	names  []string
}

// NewRegistry builds a registry from resolved upstreams.
func NewRegistry(upstreams []Upstream) *Registry {
	r := &Registry{byName: make(map[string]Upstream, len(upstreams))}
	for _, u := range upstreams {
		r.byName[u.Name] = u
		r.names = append(r.names, u.Name)
	}
	slices.Sort(r.names)
	return r
}

// Get returns the named upstream.
func (r *Registry) Get(name string) (Upstream, bool) {
	u, ok := r.byName[name]
	return u, ok
}

// Names returns all upstream names, sorted.
func (r *Registry) Names() []string { return slices.Clone(r.names) }

// All returns all upstreams, sorted by name.
func (r *Registry) All() []Upstream {
	out := make([]Upstream, 0, len(r.names))
	for _, n := range r.names {
		out = append(out, r.byName[n])
	}
	return out
}

// Len returns the number of upstreams.
func (r *Registry) Len() int { return len(r.names) }

// Suggest returns the configured name closest to name, for "did you mean"
// messages, or "" if nothing is close enough.
func (r *Registry) Suggest(name string) string {
	best, bestDist := "", -1
	for _, candidate := range r.names {
		d := levenshtein(strings.ToLower(name), candidate)
		if bestDist == -1 || d < bestDist {
			best, bestDist = candidate, d
		}
	}
	limit := max(2, len(name)/3)
	if bestDist < 0 || bestDist > limit {
		return ""
	}
	return best
}

// UnknownUpstreamMessage explains that name is not configured, with a
// suggestion when one is close.
func (r *Registry) UnknownUpstreamMessage(name string) string {
	msg := fmt.Sprintf("upstream %q is not configured", name)
	if s := r.Suggest(name); s != "" {
		return msg + fmt.Sprintf(" (did you mean %q?)", s)
	}
	if r.Len() == 0 {
		return msg + " (no upstreams are configured; add them to hookyard.yaml)"
	}
	return msg + fmt.Sprintf(" (configured upstreams: %s)", strings.Join(r.names, ", "))
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

// Headers that Hookyard manages itself and that callers may not set.
var reservedHeaders = []string{"Host", "Content-Length", "Transfer-Encoding", "Connection", "Upgrade", "Te", "Trailer", "Keep-Alive", "Proxy-Connection"}

// CanonicalHeaderKey returns the canonical form of a header name.
func CanonicalHeaderKey(k string) string { return textproto.CanonicalMIMEHeaderKey(k) }

// ValidateHeader checks a header name and value that will be sent upstream.
func ValidateHeader(name, value string) error {
	if name == "" {
		return errors.New("header name must not be empty")
	}
	for _, c := range name {
		if !isTokenChar(c) {
			return fmt.Errorf("invalid header name %q", name)
		}
	}
	if slices.Contains(reservedHeaders, CanonicalHeaderKey(name)) {
		return fmt.Errorf("header %q is managed by Hookyard and cannot be set", CanonicalHeaderKey(name))
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("header %q contains a line break or NUL character", name)
	}
	return nil
}

func isTokenChar(c rune) bool {
	if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", c)
}
