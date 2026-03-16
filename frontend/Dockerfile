# ── build stage ───────────────────────────────────────────────────────────────
FROM golang:1.25.8-alpine AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /bin/arc-runner-manager-frontend .

# ── runtime stage ─────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /bin/arc-runner-manager-frontend /arc-runner-manager-frontend
COPY static/ /app/static/

EXPOSE 3000
ENV STATIC_DIR=/app/static

USER nonroot:nonroot
ENTRYPOINT ["/arc-runner-manager-frontend"]