// Package classify decides whether an upstream response is a success, a
// failure worth retrying, or a permanent failure, using per-upstream rules.
package classify

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tanvir001728/hookyard/internal/model"
)

// Outcome values for a rule's "then".
const (
	Success = "success"
	Retry   = "retry"
	Fail    = "fail"
)

// Rule matches a response by status and, optionally, a value in its JSON body.
type Rule struct {
	// Name labels the rule in attempts; empty means "rule N".
	Name string
	// Status matches the status code; nil matches any.
	Status *StatusMatcher
	// Path is a dot path into the JSON body, such as "error.code" or
	// "items.0.state"; empty means the rule doesn't look at the body.
	Path []string
	Cond Condition
	Then model.AttemptOutcome
}

// Condition is what the value at Path must satisfy. Exactly one is set.
type Condition struct {
	Equals    *Value
	NotEquals *Value
	In        []Value
	Exists    *bool
}

// Value is a JSON scalar to compare with.
type Value struct{ V any }

// StatusMatcher matches status codes: exact codes, classes such as 4xx, and
// ranges such as 500-599.
type StatusMatcher struct {
	spec   string
	ranges [][2]int
}

var classRe = regexp.MustCompile(`^([1-5])xx$`)

// ParseStatus parses "200", "4xx", "500-599", or a comma-separated list.
func ParseStatus(spec string) (*StatusMatcher, error) {
	m := &StatusMatcher{spec: spec}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		switch {
		case classRe.MatchString(part):
			d, _ := strconv.Atoi(part[:1])
			m.ranges = append(m.ranges, [2]int{d * 100, d*100 + 99})
		case strings.Contains(part, "-"):
			lo, hi, _ := strings.Cut(part, "-")
			a, err1 := strconv.Atoi(strings.TrimSpace(lo))
			b, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 != nil || err2 != nil || a < 100 || b > 599 || a > b {
				return nil, fmt.Errorf("invalid status range %q", part)
			}
			m.ranges = append(m.ranges, [2]int{a, b})
		default:
			n, err := strconv.Atoi(part)
			if err != nil || n < 100 || n > 599 {
				return nil, fmt.Errorf("invalid status %q: use a code (200), a class (4xx), a range (500-599) or a list", part)
			}
			m.ranges = append(m.ranges, [2]int{n, n})
		}
	}
	return m, nil
}

// Match reports whether status is matched.
func (m *StatusMatcher) Match(status int) bool {
	for _, r := range m.ranges {
		if status >= r[0] && status <= r[1] {
			return true
		}
	}
	return false
}

func (m *StatusMatcher) String() string { return m.spec }

// ParsePath splits a dot path. Segments are object keys or array indexes.
func ParsePath(p string) ([]string, error) {
	p = strings.TrimPrefix(strings.TrimSpace(p), "$.")
	if p == "" {
		return nil, fmt.Errorf("body path must not be empty")
	}
	parts := strings.Split(p, ".")
	for _, s := range parts {
		if s == "" {
			return nil, fmt.Errorf("invalid body path %q", p)
		}
	}
	return parts, nil
}

// lookup walks a decoded JSON document. It reports false if the path doesn't exist.
func lookup(doc any, path []string) (any, bool) {
	cur := doc
	for _, seg := range path {
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(v) {
				return nil, false
			}
			cur = v[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// equal compares a JSON value with a configured scalar. Numbers compare by
// value regardless of type; everything else by its JSON form.
func equal(got any, want Value) bool {
	if a, ok := toFloat(got); ok {
		if b, ok := toFloat(want.V); ok {
			return a == b
		}
		return false
	}
	return fmt.Sprint(got) == fmt.Sprint(want.V) && sameKind(got, want.V)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	}
	return 0, false
}

func sameKind(a, b any) bool {
	switch a.(type) {
	case string:
		_, ok := b.(string)
		return ok
	case bool:
		_, ok := b.(bool)
		return ok
	case nil:
		return b == nil
	}
	return false
}

func (r Rule) matches(status int, doc func() (any, bool)) bool {
	if r.Status != nil && !r.Status.Match(status) {
		return false
	}
	if len(r.Path) == 0 {
		return true
	}
	d, ok := doc()
	if !ok {
		// A rule about the body can't match a body that isn't JSON.
		return false
	}
	v, found := lookup(d, r.Path)
	c := r.Cond
	switch {
	case c.Exists != nil:
		return found == *c.Exists
	case c.Equals != nil:
		return found && equal(v, *c.Equals)
	case c.NotEquals != nil:
		return found && !equal(v, *c.NotEquals)
	case c.In != nil:
		if !found {
			return false
		}
		for _, w := range c.In {
			if equal(v, w) {
				return true
			}
		}
		return false
	}
	return found
}

// Rules is an ordered list of rules; the first match wins.
type Rules []Rule

// Classify returns the outcome of the first rule that matches the response
// and that rule's label, or ok=false if none matches.
func (rs Rules) Classify(status int, body []byte) (outcome model.AttemptOutcome, label string, ok bool) {
	var (
		parsed bool
		doc    any
		isJSON bool
	)
	getDoc := func() (any, bool) {
		if !parsed {
			parsed = true
			isJSON = json.Unmarshal(body, &doc) == nil
		}
		return doc, isJSON
	}
	for i, r := range rs {
		if r.matches(status, getDoc) {
			return r.Then, r.Label(i), true
		}
	}
	return "", "", false
}

// OutcomeFromThen converts a rule's "then" into an attempt outcome.
func OutcomeFromThen(then string) (model.AttemptOutcome, error) {
	switch then {
	case Success:
		return model.OutcomeSuccess, nil
	case Retry:
		return model.OutcomeRetryableFailure, nil
	case Fail:
		return model.OutcomePermanentFailure, nil
	}
	return "", fmt.Errorf("then must be success, retry or fail, got %q", then)
}

// ThenFromOutcome is the inverse of OutcomeFromThen.
func ThenFromOutcome(o model.AttemptOutcome) string {
	switch o {
	case model.OutcomeSuccess:
		return Success
	case model.OutcomeRetryableFailure:
		return Retry
	default:
		return Fail
	}
}

// Label is the rule's name, or "rule N" for the i-th (0-based) unnamed rule.
func (r Rule) Label(i int) string {
	if r.Name != "" {
		return r.Name
	}
	return fmt.Sprintf("rule %d", i+1)
}

// String describes the condition as in hookyard.yaml, such as `equals "FAILED"`.
func (c Condition) String() string {
	val := func(v Value) string {
		b, _ := json.Marshal(v.V)
		return string(b)
	}
	switch {
	case c.Equals != nil:
		return "equals " + val(*c.Equals)
	case c.NotEquals != nil:
		return "not_equals " + val(*c.NotEquals)
	case c.In != nil:
		parts := make([]string, len(c.In))
		for i, v := range c.In {
			parts[i] = val(v)
		}
		return "in [" + strings.Join(parts, ", ") + "]"
	case c.Exists != nil && *c.Exists:
		return "exists"
	case c.Exists != nil:
		return "is missing"
	}
	return ""
}
