package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/retry"
)

const sample = `
defaults:
  timeout: 20s
  retry: quick
  dedupe_window: 12h

upstreams:
  courier-x:
    base_url: https://api.courier-x.example/v2/
    timeout: 15s
    retry:
      preset: patient
      max_attempts: ${COURIER_ATTEMPTS}
    headers:
      authorization: "Bearer ${COURIER_TOKEN}"
      X-Api-Version: "${API_VERSION:-2024-01}"
      X-Literal: "costs $$5"

  payments-y:
    base_url: http://payments.internal:8080
`

func TestParseFile(t *testing.T) {
	f, err := ParseFile([]byte(sample), env(map[string]string{"COURIER_TOKEN": "s3cret", "COURIER_ATTEMPTS": "7"}))
	if err != nil {
		t.Fatal(err)
	}

	if f.Defaults.Timeout != 20*time.Second || f.Defaults.DedupeWindow != 12*time.Hour || f.Defaults.Retry.Preset != retry.PresetQuick {
		t.Errorf("defaults = %+v", f.Defaults)
	}
	if got := f.Upstreams.Names(); strings.Join(got, ",") != "courier-x,payments-y" {
		t.Fatalf("names = %v", got)
	}

	cx, _ := f.Upstreams.Get("courier-x")
	if cx.BaseURL.String() != "https://api.courier-x.example/v2" {
		t.Errorf("base url = %s (trailing slash should be trimmed)", cx.BaseURL)
	}
	if cx.Timeout != 15*time.Second || cx.DedupeWindow != 12*time.Hour {
		t.Errorf("courier-x timeout/dedupe = %s/%s", cx.Timeout, cx.DedupeWindow)
	}
	if cx.Retry.Preset != retry.PresetPatient || cx.Retry.MaxAttempts != 7 {
		t.Errorf("courier-x retry = %+v (preset patient with max_attempts from env)", cx.Retry)
	}
	wantHeaders := map[string]string{"Authorization": "Bearer s3cret", "X-Api-Version": "2024-01", "X-Literal": "costs $5"}
	for k, v := range wantHeaders {
		if cx.Headers[k] != v {
			t.Errorf("header %s = %q, want %q", k, cx.Headers[k], v)
		}
	}

	py, _ := f.Upstreams.Get("payments-y")
	if py.Timeout != 20*time.Second || py.Retry.Preset != retry.PresetQuick {
		t.Errorf("payments-y should inherit defaults: %+v", py)
	}
}

func TestParseFileMissingEnv(t *testing.T) {
	_, err := ParseFile([]byte(sample), env(nil))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"COURIER_TOKEN is not set", "COURIER_ATTEMPTS is not set", "line "} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "API_VERSION") {
		t.Error("variables with defaults must not be reported")
	}
}

func TestParseFileUnknownKeys(t *testing.T) {
	tests := map[string]string{
		"top level":  "upstream:\n  a:\n    base_url: http://a\n",
		"upstream":   "upstreams:\n  a:\n    base_url: http://a\n    retries: 3\n",
		"defaults":   "defaults:\n  time_out: 5s\n",
		"retry spec": "upstreams:\n  a:\n    base_url: http://a\n    retry:\n      max_attempt: 3\n",
	}
	for name, src := range tests {
		_, err := ParseFile([]byte(src), env(nil))
		if err == nil || !strings.Contains(err.Error(), "unknown") || !strings.Contains(err.Error(), "line ") {
			t.Errorf("%s: expected unknown key error with a line number, got %v", name, err)
		}
	}
}

func TestParseFileBadDurationHasLineAndPath(t *testing.T) {
	_, err := ParseFile([]byte("upstreams:\n  a:\n    base_url: http://a\n    timeout: 30\n"), env(nil))
	if err == nil || !strings.Contains(err.Error(), "line 4: upstreams.a.timeout: invalid duration") {
		t.Fatalf("got %v", err)
	}
}

