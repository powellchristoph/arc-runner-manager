package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Server holds the configuration for the frontend server.
// Extracted so tests can construct it directly without env vars or os.Exit.
type Server struct {
	backendURL *url.URL
	apiKey     string
	staticDir  string
	httpClient *http.Client
}

// NewServer constructs a Server, validating the backendURL.
func NewServer(backendURL, apiKey, staticDir string) (*Server, error) {
	u, err := url.Parse(backendURL)
	if err != nil {
		return nil, fmt.Errorf("invalid backend URL %q: %w", backendURL, err)
	}
	return &Server{
		backendURL: u,
		apiKey:     apiKey,
		staticDir:  staticDir,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// Handler builds and returns the HTTP mux for the server.
// Kept separate from ListenAndServe so tests can call ServeHTTP directly.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/api/", s.handleProxy)

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
	proxyReq.Header.Set("Authorization", "Bearer "+s.apiKey)
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

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	listenAddr := getEnv("LISTEN_ADDR", ":3000")
	apiURL := mustEnv("API_URL")
	apiKey := mustEnv("API_KEY")
	staticDir := getEnv("STATIC_DIR", "static")

	srv, err := NewServer(apiURL, apiKey, staticDir)
	if err != nil {
		logger.Error("invalid configuration", "err", err)
		os.Exit(1)
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
