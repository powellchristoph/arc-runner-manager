package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestServer builds a Server backed by the given fake backend.
// Pass empty string for staticDir to get a temp dir with minimal fixtures.
func newTestServer(t *testing.T, backendURL, apiKey, staticDir string) *Server {
	t.Helper()
	if staticDir == "" {
		staticDir = makeStaticDir(t)
	}
	srv, err := NewServer(backendURL, apiKey, staticDir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

// makeStaticDir creates a temp dir with a minimal index.html and js/app.js.
func makeStaticDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("index.html", `<!doctype html><html><body>arc-runner-manager</body></html>`)
	write("js/app.js", `console.log("arc-runner-manager");`)
	return dir
}

// fakeBackend starts a test HTTP server and cleans it up with t.Cleanup.
func fakeBackend(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	be := httptest.NewServer(handler)
	t.Cleanup(be.Close)
	return be
}

// ── Healthz ──────────────────────────────────────────────────────────────────

func TestHealthz(t *testing.T) {
	srv := newTestServer(t, "http://localhost:9999", "key", "")
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected application/json content-type, got %q", ct)
	}
}

// ── Static file serving ───────────────────────────────────────────────────────

func TestIndexServed(t *testing.T) {
	srv := newTestServer(t, "http://localhost:9999", "key", "")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "arc-runner-manager") {
		t.Errorf("expected index.html body, got %q", w.Body.String())
	}
}

func TestStaticAssetServed(t *testing.T) {
	srv := newTestServer(t, "http://localhost:9999", "key", "")
	req := httptest.NewRequest(http.MethodGet, "/static/js/app.js", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for /static/js/app.js, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "arc-runner-manager") {
		t.Errorf("expected app.js content, got %q", w.Body.String())
	}
}

func TestStaticMissingReturns404(t *testing.T) {
	srv := newTestServer(t, "http://localhost:9999", "key", "")
	req := httptest.NewRequest(http.MethodGet, "/static/does-not-exist.js", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// ── NewServer validation ──────────────────────────────────────────────────────

func TestNewServerInvalidURL(t *testing.T) {
	_, err := NewServer("://bad url", "key", t.TempDir())
	if err == nil {
		t.Fatal("expected error for invalid URL, got nil")
	}
}

// ── Proxy: auth injection ─────────────────────────────────────────────────────

func TestProxyInjectsAuthHeader(t *testing.T) {
	var got string
	be := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"items":[],"total":0}`)) //nolint:errcheck
	})

	srv := newTestServer(t, be.URL, "test-token-xyz", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runners", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got != "Bearer test-token-xyz" {
		t.Errorf("expected 'Bearer test-token-xyz', got %q", got)
	}
}

// Any Authorization header the browser sends must be overwritten by the server key.
func TestProxyOverwritesClientAuthHeader(t *testing.T) {
	var got string
	be := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`)) //nolint:errcheck
	})

	srv := newTestServer(t, be.URL, "server-key", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runners", nil)
	req.Header.Set("Authorization", "Bearer browser-supplied-token")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if got != "Bearer server-key" {
		t.Errorf("expected server key to overwrite client header, got %q", got)
	}
}

// ── Proxy: path forwarding ────────────────────────────────────────────────────

func TestProxyForwardsPathVerbatim(t *testing.T) {
	cases := []string{
		"/api/v1/runners",
		"/api/v1/runners/my-team",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			var gotPath string
			be := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{}`)) //nolint:errcheck
			})

			srv := newTestServer(t, be.URL, "key", "")
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)

			if gotPath != path {
				t.Errorf("expected backend to receive %q, got %q", path, gotPath)
			}
		})
	}
}

func TestProxyForwardsQueryString(t *testing.T) {
	var gotQuery string
	be := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`)) //nolint:errcheck
	})

	srv := newTestServer(t, be.URL, "key", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runners?page=2&limit=50", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if gotQuery != "page=2&limit=50" {
		t.Errorf("expected query string forwarded, got %q", gotQuery)
	}
}

// ── Proxy: HTTP methods ───────────────────────────────────────────────────────

func TestProxyForwardsAllMethods(t *testing.T) {
	methods := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodDelete,
	}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			var gotMethod string
			be := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{}`)) //nolint:errcheck
			})

			srv := newTestServer(t, be.URL, "key", "")
			var body io.Reader
			if method == http.MethodPost || method == http.MethodPut {
				body = strings.NewReader(`{"name":"test"}`)
			}
			req := httptest.NewRequest(method, "/api/v1/runners", body)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)

			if gotMethod != method {
				t.Errorf("expected backend to receive %s, got %s", method, gotMethod)
			}
		})
	}
}

// ── Proxy: request body passthrough ──────────────────────────────────────────

func TestProxyForwardsRequestBody(t *testing.T) {
	const payload = `{"name":"my-team","githubConfigUrl":"https://github.com/my-org","minRunners":1,"maxRunners":5}`
	var gotBody string
	be := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"name":"my-team"}`)) //nolint:errcheck
	})

	srv := newTestServer(t, be.URL, "key", "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runners", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if gotBody != payload {
		t.Errorf("request body not forwarded\ngot:  %s\nwant: %s", gotBody, payload)
	}
}

