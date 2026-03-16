package helm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/release"
	"k8s.io/client-go/rest"

	"github.com/powellchristoph/arc-runner-managerinternal/models"
	"github.com/powellchristoph/arc-runner-managerpkg/config"
)

const (
	// releasePrefix is prepended to the team name for the Helm release name.
	// Matches the arc-platform convention: arc-<team>.
	releasePrefix = "arc-"
	// namespacePrefix mirrors the release prefix for namespace naming.
	namespacePrefix = "arc-"
	// managedLabel is applied to every release so we can filter on list.
	managedByLabel = "app.kubernetes.io/managed-by=arc-runner-manager"
)

// Client wraps the Helm SDK action configuration and exposes higher-level
// operations scoped to ARC runner scale sets.
type Client struct {
	cfg    *config.Config
	k8sCfg *rest.Config
	logger *slog.Logger
}

func NewClient(cfg *config.Config, k8sCfg *rest.Config, logger *slog.Logger) *Client {
	return &Client{cfg: cfg, k8sCfg: k8sCfg, logger: logger}
}

// actionConfig builds a Helm action.Configuration for the given namespace.
func (c *Client) actionConfig(namespace string) (*action.Configuration, error) {
	env := cli.New()
	env.SetNamespace(namespace)

	ac := new(action.Configuration)
	restClientGetter := newRESTClientGetter(namespace, c.k8sCfg)
	if err := ac.Init(restClientGetter, namespace, os.Getenv("HELM_DRIVER"), func(format string, v ...interface{}) {
		c.logger.Debug(fmt.Sprintf(format, v...))
	}); err != nil {
		return nil, fmt.Errorf("helm action config init: %w", err)
	}
	_ = env
	return ac, nil
}

// releaseName returns the canonical Helm release name for a team.
func releaseName(team string) string { return releasePrefix + team }

// namespace returns the target namespace for a team.
func namespace(team string) string { return namespacePrefix + team }

// List returns all ARC runner scale set releases managed by this application.
func (c *Client) List(ctx context.Context) ([]*release.Release, error) {
	// Use the default namespace for listing; Helm stores release metadata in
	// the release's own namespace as secrets by default.
	// We iterate by querying all namespaces via the k8s client instead —
	// helm list --all-namespaces equivalent requires looping over namespaces.
	// We achieve this by listing with no namespace filter and matching our label.
	ac, err := c.actionConfig("")
	if err != nil {
		return nil, err
	}

	lister := action.NewList(ac)
	lister.AllNamespaces = true
	lister.All = true
	lister.Filter = releasePrefix
	lister.Selector = managedByLabel // only releases we created

	releases, err := lister.Run()
	if err != nil {
		return nil, fmt.Errorf("helm list: %w", err)
	}
	return releases, nil
}

// Get returns the Helm release for a specific team.
func (c *Client) Get(ctx context.Context, team string) (*release.Release, error) {
	ac, err := c.actionConfig(namespace(team))
	if err != nil {
		return nil, err
	}

	getter := action.NewGet(ac)
	rel, err := getter.Run(releaseName(team))
	if err != nil {
		return nil, fmt.Errorf("helm get %s: %w", releaseName(team), err)
	}
	return rel, nil
}

// Install deploys a new ARC runner scale set via Helm.
func (c *Client) Install(ctx context.Context, rss *models.RunnerScaleSet) (*release.Release, error) {
	ns := namespace(rss.Name)
	ac, err := c.actionConfig(ns)
	if err != nil {
		return nil, err
	}

	chart, err := c.loadChart(ctx)
	if err != nil {
		return nil, err
	}

	vals := c.buildValues(rss, false)

	// If a previous install attempt left a failed release, upgrade it rather
	// than failing with "cannot re-use a name that is still in use".
	getter := action.NewGet(ac)
	if existing, err := getter.Run(releaseName(rss.Name)); err == nil {
		if existing.Info.Status == release.StatusFailed {
			c.logger.Warn("found failed release, upgrading instead of installing", "release", releaseName(rss.Name))
			return c.upgradeWithChart(ctx, rss, chart, ac, vals)
		}
		return nil, fmt.Errorf("helm install %s: release already exists with status %s", releaseName(rss.Name), existing.Info.Status)
	}

	installer := action.NewInstall(ac)
	installer.ReleaseName = releaseName(rss.Name)
	installer.Namespace = ns
	installer.CreateNamespace = true
	installer.Wait = false
	installer.Labels = map[string]string{"app.kubernetes.io/managed-by": "arc-runner-manager"}

	c.logger.Info("installing helm release", "release", installer.ReleaseName, "namespace", ns)
	rel, err := installer.RunWithContext(ctx, chart, vals)
	if err != nil {
		return nil, fmt.Errorf("helm install %s: %w", installer.ReleaseName, err)
	}
	return rel, nil
}

