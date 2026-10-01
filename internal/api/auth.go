package api

import (
	"context"
	"net/http"
	"strings"
	"time"
)

type actorKey struct{}

// actorFrom returns the name of the API token that authenticated the request.
func actorFrom(ctx context.Context) string {
	if a, ok := ctx.Value(actorKey{}).(string); ok {
		return a
	}
	return "unknown"
}

type hashedToken struct {
	name string
	sum  [32]byte
}

// requireToken accepts either an API token (Authorization: Bearer) or a
// dashboard session cookie. Cookie-authenticated requests that change state
// must also send the CSRF header.
func (s *Server) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if header := r.Header.Get("Authorization"); header != "" {
			scheme, secret, ok := strings.Cut(header, " ")
			if !ok || !strings.EqualFold(scheme, "Bearer") || secret == "" {
				unauthorized(w)
				return
			}
			actor, ok := s.matchToken(secret)
			if !ok {
				unauthorized(w)
				return
			}
			next.ServeHTTP(w, withActor(r, actor))
			return
		}

		if c, err := r.Cookie(sessionCookie); err == nil {
			actor, ok := s.verifySession(c.Value, time.Now())
			if !ok {
				unauthorized(w)
				return
			}
			if !isSafeMethod(r.Method) && r.Header.Get(csrfHeader) == "" {
				writeError(w, http.StatusForbidden, codeUnauthorized, "missing "+csrfHeader+" header")
				return
			}
			next.ServeHTTP(w, withActor(r, actor))
			return
		}

		unauthorized(w)
	})
}

func withActor(r *http.Request, actor string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), actorKey{}, actor))
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="hookyard"`)
	writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid bearer token")
}
