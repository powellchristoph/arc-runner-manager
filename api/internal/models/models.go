package models

// RunnerScaleSet is the canonical representation of a managed ARC runner scale set.
// It maps 1:1 with a Helm release of gha-runner-scale-set in the cluster.
type RunnerScaleSet struct {
	// Name is the team identifier — becomes the Helm release name, the namespace
	// suffix (arc-<name>), and the runnerScaleSetName label in GitHub Actions.
	Name string `json:"name"`

	// GitHubConfigURL is the org or repo URL the runners register against.
	// e.g. "https://github.com/orgs/your-org" or "https://kaseya.com/repo"
	GitHubConfigURL string `json:"githubConfigUrl"`

	// RunnerScaleSetName is the label used in runs-on: in workflows.
	// Defaults to Name if not set.
	RunnerScaleSetName string `json:"runnerScaleSetName,omitempty"`

	// Scaling
	MinRunners int `json:"minRunners"`
	MaxRunners int `json:"maxRunners"`

	// Runner container
	RunnerImage string    `json:"runnerImage,omitempty"`
	Resources   Resources `json:"resources,omitempty"`

	// Storage (kubernetes container mode work volume)
	StorageClass string `json:"storageClass,omitempty"`
	StorageSize  string `json:"storageSize,omitempty"`

	// GitHub App credentials — write-only on create/update, never returned on GET.
	// Stored as a Kubernetes Secret in the team's namespace.
	GitHubAppID             string `json:"githubAppId,omitempty"`
	GitHubAppInstallationID string `json:"githubAppInstallationId,omitempty"`
	GitHubAppPrivateKey     string `json:"githubAppPrivateKey,omitempty"`

	// Status — populated on GET responses, ignored on write.
	Status *RunnerStatus `json:"status,omitempty"`
}

// RunnerStatus reflects live state read from the cluster.
type RunnerStatus struct {
	HelmStatus     string `json:"helmStatus"` // deployed, failed, pending-install, etc.
	ChartVersion   string `json:"chartVersion"`
	Namespace      string `json:"namespace"`
	CurrentRunners int    `json:"currentRunners"`
	PendingRunners int    `json:"pendingRunners"`
	SecretExists   bool   `json:"secretExists"`
}

// Resources mirrors the Kubernetes resource request/limit structure.
type Resources struct {
	CPURequest    string `json:"cpuRequest,omitempty"`
	CPULimit      string `json:"cpuLimit,omitempty"`
	MemoryRequest string `json:"memoryRequest,omitempty"`
	MemoryLimit   string `json:"memoryLimit,omitempty"`
}

// CreateRequest is the request body for POST /api/v1/runners.
// GitHubApp fields are required on create.
type CreateRequest struct {
	RunnerScaleSet
}

// UpdateRequest is the request body for PUT /api/v1/runners/{name}.
// All fields are optional — only non-zero values are applied.
// Supply GitHubApp fields only when rotating credentials.
type UpdateRequest struct {
	RunnerScaleSet
}

// ErrorResponse is the standard error envelope.
type ErrorResponse struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

// ListResponse wraps a list of scale sets for consistent envelope shape.
type ListResponse struct {
	Items []RunnerScaleSet `json:"items"`
	Total int              `json:"total"`
}