// Upgrade updates an existing ARC runner scale set.
func (c *Client) Upgrade(ctx context.Context, rss *models.RunnerScaleSet) (*release.Release, error) {
	ns := namespace(rss.Name)
	ac, err := c.actionConfig(ns)
	if err != nil {
		return nil, err
	}

	chart, err := c.loadChart(ctx)
	if err != nil {
		return nil, err
	}

	vals := c.buildValues(rss, true)
	return c.upgradeWithChart(ctx, rss, chart, ac, vals)
}

// upgradeWithChart runs helm upgrade using a pre-loaded chart and action config.
// Merges new values on top of the existing release values so required chart fields
// (like githubConfigUrl) are always present even when not supplied in the request.
func (c *Client) upgradeWithChart(ctx context.Context, rss *models.RunnerScaleSet, ch *chart.Chart, ac *action.Configuration, newVals map[string]interface{}) (*release.Release, error) {
	ns := namespace(rss.Name)

	// Fetch current release values and merge new values on top.
	// This ensures required chart fields are always present regardless of
	// what fields the caller supplied in the update request.
	getter := action.NewGet(ac)
	existing, err := getter.Run(releaseName(rss.Name))
	merged := map[string]interface{}{}
	if err == nil && existing.Config != nil {
		for k, v := range existing.Config {
			merged[k] = v
		}
	}
	for k, v := range newVals {
		// Skip empty strings — let the existing value win.
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		merged[k] = v
	}

	upgrader := action.NewUpgrade(ac)
	upgrader.Namespace = ns
	upgrader.Wait = false
	upgrader.ReuseValues = false // we handle merging ourselves above

	c.logger.Info("upgrading helm release", "release", releaseName(rss.Name), "namespace", ns)
	rel, err := upgrader.RunWithContext(ctx, releaseName(rss.Name), ch, merged)
	if err != nil {
		return nil, fmt.Errorf("helm upgrade %s: %w", releaseName(rss.Name), err)
	}
	return rel, nil
}

// Uninstall removes the Helm release for a team.
func (c *Client) Uninstall(ctx context.Context, team string) error {
	ns := namespace(team)
	ac, err := c.actionConfig(ns)
	if err != nil {
		return err
	}

	uninstaller := action.NewUninstall(ac)
	uninstaller.Wait = false

	c.logger.Info("uninstalling helm release", "release", releaseName(team), "namespace", ns)
	_, err = uninstaller.Run(releaseName(team))
	if err != nil {
		return fmt.Errorf("helm uninstall %s: %w", releaseName(team), err)
	}
	return nil
}

// loadChart fetches the ARC runner scale set chart.
// Supports both OCI references (oci://...) and HTTP repo URLs.
// The chart is cached in /tmp/helm-charts after first pull.
func (c *Client) loadChart(ctx context.Context) (*chart.Chart, error) {
	cacheDir := "/tmp/helm-charts"
	chartPath := fmt.Sprintf("%s/%s", cacheDir, c.cfg.ARCChartName)

	// Return cached chart if present — avoids a registry round-trip on every install.
	if ch, err := loader.Load(chartPath); err == nil {
		c.logger.Debug("using cached chart", "path", chartPath)
		return ch, nil
	}

	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return nil, fmt.Errorf("create chart cache dir: %w", err)
	}

	settings := cli.New()

	pull := action.NewPullWithOpts(action.WithConfig(&action.Configuration{}))
	pull.Settings = settings
	pull.Version = c.cfg.ARCChartVersion
	pull.DestDir = cacheDir
	pull.Untar = true
	pull.UntarDir = cacheDir

	// OCI and HTTP repos are referenced differently.
	// OCI:  chartRef = full oci:// URI, RepoURL stays empty.
	// HTTP: chartRef = chart name, RepoURL = repo base URL.
	var chartRef string
	if strings.HasPrefix(c.cfg.ARCChartRepo, "oci://") {
		chartRef = c.cfg.ARCChartRepo + "/" + c.cfg.ARCChartName
	} else {
		pull.RepoURL = c.cfg.ARCChartRepo
		chartRef = c.cfg.ARCChartName
	}

	c.logger.Info("pulling helm chart", "ref", chartRef, "version", c.cfg.ARCChartVersion)
	if _, err := pull.Run(chartRef); err != nil {
		return nil, fmt.Errorf("helm pull %s: %w", chartRef, err)
	}

	ch, err := loader.Load(chartPath)
	if err != nil {
		return nil, fmt.Errorf("load chart from %s: %w", chartPath, err)
	}
	return ch, nil
}

