package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	helmchart "helm.sh/helm/v3/pkg/chart"
	helmrelease "helm.sh/helm/v3/pkg/release"

	"github.com/powellchristoph/arc-runner-manager/internal/api"
	"github.com/powellchristoph/arc-runner-manager/internal/models"
	"github.com/powellchristoph/arc-runner-manager/pkg/config"
)

// ── fakes ─────────────────────────────────────────────────────────────────────

type fakeHelm struct {
	releases []*helmrelease.Release
	err      error
}

func (f *fakeHelm) List(_ context.Context) ([]*helmrelease.Release, error) {
	return f.releases, f.err
}
func (f *fakeHelm) Get(_ context.Context, team string) (*helmrelease.Release, error) {
	for _, r := range f.releases {
		if r.Name == "arc-"+team {
			return r, nil
		}
	}
	return nil, fmt.Errorf("release: not found")
}
func (f *fakeHelm) Install(_ context.Context, rss *models.RunnerScaleSet) (*helmrelease.Release, error) {
	rel := minRelease(rss.Name)
	f.releases = append(f.releases, rel)
	return rel, f.err
}
func (f *fakeHelm) Upgrade(_ context.Context, rss *models.RunnerScaleSet) (*helmrelease.Release, error) {
	return minRelease(rss.Name), f.err
}
func (f *fakeHelm) UpgradeChart(_ context.Context, team string) error {
	return f.err
}
func (f *fakeHelm) Uninstall(_ context.Context, team string) error {
	for _, r := range f.releases {
		if r.Name == "arc-"+team {
			return f.err
		}
	}
	return fmt.Errorf("release: not found")
}

type fakeK8s struct {
	secretExists bool
	err          error
}

func (f *fakeK8s) EnsureNamespace(_ context.Context, _ string) error { return f.err }
func (f *fakeK8s) DeleteNamespace(_ context.Context, _ string) error { return f.err }
func (f *fakeK8s) UpsertGitHubAppSecret(_ context.Context, _, _, _, _, _ string) error {
	return f.err
}
func (f *fakeK8s) DeleteSecret(_ context.Context, _, _ string) error { return f.err }
func (f *fakeK8s) SecretExists(_ context.Context, _, _ string) (bool, error) {
	return f.secretExists, f.err
}
func (f *fakeK8s) RunnerPodCounts(_ context.Context, _ string) (int, int, error) {
	return 0, 0, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func minRelease(team string) *helmrelease.Release {
	return &helmrelease.Release{
		Name: "arc-" + team,
		Config: map[string]interface{}{
			"githubConfigUrl":    "https://github.com/orgs/test-org",
			"runnerScaleSetName": team,
			"minRunners":         float64(0),
			"maxRunners":         float64(10),
		},
		Info: &helmrelease.Info{Status: helmrelease.StatusDeployed},
		Chart: &helmchart.Chart{
			Metadata: &helmchart.Metadata{Version: "0.9.3"},
		},
	}
}

func testHandler(h *fakeHelm, k *fakeK8s) (*api.Handler, *chi.Mux) {
	cfg := &config.Config{
		DefaultMaxRunners:    10,
		DefaultRunnerImage:   "ghcr.io/actions/actions-runner:2.317.0",
		DefaultStorageClass:  "standard",
		DefaultStorageSize:   "1Gi",
		DefaultCPURequest:    "500m",
		DefaultCPULimit:      "2",
		DefaultMemoryRequest: "1Gi",
		DefaultMemoryLimit:   "4Gi",
	}
	handler := api.NewHandlerWithClients(cfg, h, k, nopLogger())
	r := chi.NewRouter()
	handler.RegisterRoutes(r)
	return handler, r
}

func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestListRunners_Empty(t *testing.T) {
	_, r := testHandler(&fakeHelm{}, &fakeK8s{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runners", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp models.ListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 0 {
		t.Errorf("expected 0 items, got %d", resp.Total)
	}
}

func TestListRunners_WithItems(t *testing.T) {
	h := &fakeHelm{releases: []*helmrelease.Release{minRelease("team-alpha"), minRelease("team-beta")}}
	_, r := testHandler(h, &fakeK8s{secretExists: true})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runners", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp models.ListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("expected 2 items, got %d", resp.Total)
	}
}

func TestCreateRunner_MissingFields(t *testing.T) {
	_, r := testHandler(&fakeHelm{}, &fakeK8s{})

	body := `{"name":"team-alpha","githubConfigUrl":"https://github.com/orgs/test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runners", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body)
	}
}

func TestCreateRunner_MissingName(t *testing.T) {
	_, r := testHandler(&fakeHelm{}, &fakeK8s{})

	body := `{"githubConfigUrl":"https://github.com/orgs/test","githubAppId":"1","githubAppInstallationId":"2","githubAppPrivateKey":"key"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runners", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body)
	}
}

func TestCreateRunner_Success(t *testing.T) {
	h := &fakeHelm{}
	_, r := testHandler(h, &fakeK8s{secretExists: true})

	body := `{
		"name": "team-alpha",
		"githubConfigUrl": "https://github.com/orgs/test-org",
		"githubAppId": "12345",
		"githubAppInstallationId": "67890",
		"githubAppPrivateKey": "-----BEGIN RSA PRIVATE KEY-----\nfake\n-----END RSA PRIVATE KEY-----"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runners", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body)
	}

	var rss models.RunnerScaleSet
	if err := json.Unmarshal(w.Body.Bytes(), &rss); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rss.Name != "team-alpha" {
		t.Errorf("expected name=team-alpha, got %s", rss.Name)
	}
	if rss.GitHubAppID != "" || rss.GitHubAppPrivateKey != "" {
		t.Error("credentials must not be returned in response")
	}
}

func TestGetRunner_NotFound(t *testing.T) {
	_, r := testHandler(&fakeHelm{}, &fakeK8s{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runners/nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestGetRunner_Found(t *testing.T) {
	h := &fakeHelm{releases: []*helmrelease.Release{minRelease("team-alpha")}}
	_, r := testHandler(h, &fakeK8s{secretExists: true})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runners/team-alpha", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body)
	}
	var rss models.RunnerScaleSet
	if err := json.Unmarshal(w.Body.Bytes(), &rss); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rss.Name != "team-alpha" {
		t.Errorf("expected team-alpha, got %s", rss.Name)
	}
}

func TestDeleteRunner_Success(t *testing.T) {
	h := &fakeHelm{releases: []*helmrelease.Release{minRelease("team-alpha")}}
	_, r := testHandler(h, &fakeK8s{})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/runners/team-alpha", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body)
	}
}

func TestDeleteRunner_NotFound(t *testing.T) {
	_, r := testHandler(&fakeHelm{}, &fakeK8s{})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/runners/ghost", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestUpdateRunner_RotatesCredentials(t *testing.T) {
	h := &fakeHelm{releases: []*helmrelease.Release{minRelease("team-alpha")}}
	_, r := testHandler(h, &fakeK8s{})

	body := `{"githubAppId":"new-id","githubAppInstallationId":"new-install","githubAppPrivateKey":"new-key"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/runners/team-alpha", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body)
	}
}
