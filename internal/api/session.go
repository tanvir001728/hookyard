package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/tanvir001728/hookyard/internal/config"
)

// Dashboard sessions: a signed cookie that stands in for an API token, so the
// browser never stores the token itself.
const (
	sessionCookie   = "hookyard_session"
	sessionLifetime = 12 * time.Hour
	// csrfHeader must accompany state-changing requests authenticated by the
	// session cookie. Browsers can't send custom headers cross-site without a
	// CORS preflight, which Hookyard never allows.
	csrfHeader = "Hookyard-Csrf"
)

type sessionClaims struct {
	Actor   string `json:"a"`
	Expires int64  `json:"e"`
}

// sessionKey is derived from the configured API tokens: it needs no extra
// setting, is identical across instances with the same config, and rotating
// the tokens invalidates every session.
func sessionKey(secrets []string) []byte {
	sorted := slices.Clone(secrets)
	slices.Sort(sorted)
	h := sha256.New()
	h.Write([]byte("hookyard-session-v1"))
	for _, s := range sorted {
		h.Write([]byte{0})
		h.Write([]byte(s))
	}
	return h.Sum(nil)
}

func (s *Server) signSession(c sessionClaims) string {
	payload, _ := json.Marshal(c)
	enc := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.sessionKey)
	mac.Write([]byte(enc))
	return enc + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifySession returns the actor of a valid, unexpired session cookie value.
func (s *Server) verifySession(value string, now time.Time) (string, bool) {
	enc, sig, ok := strings.Cut(value, ".")
	if !ok {
		return "", false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, s.sessionKey)
	mac.Write([]byte(enc))
	if !hmac.Equal(got, mac.Sum(nil)) {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", false
	}
	var c sessionClaims
	if err := json.Unmarshal(payload, &c); err != nil || now.Unix() >= c.Expires {
		return "", false
	}
	// The token the session was created with must still be configured.
	if !slices.ContainsFunc(s.v1.Tokens, func(t config.APIToken) bool { return t.Name == c.Actor }) {
		return "", false
	}
	return c.Actor, true
}

// handleCreateSession exchanges an API token for a session cookie.
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}
	actor, ok := s.matchToken(in.Token)
	if !ok {
		// People often paste the whole "name:secret" entry from
		// HOOKYARD_API_TOKENS; accept it when the name matches too.
		if name, secret, found := strings.Cut(strings.TrimSpace(in.Token), ":"); found {
			if a, matched := s.matchToken(secret); matched && a == name {
				actor, ok = a, true
			}
		}
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "invalid API token")
		return
	}
	expires := time.Now().Add(sessionLifetime)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.signSession(sessionClaims{Actor: actor, Expires: expires.Unix()}),
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(sessionLifetime.Seconds()),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
	s.log.Info("dashboard sign-in", "actor", actor)
	writeJSON(w, http.StatusOK, map[string]any{"actor": actor, "expires_at": expires.UTC()})
}

// handleGetSession reports who is signed in.
func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"actor": actorFrom(r.Context())})
}

// handleDeleteSession signs out by expiring the cookie.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// matchToken checks a token secret in constant time and returns its name.
func (s *Server) matchToken(secret string) (string, bool) {
	sum := sha256.Sum256([]byte(strings.TrimSpace(secret)))
	actor := ""
	for _, t := range s.hashedTokens {
		if subtle.ConstantTimeCompare(sum[:], t.sum[:]) == 1 {
			actor = t.name
		}
	}
	return actor, actor != ""
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
