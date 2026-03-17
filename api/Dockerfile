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
# alpine: small (~5 MB), has wget for healthchecks, and musl libc for any
# dynamically-linked tools. The Go binary is CGO_ENABLED=0 (fully static) so
# distroless/static would also work, but alpine is simpler for local dev.
FROM alpine:3

RUN apk add --no-cache ca-certificates wget

COPY --from=builder /bin/arc-runner-manager-api /arc-runner-manager-api

EXPOSE 8080

# Run as non-root (alpine's nobody uid=65534).
USER nobody:nobody
ENTRYPOINT ["/arc-runner-manager-api"]
