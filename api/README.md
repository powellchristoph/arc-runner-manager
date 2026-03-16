# arc-runner-manager — API

REST API for managing GitHub Actions Runner Controller (ARC v2) scale sets on a Kubernetes cluster.

## What it does

Wraps the Helm Go SDK (`helm.sh/helm/v3`) to install, upgrade, and uninstall
`gha-runner-scale-set` Helm releases. Each runner scale set maps to:

- A Helm release named `arc-<team>`
- A Kubernetes namespace named `arc-<team>`
- A Kubernetes Secret named `arc-<team>-github-app` holding the GitHub App credentials

Releases appear natively in `helm list -A` and are fully manageable with the
Helm CLI alongside the API.

## Architecture

```
api/
  cmd/server/          entrypoint — wires dependencies, starts HTTP server
  internal/
    api/               HTTP handlers (chi router)
    helm/              Helm SDK client — install / upgrade / uninstall / list
    k8s/               k8s client — namespace + secret management, pod counts
    middleware/        Bearer token auth
    models/            Domain types (RunnerScaleSet, Status, etc.)
  pkg/config/          Environment-variable based config
```

## Authentication

All `/api/v1/*` endpoints require a Bearer token:

```
Authorization: Bearer <API_KEY>
```

`GET /healthz` is unauthenticated (liveness probe).

## API Reference

### `GET /api/v1/runners`

Returns all managed runner scale sets.

```json
{
  "items": [
    {
      "name": "team-alpha",
      "githubConfigUrl": "https://github.com/orgs/your-org",
      "runnerScaleSetName": "team-alpha",
      "minRunners": 0,
      "maxRunners": 10,
      "runnerImage": "ghcr.io/actions/actions-runner:2.317.0",
      "resources": { "cpuRequest": "500m", "cpuLimit": "2", "memoryRequest": "1Gi", "memoryLimit": "4Gi" },
      "storageClass": "standard",
      "storageSize": "1Gi",
      "status": {
        "helmStatus": "deployed",
        "chartVersion": "0.9.3",
        "namespace": "arc-team-alpha",
        "currentRunners": 2,
        "pendingRunners": 0,
        "secretExists": true
      }
    }
  ],
  "total": 1
}
```

### `POST /api/v1/runners`

Creates a new runner scale set. GitHub App credentials are **required** on create.

```json
{
  "name": "team-alpha",
  "githubConfigUrl": "https://github.com/orgs/your-org",
  "runnerScaleSetName": "team-alpha",
  "githubAppId": "123456",
  "githubAppInstallationId": "78901234",
  "githubAppPrivateKey": "-----BEGIN RSA PRIVATE KEY-----\n...",
  "minRunners": 0,
  "maxRunners": 10
}
```

Credentials are **never returned** in responses — they live only in the cluster Secret.

### `GET /api/v1/runners/{name}`

Returns a single runner scale set by team name.

### `PUT /api/v1/runners/{name}`

Updates scaling, image, or resource settings. Supply GitHub App fields only when
rotating credentials — omitted fields are carried forward from the existing release.

### `DELETE /api/v1/runners/{name}`

Uninstalls the Helm release and deletes the namespace (and its Secret).

## Configuration

All configuration is via environment variables:

| Variable | Default | Description |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `API_KEY` | **required** | Bearer token for auth |
| `ARC_CHART_REPO` | ARC GitHub registry | Helm repo URL |
| `ARC_CHART_VERSION` | `0.9.3` | Chart version to install |
| `ARC_CONTROLLER_NS` | `arc-system` | ARC controller namespace |
| `DEFAULT_MIN_RUNNERS` | `0` | Platform default |
| `DEFAULT_MAX_RUNNERS` | `10` | Platform default |
| `DEFAULT_RUNNER_IMAGE` | `ghcr.io/actions/actions-runner:2.317.0` | Platform default |
| `DEFAULT_STORAGE_CLASS` | `standard` | Platform default |
| `DEFAULT_STORAGE_SIZE` | `1Gi` | Platform default |
| `DEFAULT_CPU_REQUEST` | `500m` | Platform default |
| `DEFAULT_CPU_LIMIT` | `2` | Platform default |
| `DEFAULT_MEMORY_REQUEST` | `1Gi` | Platform default |
| `DEFAULT_MEMORY_LIMIT` | `4Gi` | Platform default |

## Local development

```bash
# Requires a valid kubeconfig context pointing at a dev cluster
cd api
go run ./cmd/server
```

Or with Docker Compose from the repo root:

```bash
API_KEY=dev-key docker compose up --build
```

## Tests

```bash
cd api
go test -race ./...
```
