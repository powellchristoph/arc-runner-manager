# Ingress

Both the API and frontend services support Ingress, controlled independently.

## Prerequisites

- A working Ingress controller (e.g. ingress-nginx)
- cert-manager with a ClusterIssuer (for TLS)

## Minimal example — frontend only (most common)

```yaml
frontend:
  ingress:
    enabled: true
    className: "nginx"
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt-prod
      nginx.ingress.kubernetes.io/ssl-redirect: "true"
    host: "arc-runners.example.com"
    tls:
      enabled: true
      secretName: "arc-runners-tls"
```

## Both services exposed

The API ingress is useful if you want external CI systems to call the API
directly rather than through the frontend proxy, or for multi-cluster setups.

```yaml
api:
  ingress:
    enabled: true
    className: "nginx"
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt-prod
      nginx.ingress.kubernetes.io/ssl-redirect: "true"
    host: "arc-api.example.com"
    tls:
      enabled: true
      secretName: "arc-api-tls"

frontend:
  ingress:
    enabled: true
    className: "nginx"
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt-prod
      nginx.ingress.kubernetes.io/ssl-redirect: "true"
    host: "arc-runners.example.com"
    tls:
      enabled: true
      secretName: "arc-runners-tls"
```

## Restricting API access

If you expose the API, consider restricting it to internal networks using
nginx annotations:

```yaml
api:
  ingress:
    annotations:
      nginx.ingress.kubernetes.io/whitelist-source-range: "10.0.0.0/8,172.16.0.0/12"
```

## Full helm install example

```bash
helm upgrade --install arc-runner-manager ./charts/arc-runner-manager \
  --namespace arc-runner-manager \
  --create-namespace \
  --set api.existingSecret=arc-runner-manager-tokens \
  --set frontend.ingress.enabled=true \
  --set frontend.ingress.host=arc-runners.example.com \
  --set frontend.ingress.tls.enabled=true \
  --set frontend.ingress.tls.secretName=arc-runners-tls \
  --set "frontend.ingress.annotations.cert-manager\.io/cluster-issuer=letsencrypt-prod"
```