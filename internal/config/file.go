package config

import (
	"encoding"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/tanvir001728/hookyard/internal/breaker"
	"github.com/tanvir001728/hookyard/internal/classify"
	"github.com/tanvir001728/hookyard/internal/model"
	"github.com/tanvir001728/hookyard/internal/ratelimit"
	"github.com/tanvir001728/hookyard/internal/retry"
)

// DefaultConfigFile is loaded from the working directory when no config file
// is set explicitly and it exists.
const DefaultConfigFile = "hookyard.yaml"

// Built-in defaults, used when the config file doesn't set them.
const (
	DefaultTimeout      = 30 * time.Second
	DefaultDedupeWindow = 24 * time.Hour
	MaxTimeout          = 10 * time.Minute
	MaxDedupeWindow     = 30 * 24 * time.Hour
)

// fileSchema is the YAML layout of hookyard.yaml.
type fileSchema struct {
	Defaults  defaultsSchema            `yaml:"defaults"`
	Upstreams map[string]upstreamSchema `yaml:"upstreams"`
}

type defaultsSchema struct {
	Timeout        *model.Duration `yaml:"timeout"`
	Retry          retry.Spec      `yaml:"retry"`
	DedupeWindow   *model.Duration `yaml:"dedupe_window"`
	RateLimit      *string         `yaml:"rate_limit"`
	Burst          *int            `yaml:"burst"`
	MaxConcurrency *int            `yaml:"max_concurrency"`
	Breaker        breakerSpec     `yaml:"breaker"`
}

type upstreamSchema struct {
	BaseURL        string            `yaml:"base_url"`
	Timeout        *model.Duration   `yaml:"timeout"`
	Retry          retry.Spec        `yaml:"retry"`
	Headers        map[string]string `yaml:"headers"`
	DedupeWindow   *model.Duration   `yaml:"dedupe_window"`
	RateLimit      *string           `yaml:"rate_limit"`
	Burst          *int              `yaml:"burst"`
	MaxConcurrency *int              `yaml:"max_concurrency"`
	Breaker        breakerSpec       `yaml:"breaker"`
	Classify       []ruleSchema      `yaml:"classify"`
}

// Defaults are the resolved global defaults that apply to every upstream.
type Defaults struct {
	Timeout      time.Duration
	Retry        model.RetryPolicy
	DedupeWindow time.Duration
	Limits       Limits
	Breaker      Breaker
}

// Limits throttle deliveries to one upstream, per Hookyard instance.
type Limits struct {
	// RateLimit is the sustained rate; zero means unlimited.
	RateLimit ratelimit.Rate
	// Burst is how many requests may be sent at once after a quiet period.
	Burst int
	// MaxConcurrency caps in-flight deliveries; zero means unlimited.
	MaxConcurrency int
}

// Upstream is a fully resolved upstream configuration.
type Upstream struct {
	Name    string
	BaseURL *url.URL
	Timeout time.Duration
	Retry   model.RetryPolicy
	// Headers are added to every request to this upstream. Values often hold
	// secrets and must never be exposed through the API.
	Headers      map[string]string
	DedupeWindow time.Duration
	Limits       Limits
	Breaker      Breaker
	// Classify rules decide how responses count; empty means the defaults.
	Classify classify.Rules
}

// File is a loaded and validated configuration file.
type File struct {
	// Path is where the file was loaded from, or empty if none was loaded.
	Path      string
	Defaults  Defaults
	Upstreams *Registry
}

// Empty returns a configuration with built-in defaults and no upstreams.
func Empty() *File {
	return &File{Defaults: builtinDefaults(), Upstreams: NewRegistry(nil)}
}

func builtinDefaults() Defaults {
	p, _ := retry.Preset(retry.DefaultPreset)
	return Defaults{Timeout: DefaultTimeout, Retry: p, DedupeWindow: DefaultDedupeWindow, Breaker: Breaker{Enabled: true, Config: breaker.Defaults}}
}

// LoadFile reads the config file at path. If path is empty, it loads
// DefaultConfigFile when present and otherwise returns an empty configuration.
// lookup resolves ${VAR} references; nil means os.LookupEnv.
func LoadFile(path string, lookup func(string) (string, bool)) (*File, error) {
	explicit := path != ""
	if !explicit {
		path = DefaultConfigFile
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !explicit && errors.Is(err, os.ErrNotExist) {
			return Empty(), nil
		}
		return nil, fmt.Errorf("read config file: %w", err)
	}
	f, err := ParseFile(data, lookup)
	if err != nil {
		return nil, fmt.Errorf("config file %s: %w", path, err)
	}
	f.Path = path
	return f, nil
}

