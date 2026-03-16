package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/powellchristoph/arc-runner-managerinternal/middleware"
)

// ── TokenStore unit tests ─────────────────────────────────────────────────────

func TestNewTokenStore_Valid(t *testing.T) {
	store := middleware.NewTokenStore(map[string]string{
		"frontend": "tok-abc",
		"ci":       "tok-def",
	})
	if store == nil {
		t.Fatal("expected non-nil store")
	}
}

func TestNewTokenStore_EmptyValuePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for empty token value")
		}
	}()
	middleware.NewTokenStore(map[string]string{"bad": ""})
}

func TestTokenStore_Validate(t *testing.T) {
	store := middleware.NewTokenStore(map[string]string{
		"frontend":  "tok-fe-abc",
		"ci-system": "tok-ci-def",
		"admin":     "tok-adm-xyz",
	})

	cases := []struct {
		token    string
		wantName string
	}{
		{"tok-fe-abc", "frontend"},
		{"tok-ci-def", "ci-system"},
		{"tok-adm-xyz", "admin"},
		{"wrong", ""},
		{"", ""},
		{"tok-fe-abc-extra", ""},
	}

	for _, tc := range cases {
		got := store.Validate(tc.token)
		if got != tc.wantName {
			t.Errorf("Validate(%q) = %q, want %q", tc.token, got, tc.wantName)
		}
	}
}

func TestTokenStore_NoDuplicateValues(t *testing.T) {
	// Two tokens with the same value — second one wins in the reverse map.
	// This is a configuration error but shouldn't panic; just verify behaviour.
	store := middleware.NewTokenStore(map[string]string{
		"a": "shared-token",
		"b": "shared-token",
	})
	name := store.Validate("shared-token")
	if name == "" {
		t.Error("expected a valid name for shared token")
	}
}

// ── BearerAuth middleware tests ───────────────────────────────────────────────

func makeStore() *middleware.TokenStore {
	return middleware.NewTokenStore(map[string]string{
		"frontend":  "tok-fe-abc",
		"ci-system": "tok-ci-def",
	})
}

func okHandler(w http.ResponseWriter, r *http.Request) {
	name := middleware.TokenNameFromContext(r.Context())
	w.Header().Set("X-Token-Name", name)
	w.WriteHeader(http.StatusOK)
}

func applyMiddleware(store *middleware.TokenStore) http.Handler {
	return middleware.BearerAuth(store)(http.HandlerFunc(okHandler))
}

func TestBearerAuth_MissingHeader(t *testing.T) {
	h := applyMiddleware(makeStore())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestBearerAuth_WrongScheme(t *testing.T) {
	h := applyMiddleware(makeStore())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic tok-fe-abc")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestBearerAuth_InvalidToken(t *testing.T) {
	h := applyMiddleware(makeStore())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestBearerAuth_ValidToken_Frontend(t *testing.T) {
	h := applyMiddleware(makeStore())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer tok-fe-abc")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("X-Token-Name"); got != "frontend" {
		t.Errorf("token name in context = %q, want %q", got, "frontend")
	}
}

func TestBearerAuth_ValidToken_CI(t *testing.T) {
	h := applyMiddleware(makeStore())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer tok-ci-def")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("X-Token-Name"); got != "ci-system" {
		t.Errorf("token name in context = %q, want %q", got, "ci-system")
	}
}

func TestBearerAuth_WWWAuthenticateHeader(t *testing.T) {
	h := applyMiddleware(makeStore())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)
	want := `Bearer realm="arc-runner-manager"`
	if got := rec.Header().Get("WWW-Authenticate"); got != want {
		t.Errorf("WWW-Authenticate = %q, want %q", got, want)
	}
}

func TestBearerAuth_EmptyBearerValue(t *testing.T) {
	h := applyMiddleware(makeStore())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer ")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for empty bearer value, got %d", rec.Code)
	}
}

// ── TokenNameFromContext ──────────────────────────────────────────────────────

func TestTokenNameFromContext_Missing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	name := middleware.TokenNameFromContext(req.Context())
	if name != "unknown" {
		t.Errorf("expected %q, got %q", "unknown", name)
	}
}