// ── Proxy: status code passthrough ───────────────────────────────────────────

func TestProxyForwardsStatusCodes(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{"ok", http.StatusOK},
		{"created", http.StatusCreated},
		{"unauthorized", http.StatusUnauthorized},
		{"not_found", http.StatusNotFound},
		{"unprocessable", http.StatusUnprocessableEntity},
		{"conflict", http.StatusConflict},
		{"server_error", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			be := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(`{}`)) //nolint:errcheck
			})

			srv := newTestServer(t, be.URL, "key", "")
			req := httptest.NewRequest(http.MethodGet, "/api/v1/runners", nil)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)

			if w.Code != tc.status {
				t.Errorf("expected %d, got %d", tc.status, w.Code)
			}
		})
	}
}

// ── Proxy: response body passthrough ─────────────────────────────────────────

func TestProxyForwardsResponseBody(t *testing.T) {
	const responseBody = `{"items":[{"name":"my-team","maxRunners":5}],"total":1}`
	be := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(responseBody)) //nolint:errcheck
	})

	srv := newTestServer(t, be.URL, "key", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runners", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Body.String() != responseBody {
		t.Errorf("response body not forwarded\ngot:  %s\nwant: %s", w.Body.String(), responseBody)
	}
}

// ── Proxy: response header forwarding ────────────────────────────────────────

func TestProxyForwardsResponseHeaders(t *testing.T) {
	be := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "abc-123")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`)) //nolint:errcheck
	})

	srv := newTestServer(t, be.URL, "key", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runners", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type not forwarded, got %q", ct)
	}
	if xrid := w.Header().Get("X-Request-Id"); xrid != "abc-123" {
		t.Errorf("X-Request-Id not forwarded, got %q", xrid)
	}
}

// ── Proxy: backend unavailable ────────────────────────────────────────────────

func TestProxyBackendUnavailableReturns502(t *testing.T) {
	// Port 1 will immediately refuse connections.
	srv := newTestServer(t, "http://127.0.0.1:1", "key", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runners", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", w.Code)
	}
}

// ── Route isolation ───────────────────────────────────────────────────────────

func TestHealthzDoesNotHitBackend(t *testing.T) {
	backendHit := false
	be := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		backendHit = true
		w.WriteHeader(http.StatusOK)
	})

	srv := newTestServer(t, be.URL, "key", "")
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if backendHit {
		t.Error("/healthz should not have proxied to backend")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestStaticDoesNotHitBackend(t *testing.T) {
	backendHit := false
	be := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		backendHit = true
		w.WriteHeader(http.StatusOK)
	})

	srv := newTestServer(t, be.URL, "key", "")
	req := httptest.NewRequest(http.MethodGet, "/static/js/app.js", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if backendHit {
		t.Error("/static/* should not have proxied to backend")
	}
}

// ── Single-key mode: /auth/me always responds; login/logout not registered ────

func TestSingleKeyMode_AuthMe_AlwaysAuthenticated(t *testing.T) {
	srv := newTestServer(t, "http://localhost:9999", "some-key", "")
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("single-key /auth/me should return JSON, got %q: %v", w.Body.String(), err)
	}
	if !body["authenticated"] {
		t.Error("expected authenticated=true in single-key mode")
	}
	if body["multiUser"] {
		t.Error("expected multiUser=false in single-key mode")
	}
}

func TestSingleKeyMode_LoginLogout_NotRegistered(t *testing.T) {
	srv := newTestServer(t, "http://localhost:9999", "some-key", "")
	// /auth/login and /auth/logout fall through to handleIndex in single-key mode.
	// They must NOT return JSON auth responses.
	for _, path := range []string{"/auth/login", "/auth/logout"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		body := w.Body.String()
		if strings.Contains(body, `"authenticated"`) {
			t.Errorf("single-key mode: %s should not return auth JSON, got %s", path, body)
		}
	}
}

// ── Multi-user mode helpers ───────────────────────────────────────────────────

// fakeValidatingBackend starts a backend that returns 200 only when the
// Authorization header matches validToken.
func fakeValidatingBackend(t *testing.T, validToken string) *httptest.Server {
	t.Helper()
	return fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+validToken {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"items":[],"total":0}`)) //nolint:errcheck
		} else {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid token"}`)) //nolint:errcheck
		}
	})
}

// sessionCookie extracts the arc_session cookie value from a recorder.
func sessionCookie(w *httptest.ResponseRecorder) string {
	for _, c := range w.Result().Cookies() {
		if c.Name == "arc_session" {
			return c.Value
		}
	}
	return ""
}

// ── /auth/me ─────────────────────────────────────────────────────────────────

func TestMultiUserMode_AuthMe_Unauthenticated(t *testing.T) {
	srv := newTestServer(t, "http://localhost:9999", "", "")
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["authenticated"] {
		t.Error("expected authenticated=false for unauthenticated request")
	}
}

