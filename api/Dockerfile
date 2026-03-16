# ── build stage ───────────────────────────────────────────────────────────────
FROM golang:1.25.8-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /bin/arc-runner-manager-api ./cmd/server

# ── runtime stage ─────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /bin/arc-runner-manager-api /arc-runner-manager-api
# Helm needs a writable temp dir for chart cache.
# distroless/nonroot provides /tmp.

EXPOSE 8080

USER nonroot:nonroot
ENTRYPOINT ["/arc-runner-manager-api"]