// buildValues constructs the Helm values map from a RunnerScaleSet.
// On upgrade with isUpgrade=true, zero/empty values are omitted so that
// ReuseValues carries them forward from the previous release.
func (c *Client) buildValues(rss *models.RunnerScaleSet, isUpgrade bool) map[string]interface{} {
	cfg := c.cfg

	scaleSetName := rss.RunnerScaleSetName
	if scaleSetName == "" {
		scaleSetName = rss.Name
	}

	minRunners := rss.MinRunners
	maxRunners := rss.MaxRunners
	if !isUpgrade {
		if maxRunners == 0 {
			maxRunners = cfg.DefaultMaxRunners
		}
	}

	image := rss.RunnerImage
	if image == "" {
		image = cfg.DefaultRunnerImage
	}

	storageClass := rss.StorageClass
	if storageClass == "" {
		storageClass = cfg.DefaultStorageClass
	}
	storageSize := rss.StorageSize
	if storageSize == "" {
		storageSize = cfg.DefaultStorageSize
	}

	cpuReq := rss.Resources.CPURequest
	if cpuReq == "" {
		cpuReq = cfg.DefaultCPURequest
	}
	cpuLim := rss.Resources.CPULimit
	if cpuLim == "" {
		cpuLim = cfg.DefaultCPULimit
	}
	memReq := rss.Resources.MemoryRequest
	if memReq == "" {
		memReq = cfg.DefaultMemoryRequest
	}
	memLim := rss.Resources.MemoryLimit
	if memLim == "" {
		memLim = cfg.DefaultMemoryLimit
	}

	vals := map[string]interface{}{
		"githubConfigUrl":    rss.GitHubConfigURL,
		"runnerScaleSetName": scaleSetName,
		"minRunners":         minRunners,
		"maxRunners":         maxRunners,
		"containerMode": map[string]interface{}{
			"type": "kubernetes",
			"kubernetesModeWorkVolumeClaim": map[string]interface{}{
				"accessModes":      []string{"ReadWriteOnce"},
				"storageClassName": storageClass,
				"resources": map[string]interface{}{
					"requests": map[string]interface{}{
						"storage": storageSize,
					},
				},
			},
		},
		"template": map[string]interface{}{
			"spec": map[string]interface{}{
				"containers": []interface{}{
					map[string]interface{}{
						"name":  "runner",
						"image": image,
						"resources": map[string]interface{}{
							"requests": map[string]interface{}{
								"cpu":    cpuReq,
								"memory": memReq,
							},
							"limits": map[string]interface{}{
								"cpu":    cpuLim,
								"memory": memLim,
							},
						},
					},
				},
			},
		},
		// Reference the secret created in the team namespace by the k8s client.
		"githubConfigSecret": secretName(rss.Name),
	}

	return vals
}

// SecretName returns the Kubernetes secret name for a team's GitHub App credentials.
func SecretName(team string) string { return "arc-" + team + "-github-app" }

func secretName(team string) string { return SecretName(team) }

// TeamFromRelease extracts the team name from a Helm release name.
func TeamFromRelease(relName string) string {
	return strings.TrimPrefix(relName, releasePrefix)
}

// Namespace returns the namespace for a team (exported for use by k8s client).
func Namespace(team string) string { return namespace(team) }
