# arc-runner-manager

Self-hosted provisioning API and web UI for managing GitHub Actions Runner Controller
(ARC v2) runner scale sets on Kubernetes.

## Overview

```
┌─────────────────────────────────────────────────────────────────┐
│  Browser                                                        │
│  Bootstrap 5 UI  ──/api/──►  Frontend (Go proxy)               │
│                                    │                            │
│                              Authorization: Bearer <key>        │
│                                    │                            │
│                             API Server (Go)                     │
│                           ┌────────┴────────┐                   │
│                      Helm Go SDK        k8s client              │
│                           │                 │                   │
│                    Helm releases       Namespaces               │
│                    (arc-<team>)        Secrets (GitHub App)     │
│                                        Pods (status)            │
└─────────────────────────────────────────────────────────────────┘
```

Each team runner scale set maps to:
- Helm release: `arc-<team>` (visible in `helm list -A`)
- Namespace: `arc-<team>`
- Secret: `arc-<team>-github-app` (GitHub App credentials, write-only via API)

## Repository structure

```
arc-runner-manager/
  api/                          Go REST API (Helm SDK + k8s client)
    cmd/server/main.go
    internal/
      api/handlers.go           HTTP handlers
      helm/client.go            Helm install/upgrade/uninstall/list
      k8s/client.go             Namespace, secret, pod management
      middleware/auth.go        Bearer token middleware
      models/models.go          Domain types
    pkg/config/config.go
    Dockerfile
    README.md

  frontend/                     Go HTTP server + Bootstrap 5 UI
    main.go                     Static file server + API proxy
    static/
      index.html
      js/app.js
    Dockerfile
    README.md

  charts/
    arc-runner-manager/         Helm chart deploying both services
      Chart.yaml
      values.yaml
      templates/
        _helpers.tpl
        rbac.yaml               ServiceAccount + ClusterRole + ClusterRoleBinding
        api.yaml                API Deployment + Service + Secret
        frontend.yaml           Frontend Deployment + Service + (optional) Ingress

  .github/workflows/ci.yaml     CI: build, test, helm lint, publish to GHCR
  docker-compose.yaml           Local dev
  skaffold.yaml                 Local k8s dev (Skaffold)
```

## Deployment

### Prerequisites

- ARC controller already installed in `arc-system` namespace
  (`gha-runner-scale-set-controller` chart)
- Container images built and pushed to your registry

### Install

```bash
helm install arc-runner-manager charts/arc-runner-manager \
  --namespace arc-runner-manager \
  --create-namespace \
  --set api.apiKey="$(openssl rand -hex 32)" \
  --set frontend.ingress.enabled=true \
  --set frontend.ingress.host=arc-runners.internal.example.com \
  --set api.defaults.storageClass=ceph-block
```

### Supply the API key from an existing secret

```bash
kubectl create secret generic arc-manager-api-key \
  --namespace arc-runner-manager \
  --from-literal=api-key="$(openssl rand -hex 32)"

helm install arc-runner-manager charts/arc-runner-manager \
  --namespace arc-runner-manager \
  --create-namespace \
  --set api.existingSecret=arc-manager-api-key
```

### Values reference

See [`charts/arc-runner-manager/values.yaml`](charts/arc-runner-manager/values.yaml)
for the full annotated values file.

Key values to override for your cluster:

```yaml
api:
  apiKey: ""                         # or use existingSecret
  arc:
    chartVersion: "0.9.3"            # pin to your tested ARC version
    controllerNamespace: "arc-system"
  defaults:
    storageClass: "ceph-block"       # your cluster's RWO storage class
    runnerImage: "ghcr.io/actions/actions-runner:2.317.0"

frontend:
  ingress:
    enabled: true
    className: "nginx"
    host: "arc-runners.internal.example.com"
```

## Local development

### With Docker Compose (no cluster required for UI dev)

```bash
cp .env.example .env   # set API_KEY and KUBECONFIG
docker compose up --build
# API:      http://localhost:8080
# Frontend: http://localhost:3000
```

### With Skaffold (full in-cluster dev)

```bash
# Requires a local cluster (k3d, kind, etc.)
skaffold dev
# Port-forwarded at http://localhost:3000
```

## Creating a runner — end to end

1. Create a GitHub App scoped to your org or target repos. Note the App ID and
   Installation ID. Export the private key as a PEM file.

2. Open the web UI and click **New Runner**.

3. Fill in the form:
   - **Team Name**: unique identifier, e.g. `team-alpha`
   - **GitHub Config URL**: `https://github.com/orgs/your-org`
   - **Runner Scale Set Label**: label used in `runs-on:` — typically same as team name
   - **GitHub App credentials**: paste App ID, Installation ID, and PEM key

4. Click **Save**. The API will:
   - Create namespace `arc-team-alpha`
   - Write credentials to Secret `arc-team-alpha/arc-team-alpha-github-app`
   - Install the `gha-runner-scale-set` Helm release `arc-team-alpha`

5. Add `runs-on: team-alpha` to a workflow. The first queued job will spin up
   an ephemeral runner pod.

## RBAC

The API service account requires broad cluster permissions because it manages
namespaces and secrets across the cluster and installs Helm releases (which store
state as Secrets in the release namespace). Review `charts/arc-runner-manager/templates/rbac.yaml`
and tighten if your environment has stricter policy requirements.

## CI

GitHub Actions workflow in `.github/workflows/ci.yaml`:

- `go test -race` for API and frontend on every PR
- `helm lint` + `helm template` render validation
- On merge to `main`: build and push images to GHCR

## Upgrading ARC chart version

The Helm Go SDK will call `helm upgrade` when you update `api.arc.chartVersion`
and restart the API pod. Note that ARC does **not** support in-place upgrades
across CRD version boundaries — follow the ARC release notes for any version
that changes CRDs (typically requires uninstall/reinstall of all scale sets).
