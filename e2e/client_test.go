// Package e2e contains functional tests that run against a live
// arc-runner-manager API instance. The API must be running before
// executing these tests.
//
// Required environment variables:
//
//	API_URL   base URL of the API (default: http://localhost:8080)
//	API_KEY   bearer token (default: dev-key-change-me)
//	STORAGE_CLASS  k8s storage class for runner PVCs (default: hostpath)
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"testing"
	"time"
)

// client is a thin wrapper around net/http for the arc-runner-manager API.
type client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func newClient(t *testing.T) *client {
	t.Helper()
	return &client{
		baseURL: getEnv("API_URL", "http://localhost:8080"),
		apiKey:  getEnv("API_KEY", "dev-key-change-me"),
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

type response struct {
	StatusCode int
	Body       []byte
}

func (r *response) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode response body: %v\nbody: %s", err, r.Body)
	}
}

func (r *response) assertStatus(t *testing.T, want int) {
	t.Helper()
	if r.StatusCode != want {
		t.Fatalf("expected HTTP %d, got %d\nbody: %s", want, r.StatusCode, r.Body)
	}
}

func (c *client) do(t *testing.T, method, path string, body any) *response {
	t.Helper()

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		t.Fatalf("execute request %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	return &response{StatusCode: resp.StatusCode, Body: respBody}
}

func (c *client) get(t *testing.T, path string) *response {
	return c.do(t, http.MethodGet, path, nil)
}

func (c *client) post(t *testing.T, path string, body any) *response {
	return c.do(t, http.MethodPost, path, body)
}

func (c *client) put(t *testing.T, path string, body any) *response {
	return c.do(t, http.MethodPut, path, body)
}

func (c *client) delete(t *testing.T, path string) *response {
	return c.do(t, http.MethodDelete, path, nil)
}

// doWithKey sends a request with an explicit API key (used for auth tests).
func (c *client) doWithKey(t *testing.T, method, path, key string) *response {
	t.Helper()
	req, err := http.NewRequest(method, c.baseURL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		t.Fatalf("execute request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return &response{StatusCode: resp.StatusCode, Body: body}
}

// teamName generates a unique team name for a test run.
// Includes a random suffix so re-runs don't collide with still-terminating
// namespaces from the previous run.
func teamName(t *testing.T) string {
	t.Helper()
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	suffix := make([]byte, 5)
	for i := range suffix {
		suffix[i] = chars[rand.Intn(len(chars))]
	}
	return "e2e-" + string(suffix)
}

// runnerPayload builds a minimal valid create request for a team.
func runnerPayload(team string, extra map[string]any) map[string]any {
	payload := map[string]any{
		"name":                    team,
		"githubConfigUrl":         "https://github.com/orgs/test-org",
		"githubAppId":             "123",
		"githubAppInstallationId": "456",
		"githubAppPrivateKey":     "fake-key-functional-test",
		"minRunners":              0,
		"maxRunners":              2,
		"storageClass":            getEnv("STORAGE_CLASS", "hostpath"),
	}
	for k, v := range extra {
		payload[k] = v
	}
	return payload
}

// cleanupRunner registers a cleanup func that deletes the named runner
// after the test completes, regardless of pass/fail.
func cleanupRunner(t *testing.T, c *client, team string) {
	t.Helper()
	t.Cleanup(func() {
		resp := c.delete(t, "/api/v1/runners/"+team)
		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
			t.Logf("cleanup: unexpected status deleting %s: %d", team, resp.StatusCode)
		}
	})
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// readonlyMode reports whether the API is running with ALLOW_READONLY_UNAUTHENTICATED=true.
// Set this env var in the test environment to match the API's configuration.
func readonlyMode() bool {
	v := os.Getenv("ALLOW_READONLY_UNAUTHENTICATED")
	return v == "true" || v == "1"
}

// checkAPIAvailable fails the test immediately if the API is not reachable.
// All tests call this so a missing API is a clear error, not a silent skip.
func checkAPIAvailable(t *testing.T, c *client) {
	t.Helper()
	resp, err := c.httpClient.Get(c.baseURL + "/healthz")
	if err != nil {
		t.Fatalf("API not available at %s — start with 'task run:api' first: %v", c.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("API healthz returned HTTP %d — check that API_KEY env var is set and API is running", resp.StatusCode)
	}
}

// mustJSON is a test helper that pretty-prints a value for failure messages.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("mustJSON: %v", err)
	}
	return string(b)
}

// assertField checks a decoded map field.
func assertField(t *testing.T, label string, got map[string]any, key string, want any) {
	t.Helper()
	v, ok := got[key]
	if !ok {
		t.Errorf("%s: field %q missing from response", label, key)
		return
	}
	wantStr := fmt.Sprintf("%v", want)
	gotStr := fmt.Sprintf("%v", v)
	if gotStr != wantStr {
		t.Errorf("%s: field %q = %q, want %q", label, key, gotStr, wantStr)
	}
}
