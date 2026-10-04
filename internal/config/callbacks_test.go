package config

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestURLPatternMatch(t *testing.T) {
	tests := []struct {
		pattern, url string
		want         bool
	}{
		{"https://orders.internal", "https://orders.internal/hooks", true},
		{"https://orders.internal/hooks/", "https://orders.internal/hooks/hookyard", true},
		{"https://orders.internal/hooks", "https://orders.internal/hooks", true},
		{"https://orders.internal/hooks", "https://orders.internal/hooks/x", true},
		{"https://orders.internal/hooks", "https://orders.internal/hooksevil", false},
		{"https://orders.internal/hooks", "https://orders.internal/other", false},
		{"https://orders.internal", "http://orders.internal/hooks", false},
		{"https://orders.internal", "https://orders.internal:8443/hooks", false},
		{"https://orders.internal:8443", "https://orders.internal:8443/hooks", true},
		{"https://orders.internal", "https://orders.internal.evil.com/hooks", false},
		{"http://*.svc.cluster.local", "http://orders.default.svc.cluster.local/hooks", true},
		{"http://*.svc.cluster.local", "http://svc.cluster.local/hooks", false},
		{"http://*.svc.cluster.local", "http://evilsvc.cluster.local/hooks", false},
		{"https://Orders.Internal", "https://orders.internal/", true},
	}
	for _, tt := range tests {
		p, err := ParseURLPattern(tt.pattern)
		if err != nil {
			t.Fatalf("%s: %v", tt.pattern, err)
		}
		u, _ := url.Parse(tt.url)
		if got := p.Match(u); got != tt.want {
			t.Errorf("%s matches %s = %v, want %v", tt.pattern, tt.url, got, tt.want)
		}
	}
}

func TestParseURLPatternErrors(t *testing.T) {
	for _, bad := range []string{"orders.internal", "ftp://x", "https://user:pw@x", "https://x/?a=1", "https://a.*.com"} {
		if _, err := ParseURLPattern(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestParseFileCallbacks(t *testing.T) {
	f, err := ParseFile([]byte(`
callbacks:
  allow: ["http://orders.internal/hooks/"]
  timeout: 5s
  max_attempts: 3
defaults:
  callback_url: http://orders.internal/hooks/hookyard
upstreams:
  courier-x:
    base_url: https://courier.example
  payments-y:
    base_url: https://payments.example
    callback_url: ""
`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Callbacks.Timeout != 5*time.Second || f.Callbacks.MaxAttempts != 3 || len(f.Callbacks.Allow) != 1 {
		t.Errorf("callbacks = %+v", f.Callbacks)
	}
	if u, _ := f.Upstreams.Get("courier-x"); u.CallbackURL != "http://orders.internal/hooks/hookyard" {
		t.Errorf("courier-x callback_url = %q", u.CallbackURL)
	}
	if u, _ := f.Upstreams.Get("payments-y"); u.CallbackURL != "" {
		t.Errorf("payments-y callback_url = %q, want the default turned off", u.CallbackURL)
	}
	if err := f.Callbacks.CheckCallbackURL("http://elsewhere/hooks"); err == nil {
		t.Error("a URL outside the allow-list must be rejected")
	}

	_, err = ParseFile([]byte(`
callbacks:
  allow: ["orders", "http://orders.internal"]
  max_attempts: 0
defaults:
  callback_url: ftp://x
upstreams:
  courier-x:
    base_url: https://courier.example
    callback_url: http://elsewhere/hooks
`), nil)
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"callbacks.allow[0]", "callbacks.max_attempts", "defaults.callback_url", "upstreams.courier-x.callback_url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s:\n%v", want, err)
		}
	}
}

func TestCallbackSecretsFromEnv(t *testing.T) {
	cfg, err := ServerFromEnv(env(map[string]string{"HOOKYARD_CALLBACK_SECRETS": "whsec_" + base64.StdEncoding.EncodeToString(make([]byte, 32))}))
	if err != nil || len(cfg.CallbackSecrets) != 1 {
		t.Fatalf("secrets = %v, %v", cfg.CallbackSecrets, err)
	}
	if _, err := ServerFromEnv(env(map[string]string{"HOOKYARD_CALLBACK_SECRETS": "plain"})); err == nil {
		t.Error("a secret without whsec_ must fail")
	}
}
