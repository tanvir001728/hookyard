package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

type actorKey struct{}

// actorFrom returns the name of the API token that authenticated the request.
func actorFrom(ctx context.Context) string {
	if a, ok := ctx.Value(actorKey{}).(string); ok {
		return a
	}
	return "unknown"
}

// requireToken rejects requests without a valid bearer token.
func (s *Server) requireToken(next http.Handler) http.Handler {
	type hashed struct {
		name string
		sum  [32]byte
	}
	tokens := make([]hashed, len(s.v1.Tokens))
	for i, t := range s.v1.Tokens {
		tokens[i] = hashed{name: t.Name, sum: sha256.Sum256([]byte(t.Secret))}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, secret, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || secret == "" {
			unauthorized(w)
			return
		}
		// Compare fixed-size hashes in constant time and check every token,
		// so timing reveals neither the secret nor which token matched.
		sum := sha256.Sum256([]byte(strings.TrimSpace(secret)))
		actor := ""
		for _, t := range tokens {
			if subtle.ConstantTimeCompare(sum[:], t.sum[:]) == 1 {
				actor = t.name
			}
		}
		if actor == "" {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, actor)))
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="hookyard"`)
	writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid bearer token")
}
