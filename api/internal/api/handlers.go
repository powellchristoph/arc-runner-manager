package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	helmrelease "helm.sh/helm/v3/pkg/release"

	helmclient "github.com/powellchristoph/arc-runner-manager/internal/helm"
	"github.com/powellchristoph/arc-runner-manager/internal/models"
	"github.com/powellchristoph/arc-runner-manager/pkg/config"
)

// HelmClient is the interface the Handler uses to interact with Helm.
// The real implementation is internal/helm.Client; tests supply a fake.
type HelmClient interface {
	List(ctx context.Context) ([]*helmrelease.Release, error)
	Get(ctx context.Context, team string) (*helmrelease.Release, error)
	Install(ctx context.Context, rss *models.RunnerScaleSet) (*helmrelease.Release, error)
	Upgrade(ctx context.Context, rss *models.RunnerScaleSet) (*helmrelease.Release, error)
	UpgradeChart(ctx context.Context, team string) error
	Uninstall(ctx context.Context, team string) error
}

// K8sClient is the interface the Handler uses for Kubernetes operations.
type K8sClient interface {
	EnsureNamespace(ctx context.Context, ns string) error
	DeleteNamespace(ctx context.Context, ns string) error
	UpsertGitHubAppSecret(ctx context.Context, namespace, secretName, appID, installationID, privateKey string) error
	DeleteSecret(ctx context.Context, namespace, secretName string) error
	SecretExists(ctx context.Context, namespace, secretName string) (bool, error)
	RunnerPodCounts(ctx context.Context, namespace string) (running, pending int, err error)
}

// Handler holds dependencies for the API endpoints.
type Handler struct {
	cfg    *config.Config
	helm   HelmClient
	k8s    K8sClient
	logger *slog.Logger
}

// NewHandler constructs a Handler with the real Helm and k8s clients.
func NewHandler(cfg *config.Config, helm HelmClient, k8s K8sClient, logger *slog.Logger) *Handler {
	return &Handler{cfg: cfg, helm: helm, k8s: k8s, logger: logger}
}

// NewHandlerWithClients is an alias used by tests to inject fakes.
var NewHandlerWithClients = NewHandler

// RegisterRoutes mounts all API v1 routes onto the given router.
// Do NOT register /healthz here — it belongs outside the auth group.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/v1/runners", func(r chi.Router) {
		r.Get("/", h.ListRunners)
		r.Post("/", h.CreateRunner)
		r.Post("/upgrade-chart", h.UpgradeAllRunners)
		r.Get("/{name}", h.GetRunner)
		r.Put("/{name}", h.UpdateRunner)
		r.Delete("/{name}", h.DeleteRunner)
	})
}

// Healthz is a simple liveness probe endpoint.
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetDefaults returns the server-configured runner defaults so the UI can
// pre-populate the create form with the operator's chosen values.
func (h *Handler) GetDefaults(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"minRunners":      h.cfg.DefaultMinRunners,
		"maxRunners":      h.cfg.DefaultMaxRunners,
		"runnerImage":     h.cfg.DefaultRunnerImage,
		"storageClass":    h.cfg.DefaultStorageClass,
		"storageSize":     h.cfg.DefaultStorageSize,
		"cpuRequest":      h.cfg.DefaultCPURequest,
		"cpuLimit":        h.cfg.DefaultCPULimit,
		"memoryRequest":   h.cfg.DefaultMemoryRequest,
		"memoryLimit":     h.cfg.DefaultMemoryLimit,
		"arcChartVersion": h.cfg.ARCChartVersion,
	})
}

// UpgradeAllRunners upgrades every managed runner scale set to the currently
// configured ARC chart version, preserving each runner's existing values.
// Runners already at the target version are skipped.
//
// POST /api/v1/runners/upgrade-chart
func (h *Handler) UpgradeAllRunners(w http.ResponseWriter, r *http.Request) {
	releases, err := h.helm.List(r.Context())
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to list releases", err)
		return
	}

	var upgraded, skipped, failed []string
	for _, rel := range releases {
		team := helmclient.TeamFromRelease(rel.Name)
		if rel.Chart.Metadata.Version == h.cfg.ARCChartVersion {
			skipped = append(skipped, team)
			continue
		}
		if err := h.helm.UpgradeChart(r.Context(), team); err != nil {
			h.logger.Error("chart upgrade failed", "team", team, "err", err)
			failed = append(failed, team)
		} else {
			upgraded = append(upgraded, team)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"upgraded": upgraded,
		"skipped":  skipped,
		"failed":   failed,
	})
}

