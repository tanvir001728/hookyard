package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/config"
)

// signIn exchanges the test token for a session cookie.
func (a *testAPI) signIn() *http.Cookie {
	a.t.Helper()
	rec := a.raw(http.MethodPost, "/ui/session", `{"token":"`+testToken+`"}`, nil)
	if rec.Code != http.StatusOK {
		a.t.Fatalf("sign in: %d %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	a.t.Fatal("no session cookie set")
	return nil
}

// raw sends a request without the bearer token, optionally with a cookie.
func (a *testAPI) raw(method, path, body string, cookie *http.Cookie, headers ...string) *httptest.ResponseRecorder {
	a.t.Helper()
	req := httptest.NewRequestWithContext(a.t.Context(), method, path, strings.NewReader(body))
	if cookie != nil {
		req.AddCookie(cookie)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	a.srv.ServeHTTP(rec, req)
	return rec
}

func TestSessionSignIn(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)

	if rec := a.raw(http.MethodPost, "/ui/session", `{"token":"wrong-token-0000000"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad token: %d", rec.Code)
	}

	// The whole "name:secret" entry from HOOKYARD_API_TOKENS works too, but
	// only with the right name.
	if rec := a.raw(http.MethodPost, "/ui/session", `{"token":"orders:`+testToken+`"}`, nil); rec.Code != http.StatusOK {
		t.Errorf("name:secret form: %d", rec.Code)
	}
	if rec := a.raw(http.MethodPost, "/ui/session", `{"token":"billing:`+testToken+`"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong name with a valid secret: %d", rec.Code)
	}

	c := a.signIn()
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge <= 0 || strings.Contains(c.Value, testToken) {
		t.Errorf("unsafe cookie: %+v", c)
	}

	rec := a.raw(http.MethodGet, "/ui/session", "", c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"actor":"orders"`) {
		t.Errorf("get session: %d %s", rec.Code, rec.Body)
	}
	if rec := a.raw(http.MethodGet, "/ui/session", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no cookie: %d", rec.Code)
	}
}

func TestSessionSecureBehindTLSProxy(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	rec := a.raw(http.MethodPost, "/ui/session", `{"token":"`+testToken+`"}`, nil, "X-Forwarded-Proto", "https")
	if c := rec.Result().Cookies(); len(c) != 1 || !c[0].Secure {
		t.Errorf("cookie must be Secure behind an HTTPS proxy: %+v", c)
	}
}

func TestSessionAuthorizesAPI(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	c := a.signIn()

	if rec := a.raw(http.MethodGet, "/v1/requests", "", c); rec.Code != http.StatusOK {
		t.Errorf("GET with session: %d", rec.Code)
	}

	body := `{"upstream":"courier-x","method":"GET","path":"/"}`
	if rec := a.raw(http.MethodPost, "/v1/requests", body, c); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), csrfHeader) {
		t.Errorf("POST with session but no CSRF header: %d %s", rec.Code, rec.Body)
	}
	if rec := a.raw(http.MethodPost, "/v1/requests", body, c, csrfHeader, "1"); rec.Code != http.StatusAccepted {
		t.Errorf("POST with session and CSRF header: %d %s", rec.Code, rec.Body)
	}
}

func TestSessionRejectsTamperedAndExpired(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	c := a.signIn()

	tampered := *c
	payload, sig, _ := strings.Cut(c.Value, ".")
	tampered.Value = payload + "x." + sig
	if rec := a.raw(http.MethodGet, "/v1/requests", "", &tampered); rec.Code != http.StatusUnauthorized {
		t.Errorf("tampered cookie: %d", rec.Code)
	}

	if _, ok := a.srv.verifySession(c.Value, time.Now().Add(sessionLifetime+time.Minute)); ok {
		t.Error("expired session must be rejected")
	}
	if _, ok := a.srv.verifySession("garbage", time.Now()); ok {
		t.Error("garbage must be rejected")
	}
}

func TestSessionInvalidatedByTokenRotation(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	c := a.signIn()

	rotated := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithV1(V1{
		Store: a.srv.v1.Store, Config: a.srv.v1.Config, MaxBody: 4096,
		Tokens: []config.APIToken{{Name: "orders", Secret: "a-brand-new-token-0001"}},
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/requests", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	rotated.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("session signed with old tokens: %d, want 401", rec.Code)
	}
}

func TestSignOut(t *testing.T) {
	t.Parallel()
	a := newTestAPI(t)
	rec := a.raw(http.MethodDelete, "/ui/session", "", a.signIn())
	c := rec.Result().Cookies()
	if rec.Code != http.StatusNoContent || len(c) != 1 || c[0].MaxAge >= 0 {
		t.Errorf("sign out: %d %+v", rec.Code, c)
	}
}

func TestDashboardRouting(t *testing.T) {
	t.Parallel()
	file, _ := config.ParseFile([]byte(testConfig), nil)
	dash := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "dashboard") })
	srv := New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithV1(V1{Config: file, Tokens: []config.APIToken{{Name: "a", Secret: testToken}}, MaxBody: 1024}),
		WithDashboard(dash))

	for path, want := range map[string]string{
		"/":               "dashboard",
		"/requests/abc":   "dashboard",
		"/healthz":        `{"status":"ok"}`,
		"/v1/nope":        `"code":"not_found"`,
		"/ui/nope":        `"code":"not_found"`,
		"/v1/requests/xx": `"code":"unauthorized"`,
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: got %q, want %q", path, rec.Body.String(), want)
		}
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/requests", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST to a dashboard path: %d, want 405", rec.Code)
	}
}