func TestMultiUserMode_AuthMe_WrongMethod(t *testing.T) {
	srv := newTestServer(t, "http://localhost:9999", "", "")
	req := httptest.NewRequest(http.MethodPost, "/auth/me", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── /auth/login ──────────────────────────────────────────────────────────────

func TestMultiUserMode_Login_InvalidToken(t *testing.T) {
	be := fakeValidatingBackend(t, "good-token")
	srv := newTestServer(t, be.URL, "", "")

	body := strings.NewReader(`{"token":"bad-token"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid token, got %d", w.Code)
	}
	if sid := sessionCookie(w); sid != "" {
		t.Error("session cookie must not be set for invalid token")
	}
}

func TestMultiUserMode_Login_ValidToken(t *testing.T) {
	be := fakeValidatingBackend(t, "good-token")
	srv := newTestServer(t, be.URL, "", "")

	body := strings.NewReader(`{"token":"good-token"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp["authenticated"] {
		t.Error("expected authenticated=true after successful login")
	}
	if sid := sessionCookie(w); sid == "" {
		t.Error("expected arc_session cookie to be set after login")
	}
}

func TestMultiUserMode_Login_MissingToken(t *testing.T) {
	srv := newTestServer(t, "http://localhost:9999", "", "")
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing token, got %d", w.Code)
	}
}

func TestMultiUserMode_Login_WrongMethod(t *testing.T) {
	srv := newTestServer(t, "http://localhost:9999", "", "")
	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── /auth/me after login ──────────────────────────────────────────────────────

func TestMultiUserMode_AuthMe_AfterLogin(t *testing.T) {
	be := fakeValidatingBackend(t, "good-token")
	srv := newTestServer(t, be.URL, "", "")

	// Login to get a session cookie.
	loginBody := strings.NewReader(`{"token":"good-token"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/auth/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/json")
	loginW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(loginW, loginReq)

	sid := sessionCookie(loginW)
	if sid == "" {
		t.Fatal("login did not set session cookie")
	}

	// Now call /auth/me with the session cookie.
	meReq := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	meReq.AddCookie(&http.Cookie{Name: "arc_session", Value: sid})
	meW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(meW, meReq)

	if meW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", meW.Code)
	}
	var resp map[string]bool
	if err := json.Unmarshal(meW.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp["authenticated"] {
		t.Error("expected authenticated=true after login")
	}
}

// ── /auth/logout ─────────────────────────────────────────────────────────────

func TestMultiUserMode_Logout(t *testing.T) {
	be := fakeValidatingBackend(t, "good-token")
	srv := newTestServer(t, be.URL, "", "")

	// Login first.
	loginBody := strings.NewReader(`{"token":"good-token"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/auth/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/json")
	loginW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(loginW, loginReq)
	sid := sessionCookie(loginW)

	// Logout.
	logoutReq := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	logoutReq.AddCookie(&http.Cookie{Name: "arc_session", Value: sid})
	logoutW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(logoutW, logoutReq)
	if logoutW.Code != http.StatusOK {
		t.Fatalf("expected 200 from logout, got %d", logoutW.Code)
	}

	// /auth/me should now report unauthenticated.
	meReq := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	meReq.AddCookie(&http.Cookie{Name: "arc_session", Value: sid})
	meW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(meW, meReq)
	var resp map[string]bool
	if err := json.Unmarshal(meW.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["authenticated"] {
		t.Error("expected authenticated=false after logout")
	}
}

// ── Multi-user proxy: no session → no auth header forwarded ──────────────────

func TestMultiUserMode_Proxy_NoSession_NoAuthHeader(t *testing.T) {
	var gotAuth string
	be := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"items":[],"total":0}`)) //nolint:errcheck
	})

	srv := newTestServer(t, be.URL, "", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runners", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if gotAuth != "" {
		t.Errorf("expected no Authorization header for unauthenticated proxy, got %q", gotAuth)
	}
}

// Multi-user proxy: valid session → token injected.
func TestMultiUserMode_Proxy_WithSession_InjectsToken(t *testing.T) {
	var gotAuth string
	be := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"items":[],"total":0}`)) //nolint:errcheck
	})

	srv := newTestServer(t, be.URL, "", "")

	// Manually insert a session.
	sid := "test-session-id"
	srv.sessions.Store(sid, "my-api-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runners", nil)
	req.AddCookie(&http.Cookie{Name: "arc_session", Value: sid})
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if gotAuth != "Bearer my-api-token" {
		t.Errorf("expected 'Bearer my-api-token', got %q", gotAuth)
	}
}

// Session cookie must never be forwarded to the backend.
func TestMultiUserMode_Proxy_SessionCookieNotForwarded(t *testing.T) {
	var gotCookie string
	be := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`)) //nolint:errcheck
	})

	srv := newTestServer(t, be.URL, "", "")
	sid := "test-session-id"
	srv.sessions.Store(sid, "my-api-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runners", nil)
	req.AddCookie(&http.Cookie{Name: "arc_session", Value: sid})
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if gotCookie != "" {
		t.Errorf("session cookie must not be forwarded to backend, got Cookie: %q", gotCookie)
	}
}
