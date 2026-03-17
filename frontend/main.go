package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

// Server holds the configuration for the frontend server.
// Extracted so tests can construct it directly without env vars or os.Exit.
type Server struct {
	backendURL *url.URL
	apiKey     string       // non-empty in single-key mode; empty in multi-user mode
	staticDir  string
	httpClient *http.Client
	sessions   *sync.Map    // sessionID(string) → token(string); nil in single-key mode
}

// NewServer constructs a Server, validating the backendURL.
// When apiKey is empty the server runs in multi-user mode: sessions are
// managed via httponly cookies and /auth/* endpoints are registered.
func NewServer(backendURL, apiKey, staticDir string) (*Server, error) {
	u, err := url.Parse(backendURL)
	if err != nil {
		return nil, fmt.Errorf("invalid backend URL %q: %w", backendURL, err)
	}
	s := &Server{
		backendURL: u,
		apiKey:     apiKey,
		staticDir:  staticDir,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
	if apiKey == "" {
		s.sessions = &sync.Map{}
	}
	return s, nil
}

// Handler builds and returns the HTTP mux for the server.
// Kept separate from ListenAndServe so tests can call ServeHTTP directly.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/api/", s.handleProxy)

	// /auth/me is always registered so the JS can detect the operating mode.
	// /auth/login and /auth/logout are only registered in multi-user mode.
	mux.HandleFunc("/auth/me", s.handleMe)
	if s.sessions != nil {
		mux.HandleFunc("/auth/login",  s.handleLogin)
		mux.HandleFunc("/auth/logout", s.handleLogout)
	}

	// Static assets: /static/app.js etc.
	fs := http.FileServer(http.Dir(s.staticDir))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))

	// SPA root — serve index.html for all unmatched routes.
	mux.HandleFunc("/", s.handleIndex)

	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, s.staticDir+"/index.html")
}

// handleProxy forwards /api/* to the backend, injecting the API key.
// Path is forwarded verbatim; the backend owns its own versioning (/api/v1/...).
func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	target := *s.backendURL // copy
	target.Path = r.URL.Path
	target.RawQuery = r.URL.RawQuery

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		http.Error(w, "proxy error", http.StatusBadGateway)
		return
	}

	// Forward request headers, then set auth.
	proxyReq.Header = r.Header.Clone()
	proxyReq.Header.Del("Cookie") // never forward session cookies to the backend

	token := s.resolveToken(r)
	if token != "" {
		proxyReq.Header.Set("Authorization", "Bearer "+token)
	} else {
		proxyReq.Header.Del("Authorization")
	}
	proxyReq.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(proxyReq)
	if err != nil {
		http.Error(w, "backend unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Forward all response headers.
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}

// resolveToken returns the Bearer token to inject for this request.
// In single-key mode it always returns the configured API key.
// In multi-user mode it reads the session cookie and returns the associated token.
func (s *Server) resolveToken(r *http.Request) string {
	if s.apiKey != "" {
		return s.apiKey
	}
	if s.sessions == nil {
		return ""
	}
	c, err := r.Cookie("arc_session")
	if err != nil {
		return ""
	}
	if tok, ok := s.sessions.Load(c.Value); ok {
		return tok.(string)
	}
	return ""
}

// handleLogin validates a token against the backend and, if valid, stores it
// in the session store and sets an httponly session cookie.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"token required"}`)) //nolint:errcheck
		return
	}
	if !s.validateTokenAgainstBackend(r, body.Token) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid token"}`)) //nolint:errcheck
		return
	}
	sid := generateSessionID()
	s.sessions.Store(sid, body.Token)
	http.SetCookie(w, &http.Cookie{
		Name:     "arc_session",
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"authenticated":true}`)) //nolint:errcheck
}

// handleLogout clears the session cookie and removes the session from the store.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c, err := r.Cookie("arc_session"); err == nil {
		s.sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "arc_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"authenticated":false}`)) //nolint:errcheck
}

// handleMe is always registered and reports the operating mode plus auth state.
//
//   - Single-key mode (API_KEY set):  {"authenticated":true,"multiUser":false}
//   - Multi-user mode (API_KEY unset): {"authenticated":bool,"multiUser":true}
//
// The JS uses multiUser to decide whether to render the login/logout UI.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if s.sessions == nil {
		// Single-key mode: always authenticated, no login UI needed.
		w.Write([]byte(`{"authenticated":true,"multiUser":false}`)) //nolint:errcheck
		return
	}
	authenticated := false
	if c, err := r.Cookie("arc_session"); err == nil {
		if _, ok := s.sessions.Load(c.Value); ok {
			authenticated = true
		}
	}
	if authenticated {
		w.Write([]byte(`{"authenticated":true,"multiUser":true}`)) //nolint:errcheck
	} else {
		w.Write([]byte(`{"authenticated":false,"multiUser":true}`)) //nolint:errcheck
	}
}

// validateTokenAgainstBackend makes a GET to /api/v1/runners with the given
// token and returns true if the backend responds with 200 OK.
func (s *Server) validateTokenAgainstBackend(r *http.Request, token string) bool {
	target := *s.backendURL
	target.Path = "/api/v1/runners"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// generateSessionID returns a cryptographically random, URL-safe session ID.
func generateSessionID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	listenAddr := getEnv("LISTEN_ADDR", ":3000")
	apiURL := mustEnv("API_URL")
	apiKey := getEnv("API_KEY", "") // optional: empty enables multi-user mode
	staticDir := getEnv("STATIC_DIR", "static")

	srv, err := NewServer(apiURL, apiKey, staticDir)
	if err != nil {
		logger.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	if apiKey == "" {
		logger.Info("running in multi-user mode (API_KEY not set)")
	}

	logger.Info("frontend server starting", "addr", listenAddr, "backend", apiURL)
	httpSrv := &http.Server{
		Addr:         listenAddr,
		Handler:      srv.Handler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		logger.Error("server error", "err", err)
		os.Exit(1)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "FATAL: %s is required\n", key)
		os.Exit(1)
	}
	return v
}