// ParseFile parses and validates a config file.
func ParseFile(data []byte, lookup func(string) (string, bool)) (*File, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if err := interpolate(&root, lookup); err != nil {
		return nil, err
	}

	var raw fileSchema
	if len(root.Content) > 0 {
		if err := checkKnownFields(root.Content[0], reflect.TypeFor[fileSchema](), ""); err != nil {
			return nil, err
		}
		if err := root.Decode(&raw); err != nil {
			return nil, err
		}
	}

	return resolve(raw)
}

// checkKnownFields rejects mapping keys that don't correspond to a field of t,
// reporting the line and the valid keys. node.Decode has no strict mode, and a
// misspelled key (such as "retires") must never be silently ignored.
func checkKnownFields(n *yaml.Node, t reflect.Type, path string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// Check text-parsed values (such as durations) here, because errors from
	// Decode lack the line and field path.
	if n.Kind == yaml.ScalarNode && reflect.PointerTo(t).Implements(reflect.TypeFor[encoding.TextUnmarshaler]()) {
		v := reflect.New(t).Interface().(encoding.TextUnmarshaler)
		if err := v.UnmarshalText([]byte(n.Value)); err != nil {
			return fmt.Errorf("line %d: %s: %w", n.Line, path, err)
		}
		return nil
	}
	if t == reflect.TypeFor[yaml.Node]() || t.Kind() == reflect.Interface {
		return nil // raw values, checked where they are used
	}
	if n.Kind == yaml.SequenceNode && t.Kind() == reflect.Slice {
		for i, c := range n.Content {
			if err := checkKnownFields(c, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return nil // type mismatches are reported by Decode
	}
	if reflect.PointerTo(t).Implements(reflect.TypeFor[yaml.Unmarshaler]()) {
		return nil // custom unmarshalers validate their own keys
	}

	switch t.Kind() {
	case reflect.Map:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if err := checkKnownFields(n.Content[i+1], t.Elem(), joinPath(path, n.Content[i].Value)); err != nil {
				return err
			}
		}
	case reflect.Struct:
		fields := map[string]reflect.Type{}
		var names []string
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if name == "" || name == "-" {
				continue
			}
			fields[name] = f.Type
			names = append(names, name)
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			ft, ok := fields[key.Value]
			if !ok {
				where := "top level"
				if path != "" {
					where = path
				}
				return fmt.Errorf("line %d: unknown key %q in %s (valid keys: %s)", key.Line, key.Value, where, strings.Join(names, ", "))
			}
			if err := checkKnownFields(n.Content[i+1], ft, joinPath(path, key.Value)); err != nil {
				return err
			}
		}
	default:
	}
	return nil
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

var envRef = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// interpolate replaces ${VAR} and ${VAR:-default} in string values. "$$"
// produces a literal "$". Keys are never interpolated.
func interpolate(n *yaml.Node, lookup func(string) (string, bool)) error {
	var missing []string
	var walk func(n *yaml.Node, isKey bool)
	walk = func(n *yaml.Node, isKey bool) {
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				walk(c, false)
			}
		case yaml.MappingNode:
			for i, c := range n.Content {
				walk(c, i%2 == 0)
			}
		case yaml.ScalarNode:
			if isKey || !strings.Contains(n.Value, "$") {
				return
			}
			n.Value = envRef.ReplaceAllStringFunc(n.Value, func(m string) string {
				if m == "$$" {
					return "$"
				}
				sub := envRef.FindStringSubmatch(m)
				if v, ok := lookup(sub[1]); ok && v != "" {
					return v
				}
				if strings.Contains(m, ":-") {
					return sub[2]
				}
				missing = append(missing, fmt.Sprintf("line %d: environment variable %s is not set", n.Line, sub[1]))
				return ""
			})
			// Let plain scalars be re-typed after substitution, so that
			// max_attempts: ${N} decodes as a number.
			if n.Style == 0 {
				n.Tag = ""
			}
		default:
		}
	}
	walk(n, false)
	if len(missing) > 0 {
		return &Error{Problems: missing}
	}
	return nil
}

var upstreamNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func resolve(raw fileSchema) (*File, error) {
	var problems []string
	add := func(field, format string, args ...any) {
		problems = append(problems, field+": "+fmt.Sprintf(format, args...))
	}

	defaults := builtinDefaults()
	if raw.Defaults.Timeout != nil {
		defaults.Timeout = raw.Defaults.Timeout.Std()
		checkTimeout("defaults.timeout", defaults.Timeout, add)
	}
	if raw.Defaults.DedupeWindow != nil {
		defaults.DedupeWindow = raw.Defaults.DedupeWindow.Std()
		checkDedupeWindow("defaults.dedupe_window", defaults.DedupeWindow, add)
	}
	if p, err := retry.Resolve(defaults.Retry, raw.Defaults.Retry); err != nil {
		addRetryProblems("defaults.retry", err, add)
	} else {
		defaults.Retry = p
	}
	defaults.Limits = resolveLimits("defaults", Limits{}, raw.Defaults.RateLimit, raw.Defaults.Burst, raw.Defaults.MaxConcurrency, add)
	defaults.Breaker = resolveBreaker("defaults.breaker", defaults.Breaker, raw.Defaults.Breaker, add)

	upstreams := make([]Upstream, 0, len(raw.Upstreams))
	for name, u := range raw.Upstreams {
		prefix := "upstreams." + name
		if !upstreamNameRe.MatchString(name) {
			add(prefix, "invalid name: use lowercase letters, digits, '-' and '_' (up to 63 characters, starting with a letter or digit)")
		}

		up := Upstream{Name: name, Timeout: defaults.Timeout, Retry: defaults.Retry, DedupeWindow: defaults.DedupeWindow}
		up.Limits = resolveLimits(prefix, defaults.Limits, u.RateLimit, u.Burst, u.MaxConcurrency, add)
		up.Breaker = resolveBreaker(prefix+".breaker", defaults.Breaker, u.Breaker, add)
		up.Classify = resolveRules(prefix+".classify", u.Classify, add)

		if base, err := parseBaseURL(u.BaseURL); err != nil {
			add(prefix+".base_url", "%s", err)
		} else {
			up.BaseURL = base
		}
		if u.Timeout != nil {
			up.Timeout = u.Timeout.Std()
			checkTimeout(prefix+".timeout", up.Timeout, add)
		}
		if u.DedupeWindow != nil {
			up.DedupeWindow = u.DedupeWindow.Std()
			checkDedupeWindow(prefix+".dedupe_window", up.DedupeWindow, add)
		}
		if p, err := retry.Resolve(defaults.Retry, u.Retry); err != nil {
			addRetryProblems(prefix+".retry", err, add)
		} else {
			up.Retry = p
		}

		up.Headers = make(map[string]string, len(u.Headers))
		for k, v := range u.Headers {
			if err := ValidateHeader(k, v); err != nil {
				add(prefix+".headers."+k, "%s", err)
				continue
			}
			up.Headers[CanonicalHeaderKey(k)] = v
		}
		upstreams = append(upstreams, up)
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, &Error{Problems: problems}
	}
	return &File{Defaults: defaults, Upstreams: NewRegistry(upstreams)}, nil
}

// Upper bounds for limits, to catch typos.
const (
	maxBurst          = 100_000
	maxMaxConcurrency = 1024
)

func resolveLimits(prefix string, base Limits, rate *string, burst, concurrency *int, add func(string, string, ...any)) Limits {
	l := base
	burstSet := burst != nil
	if rate != nil {
		r, err := ratelimit.ParseRate(*rate)
		if err != nil {
			add(prefix+".rate_limit", "%s", err)
		} else {
			l.RateLimit = r
			if !burstSet {
				l.Burst = ratelimit.DefaultBurst(r)
			}
		}
	}
	if burstSet {
		if *burst < 1 || *burst > maxBurst {
			add(prefix+".burst", "must be between 1 and %d", maxBurst)
		} else {
			l.Burst = *burst
		}
		if l.RateLimit == 0 && rate == nil {
			add(prefix+".burst", "only applies together with rate_limit")
		}
	}
	if concurrency != nil {
		if *concurrency < 1 || *concurrency > maxMaxConcurrency {
			add(prefix+".max_concurrency", "must be between 1 and %d", maxMaxConcurrency)
		} else {
			l.MaxConcurrency = *concurrency
		}
	}
	return l
}

func parseBaseURL(s string) (*url.URL, error) {
	if strings.TrimSpace(s) == "" {
		return nil, errors.New("is required")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("must be an absolute http or https URL, got %q", s)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("must not contain a query string or fragment; put them in the request path")
	}
	if u.User != nil {
		return nil, errors.New("must not contain credentials; use headers with ${ENV_VAR} references instead")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

func checkTimeout(field string, d time.Duration, add func(string, string, ...any)) {
	if d <= 0 || d > MaxTimeout {
		add(field, "must be greater than 0 and at most %s", model.FormatDuration(MaxTimeout))
	}
}

func checkDedupeWindow(field string, d time.Duration, add func(string, string, ...any)) {
	if d <= 0 || d > MaxDedupeWindow {
		add(field, "must be greater than 0 and at most %s", model.FormatDuration(MaxDedupeWindow))
	}
}

func addRetryProblems(prefix string, err error, add func(string, string, ...any)) {
	for _, fe := range retry.FieldErrors(err) {
		field := prefix
		if fe.Field != "" {
			field += "." + fe.Field
		}
		add(field, "%s", fe.Message)
	}
}

// Error lists every problem found in a configuration.
type Error struct {
	Problems []string
}

func (e *Error) Error() string {
	if len(e.Problems) == 1 {
		return e.Problems[0]
	}
	return fmt.Sprintf("%d problems:\n  - %s", len(e.Problems), strings.Join(e.Problems, "\n  - "))
}
