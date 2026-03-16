package middleware

import (
	"context"
	"net/http"
	"strings"
)

// contextKey is an unexported type for context keys in this package.
type contextKey string

const tokenNameKey contextKey = "tokenName"

// TokenStore holds the set of valid API tokens keyed by a human-readable name.
// Names are used for logging and are never returned to clients.
type TokenStore struct {
	// tokenByValue maps raw token string → token name for O(1) lookup.
	tokenByValue map[string]string
}

// NewTokenStore builds a TokenStore from a name→token map.
// Panics if any token value is empty.
func NewTokenStore(tokens map[string]string) *TokenStore {
	m := make(map[string]string, len(tokens))
	for name, tok := range tokens {
		if tok == "" {
			panic("arc-runner-manager: token for " + name + " must not be empty")
		}
		m[tok] = name
	}
	return &TokenStore{tokenByValue: m}
}

// Validate returns the token name if the raw token is valid, or "" if not.
func (ts *TokenStore) Validate(token string) string {
	return ts.tokenByValue[token]
}

// BearerAuth returns middleware that validates Bearer tokens against the store.
// On success, the token name is stored in the request context and is available
// via TokenNameFromContext for use in audit logging.
func BearerAuth(store *TokenStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				writeUnauthorized(w, "missing Authorization header")
				return
			}

			const prefix = "Bearer "
			if !strings.HasPrefix(authHeader, prefix) {
				writeUnauthorized(w, "Authorization header must use Bearer scheme")
				return
			}

			token := strings.TrimPrefix(authHeader, prefix)
			name := store.Validate(token)
			if name == "" {
				writeUnauthorized(w, "invalid API key")
				return
			}

			ctx := context.WithValue(r.Context(), tokenNameKey, name)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// TokenNameFromContext returns the authenticated token name from the context,
// or "unknown" if not present. Use in structured log fields for audit trails.
func TokenNameFromContext(ctx context.Context) string {
	if name, ok := ctx.Value(tokenNameKey).(string); ok {
		return name
	}
	return "unknown"
}

func writeUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="arc-runner-manager"`)
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"error":"` + msg + `"}`)) //nolint:errcheck
}