func TestParseFileValidation(t *testing.T) {
	src := `
defaults:
  timeout: 20m
upstreams:
  Bad_Name:
    base_url: https://ok.example
  no-url: {}
  ftp:
    base_url: ftp://files.example
  query:
    base_url: https://a.example/?x=1
  creds:
    base_url: https://user:pw@a.example
  hdr:
    base_url: https://a.example
    headers:
      Host: other.example
      "bad name": x
  pol:
    base_url: https://a.example
    retry: { max_attempts: 0, preset: forever }
`
	_, err := ParseFile([]byte(src), env(nil))
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{
		"defaults.timeout",
		"upstreams.Bad_Name: invalid name",
		"upstreams.no-url.base_url: is required",
		"upstreams.ftp.base_url: must be an absolute http or https URL",
		"upstreams.query.base_url: must not contain a query string",
		"upstreams.creds.base_url: must not contain credentials",
		`header "Host" is managed by Hookyard`,
		`invalid header name "bad name"`,
		`upstreams.pol.retry.preset: unknown preset "forever"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q:\n%v", want, err)
		}
	}
}

func TestParseFileEmpty(t *testing.T) {
	f, err := ParseFile(nil, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if f.Upstreams.Len() != 0 || f.Defaults.Retry.Preset != retry.DefaultPreset || f.Defaults.Timeout != DefaultTimeout {
		t.Errorf("empty file should give built-in defaults: %+v", f.Defaults)
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	if err := os.WriteFile(path, []byte("upstreams:\n  a:\n    base_url: http://a.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := LoadFile(path, env(nil))
	if err != nil || f.Upstreams.Len() != 1 || f.Path != path {
		t.Fatalf("LoadFile: %+v %v", f, err)
	}

	if _, err := LoadFile(filepath.Join(dir, "missing.yaml"), env(nil)); err == nil {
		t.Error("an explicitly set file that is missing must be an error")
	}

	t.Chdir(dir)
	f, err = LoadFile("", env(nil))
	if err != nil || f.Upstreams.Len() != 0 || f.Path != "" {
		t.Errorf("no default file present: want empty config, got %+v %v", f, err)
	}

	if err := os.WriteFile(filepath.Join(dir, DefaultConfigFile), []byte("upstreams:\n  b:\n    base_url: http://b.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err = LoadFile("", env(nil))
	if err != nil || f.Path != DefaultConfigFile {
		t.Errorf("default file present: got %+v %v", f, err)
	}
}

func TestRegistrySuggest(t *testing.T) {
	r := NewRegistry([]Upstream{{Name: "courier-x"}, {Name: "payments-y"}, {Name: "erp"}})
	tests := map[string]string{
		"courier-y":  "courier-x",
		"Courier-X":  "courier-x",
		"payment-y":  "payments-y",
		"crm":        "erp",
		"completely": "",
	}
	for in, want := range tests {
		if got := r.Suggest(in); got != want {
			t.Errorf("Suggest(%q) = %q, want %q", in, got, want)
		}
	}
	if msg := r.UnknownUpstreamMessage("courier-y"); !strings.Contains(msg, `did you mean "courier-x"?`) {
		t.Errorf("message = %q", msg)
	}
	if msg := NewRegistry(nil).UnknownUpstreamMessage("a"); !strings.Contains(msg, "no upstreams are configured") {
		t.Errorf("message = %q", msg)
	}
}

func TestParseFileLimits(t *testing.T) {
	src := `
defaults:
  max_concurrency: 8
upstreams:
  a:
    base_url: http://a
    rate_limit: 600/m
  b:
    base_url: http://b
    rate_limit: 5/s
    burst: 20
    max_concurrency: 2
  c:
    base_url: http://c
`
	f, err := ParseFile([]byte(src), env(nil))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := f.Upstreams.Get("a")
	b, _ := f.Upstreams.Get("b")
	c, _ := f.Upstreams.Get("c")
	if a.Limits != (Limits{RateLimit: 10, Burst: 10, MaxConcurrency: 8}) {
		t.Errorf("a = %+v (default burst is one second's worth; concurrency from defaults)", a.Limits)
	}
	if b.Limits != (Limits{RateLimit: 5, Burst: 20, MaxConcurrency: 2}) {
		t.Errorf("b = %+v", b.Limits)
	}
	if c.Limits != (Limits{MaxConcurrency: 8}) {
		t.Errorf("c = %+v (no rate limit unless configured)", c.Limits)
	}
}

func TestParseFileLimitErrors(t *testing.T) {
	src := `
upstreams:
  a:
    base_url: http://a
    rate_limit: 10 per second
    max_concurrency: 0
  b:
    base_url: http://b
    burst: 5
`
	_, err := ParseFile([]byte(src), env(nil))
	for _, want := range []string{"upstreams.a.rate_limit: invalid rate", "upstreams.a.max_concurrency: must be between 1", "upstreams.b.burst: only applies together with rate_limit"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}

func TestParseFileBreaker(t *testing.T) {
	src := `
defaults:
  breaker:
    cooldown: 10s
upstreams:
  a:
    base_url: http://a
  b:
    base_url: http://b
    breaker: off
  c:
    base_url: http://c
    breaker:
      failure_rate: 0.25
      probes: 1
`
	f, err := ParseFile([]byte(src), env(nil))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := f.Upstreams.Get("a")
	b, _ := f.Upstreams.Get("b")
	c, _ := f.Upstreams.Get("c")
	if !a.Breaker.Enabled || a.Breaker.Cooldown != 10*time.Second || a.Breaker.FailureRate != 0.5 {
		t.Errorf("a inherits defaults: %+v", a.Breaker)
	}
	if b.Breaker.Enabled {
		t.Errorf("b turned the breaker off: %+v", b.Breaker)
	}
	if !c.Breaker.Enabled || c.Breaker.FailureRate != 0.25 || c.Breaker.Probes != 1 || c.Breaker.Cooldown != 10*time.Second {
		t.Errorf("c overrides fields on top of defaults: %+v", c.Breaker)
	}

	_, err = ParseFile([]byte("upstreams:\n  a:\n    base_url: http://a\n    breaker:\n      failure_rat: 2\n"), env(nil))
	if err == nil || !strings.Contains(err.Error(), `unknown breaker field "failure_rat"`) {
		t.Errorf("typo: %v", err)
	}
	_, err = ParseFile([]byte("upstreams:\n  a:\n    base_url: http://a\n    breaker:\n      failure_rate: 2\n"), env(nil))
	if err == nil || !strings.Contains(err.Error(), "upstreams.a.breaker.failure_rate") {
		t.Errorf("range: %v", err)
	}
}