// ListRunners returns all ARC runner scale sets managed by this application.
//
// GET /api/v1/runners
func (h *Handler) ListRunners(w http.ResponseWriter, r *http.Request) {
	releases, err := h.helm.List(r.Context())
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to list releases", err)
		return
	}

	items := make([]models.RunnerScaleSet, 0, len(releases))
	for _, rel := range releases {
		rss := h.releaseToModel(r.Context(), rel)
		items = append(items, rss)
	}

	writeJSON(w, http.StatusOK, models.ListResponse{Items: items, Total: len(items)})
}

// GetRunner returns a single runner scale set by team name.
//
// GET /api/v1/runners/{name}
func (h *Handler) GetRunner(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	rel, err := h.helm.Get(r.Context(), name)
	if err != nil {
		if isNotFound(err) {
			h.writeError(w, http.StatusNotFound, "runner not found", nil)
			return
		}
		h.writeError(w, http.StatusInternalServerError, "failed to get release", err)
		return
	}

	writeJSON(w, http.StatusOK, h.releaseToModel(r.Context(), rel))
}

// CreateRunner provisions a new runner scale set.
//
// POST /api/v1/runners
// Body: models.CreateRequest (githubAppId, githubAppInstallationId, githubAppPrivateKey required)
func (h *Handler) CreateRunner(w http.ResponseWriter, r *http.Request) {
	var req models.CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body", err)
		return
	}

	if err := validateCreate(&req.RunnerScaleSet); err != nil {
		h.writeError(w, http.StatusUnprocessableEntity, err.Error(), nil)
		return
	}

	ctx := r.Context()
	ns := helmclient.Namespace(req.Name)

	// 1. Ensure the namespace exists before writing the secret.
	if err := h.k8s.EnsureNamespace(ctx, ns); err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to ensure namespace", err)
		return
	}

	// 2. Write GitHub App credentials to a Kubernetes Secret.
	secretName := helmclient.SecretName(req.Name)
	if err := h.k8s.UpsertGitHubAppSecret(
		ctx, ns, secretName,
		req.GitHubAppID, req.GitHubAppInstallationID, req.GitHubAppPrivateKey,
	); err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to write github app secret", err)
		return
	}

	// 3. Install the Helm chart.
	rel, err := h.helm.Install(ctx, &req.RunnerScaleSet)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "helm install failed", err)
		return
	}

	writeJSON(w, http.StatusCreated, h.releaseToModel(ctx, rel))
}

// UpdateRunner updates an existing runner scale set.
//
// PUT /api/v1/runners/{name}
// Body: models.UpdateRequest (all fields optional; supply GitHubApp fields to rotate creds)
func (h *Handler) UpdateRunner(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	var req models.UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body", err)
		return
	}
	req.Name = name // name is authoritative from the URL

	ctx := r.Context()
	ns := helmclient.Namespace(name)

	// Optionally rotate credentials if any GitHub App field was supplied.
	if req.GitHubAppID != "" || req.GitHubAppInstallationID != "" || req.GitHubAppPrivateKey != "" {
		secretName := helmclient.SecretName(name)
		if err := h.k8s.UpsertGitHubAppSecret(
			ctx, ns, secretName,
			req.GitHubAppID, req.GitHubAppInstallationID, req.GitHubAppPrivateKey,
		); err != nil {
			h.writeError(w, http.StatusInternalServerError, "failed to update github app secret", err)
			return
		}
	}

	rel, err := h.helm.Upgrade(ctx, &req.RunnerScaleSet)
	if err != nil {
		if isNotFound(err) {
			h.writeError(w, http.StatusNotFound, "runner not found", nil)
			return
		}
		h.writeError(w, http.StatusInternalServerError, "helm upgrade failed", err)
		return
	}

	writeJSON(w, http.StatusOK, h.releaseToModel(ctx, rel))
}

