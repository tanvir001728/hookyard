package config

import (
	"strings"
	"testing"
)

func TestParseAPITokens(t *testing.T) {
	tokens, err := ParseAPITokens(" orders:0123456789abcdef0123 , fedcba9876543210fedc ,")
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens", len(tokens))
	}
	if tokens[0] != (APIToken{Name: "orders", Secret: "0123456789abcdef0123"}) {
		t.Errorf("named token = %+v", tokens[0])
	}
	if tokens[1].Name != "token-2" || tokens[1].Secret != "fedcba9876543210fedc" {
		t.Errorf("unnamed token = %+v", tokens[1])
	}
}

func TestParseAPITokensErrors(t *testing.T) {
	tests := map[string]string{
		"short":          "a:short",
		"bad name":       "bad name:0123456789abcdef",
		"duplicate name": "a:0123456789abcdef,a:fedcba9876543210",
		"duplicate key":  "a:0123456789abcdef,b:0123456789abcdef",
	}
	for name, in := range tests {
		if _, err := ParseAPITokens(in); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	_, err := ParseAPITokens("a:short")
	if err == nil || !strings.Contains(err.Error(), "openssl rand -hex 32") {
		t.Errorf("short token error should say how to generate one: %v", err)
	}
}
