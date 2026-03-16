# arc-runner-manager — Frontend

Bootstrap-based web UI for the ARC Runner Manager API.

## What it does

- Lists all runner scale sets with live status (Helm status, running/pending pod counts, secret presence)
- Create, edit, delete runner scale sets via modal forms
- Detail side-drawer with full configuration view
- Proxies all API calls server-side — the browser never holds the API key

## Architecture

The frontend is a single Go HTTP binary that:

1. Serves static files from `/app/static/` (HTML + JS)
2. Proxies `/api/*` requests to the backend API, injecting the `Authorization: Bearer` header server-side

The UI itself is a single-page Bootstrap 5 application with vanilla JavaScript —
no build step, no bundler.

```
frontend/
  main.go           HTTP server + proxy handler
  main_test.go      Integration tests for proxy and healthz
  go.mod
  static/
    index.html      Single HTML file — Bootstrap 5, all UI structure
    js/
      app.js        All frontend logic — API calls, rendering, modals
```

## Configuration

| Variable | Required | Description |
|---|---|---|
| `LISTEN_ADDR` | No (`:3000`) | HTTP listen address |
| `API_URL` | **Yes** | Backend API base URL, e.g. `http://arc-runner-manager-api:8080` |
| `API_KEY` | **Yes** | API key injected server-side into proxied requests |

## Local development

```bash
cd frontend
API_URL=http://localhost:8080 API_KEY=dev-key go run .
# Open http://localhost:3000
```

Or with Docker Compose from the repo root:

```bash
API_KEY=dev-key docker compose up --build
# Open http://localhost:3000
```

## Tests

```bash
cd frontend
go test -race ./...
```