// DeleteRunner uninstalls the Helm release and removes the namespace.
//
// DELETE /api/v1/runners/{name}
func (h *Handler) DeleteRunner(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	ctx := r.Context()

	if err := h.helm.Uninstall(ctx, name); err != nil {
		if isNotFound(err) {
			h.writeError(w, http.StatusNotFound, "runner not found", nil)
			return
		}
		h.writeError(w, http.StatusInternalServerError, "helm uninstall failed", err)
		return
	}

	// Delete the namespace (this also removes the GitHub App secret).
	// Use a background context so request cancellation or the HTTP timeout
	// does not abort the cleanup after the Helm uninstall has succeeded.
	ns := helmclient.Namespace(name)
	if err := h.k8s.DeleteNamespace(context.Background(), ns); err != nil {
		h.logger.Error("failed to delete namespace after uninstall", "namespace", ns, "err", err)
	} else {
		h.logger.Info("namespace deletion initiated", "namespace", ns)
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (h *Handler) releaseToModel(ctx context.Context, rel *helmrelease.Release) models.RunnerScaleSet {
	team := helmclient.TeamFromRelease(rel.Name)
	ns := helmclient.Namespace(team)

	running, pending, _ := h.k8s.RunnerPodCounts(ctx, ns)
	secretExists, _ := h.k8s.SecretExists(ctx, ns, helmclient.SecretName(team))

	rss := models.RunnerScaleSet{
		Name: team,
		Status: &models.RunnerStatus{
			HelmStatus:     rel.Info.Status.String(),
			ChartVersion:   rel.Chart.Metadata.Version,
			Namespace:      ns,
			CurrentRunners: running,
			PendingRunners: pending,
			SecretExists:   secretExists,
		},
	}

	// Extract values from the release for display. Helm stores the merged
	// values in rel.Config (user-supplied) and rel.Chart.Values (chart defaults).
	vals := rel.Config

	if v, ok := vals["githubConfigUrl"].(string); ok {
		rss.GitHubConfigURL = v
	}
	if v, ok := vals["runnerScaleSetName"].(string); ok {
		rss.RunnerScaleSetName = v
	}
	if v, ok := vals["minRunners"].(float64); ok {
		rss.MinRunners = int(v)
	}
	if v, ok := vals["maxRunners"].(float64); ok {
		rss.MaxRunners = int(v)
	}

	if cm, ok := vals["containerMode"].(map[string]interface{}); ok {
		if wv, ok := cm["kubernetesModeWorkVolumeClaim"].(map[string]interface{}); ok {
			if sc, ok := wv["storageClassName"].(string); ok {
				rss.StorageClass = sc
			}
			if res, ok := wv["resources"].(map[string]interface{}); ok {
				if req, ok := res["requests"].(map[string]interface{}); ok {
					if s, ok := req["storage"].(string); ok {
						rss.StorageSize = s
					}
				}
			}
		}
	}

	if tmpl, ok := vals["template"].(map[string]interface{}); ok {
		if spec, ok := tmpl["spec"].(map[string]interface{}); ok {
			if containers, ok := spec["containers"].([]interface{}); ok && len(containers) > 0 {
				if c, ok := containers[0].(map[string]interface{}); ok {
					if img, ok := c["image"].(string); ok {
						rss.RunnerImage = img
					}
					if res, ok := c["resources"].(map[string]interface{}); ok {
						if req, ok := res["requests"].(map[string]interface{}); ok {
							rss.Resources.CPURequest, _ = req["cpu"].(string)
							rss.Resources.MemoryRequest, _ = req["memory"].(string)
						}
						if lim, ok := res["limits"].(map[string]interface{}); ok {
							rss.Resources.CPULimit, _ = lim["cpu"].(string)
							rss.Resources.MemoryLimit, _ = lim["memory"].(string)
						}
					}
				}
			}
		}
	}

	return rss
}

func validateCreate(rss *models.RunnerScaleSet) error {
	var missing []string
	if rss.Name == "" {
		missing = append(missing, "name")
	}
	if rss.GitHubConfigURL == "" {
		missing = append(missing, "githubConfigUrl")
	}
	if rss.GitHubAppID == "" {
		missing = append(missing, "githubAppId")
	}
	if rss.GitHubAppInstallationID == "" {
		missing = append(missing, "githubAppInstallationId")
	}
	if rss.GitHubAppPrivateKey == "" {
		missing = append(missing, "githubAppPrivateKey")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not found") || strings.Contains(msg, "no release") || strings.Contains(msg, "has no deployed releases")
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func (h *Handler) writeError(w http.ResponseWriter, status int, msg string, err error) {
	resp := models.ErrorResponse{Error: msg}
	if err != nil {
		resp.Details = err.Error()
		h.logger.Error(msg, "err", err)
	}
	writeJSON(w, status, resp)
}
