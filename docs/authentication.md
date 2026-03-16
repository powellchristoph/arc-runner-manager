# Authentication

ARC Runner Manager uses Bearer token authentication for all `/api/v1/*` endpoints.
`/healthz` is unauthenticated (liveness probe use).

## Token format

```
Authorization: Bearer <token-value>
```

## Issuing tokens

Tokens are named — every token has a human-readable name used in audit logs.
Issue separate tokens to separate systems (CI pipelines, the frontend, admin
tooling) so you can revoke individual access without rotating everything.

Token values are a JSON object mapping name → value:

```json
{
  "frontend":  "tok-fe-abc123def456",
  "ci-system": "tok-ci-xyz789uvw012",
  "admin":     "tok-adm-mno345pqr678"
}
```

Generate strong token values:

```bash
openssl rand -hex 32
```

## Configuration

### Option A — Kubernetes Secret (recommended for production)

Create the secret before deploying:

```bash
kubectl create secret generic arc-runner-manager-tokens \
  --namespace arc-runner-manager \
  --from-literal=api-tokens='{"frontend":"tok-fe-...","ci-system":"tok-ci-...","admin":"tok-adm-..."}'
```

Reference it in `values.yaml`:

```yaml
api:
  existingSecret: "arc-runner-manager-tokens"
  existingSecretKey: "api-tokens"   # default, can omit
```

The chart will not create its own Secret when `existingSecret` is set. Use
[External Secrets Operator](https://external-secrets.io) or Vault Agent to
populate the secret from your secrets manager.

### Option B — Chart-managed Secret (dev/staging)

```bash
helm upgrade arc-runner-manager ./charts/arc-runner-manager \
  --set api.apiTokens='{"frontend":"tok-fe-...","ci-system":"tok-ci-..."}'
```

Or in `values.yaml`:

```yaml
api:
  apiTokens: '{"frontend":"tok-fe-...","ci-system":"tok-ci-..."}'
```

### Option C — Legacy single token (backwards compatible)

```yaml
api:
  apiKey: "my-single-token"
```

Equivalent to `{"default": "my-single-token"}`. Prefer A or B for new deployments.

## Token names in audit logs

Every authenticated request carries the token name in log context:

```json
{"time":"...","level":"INFO","msg":"installing helm release","release":"arc-team-x","caller":"frontend"}
```

The token value is never logged.

## Revoking a token

Update the secret removing the entry, then rolling-restart the API:

```bash
kubectl patch secret arc-runner-manager-tokens \
  --namespace arc-runner-manager \
  --type merge \
  -p '{"stringData":{"api-tokens":"{\"frontend\":\"tok-fe-NEW\",\"ci-system\":\"tok-ci-...\"}"}}'

kubectl rollout restart deployment arc-runner-manager-api \
  --namespace arc-runner-manager
```

Only the patched token changes. Other tokens are unaffected.

## Local development

The Taskfile passes `API_KEY=dev-key-change-me` by default (legacy fallback).
Override with multi-token format if needed:

```bash
API_TOKENS='{"dev":"my-token","ci":"ci-token"}' task run:api
```