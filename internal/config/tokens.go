package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// MinTokenLength is the minimum length of an API token secret.
const MinTokenLength = 16

// APIToken authenticates API clients. Name identifies the client in logs and
// the audit log; Secret is the bearer token.
type APIToken struct {
	Name   string
	Secret string
}

var tokenNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// ParseAPITokens parses a comma-separated list of tokens. Each entry is
// either "secret" or "name:secret". Unnamed tokens are called token-1,
// token-2, and so on.
func ParseAPITokens(s string) ([]APIToken, error) {
	var (
		tokens []APIToken
		errs   []error
	)
	names := map[string]bool{}
	secrets := map[string]bool{}
	for i, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, secret, named := strings.Cut(entry, ":")
		if !named {
			name, secret = fmt.Sprintf("token-%d", i+1), entry
		}
		switch {
		case !tokenNameRe.MatchString(name):
			errs = append(errs, fmt.Errorf("token %d: invalid name %q (letters, digits, '.', '_' and '-')", i+1, name))
		case len(secret) < MinTokenLength:
			errs = append(errs, fmt.Errorf("token %q: must be at least %d characters (generate one with: openssl rand -hex 32)", name, MinTokenLength))
		case names[name]:
			errs = append(errs, fmt.Errorf("token %q: duplicate name", name))
		case secrets[secret]:
			errs = append(errs, fmt.Errorf("token %q: duplicate secret", name))
		default:
			names[name], secrets[secret] = true, true
			tokens = append(tokens, APIToken{Name: name, Secret: secret})
		}
	}
	return tokens, errors.Join(errs...)
}
