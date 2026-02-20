# =============================================================================
# Project Sentinel — API Server Dockerfile
# Multi-stage build: Go compile → minimal Alpine runtime
# =============================================================================

# ── Stage 1: Build ──────────────────────────────────────────
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /build

# Cache Go module downloads (rarely changes)
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# Copy source and build a static binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-w -s" -o /sentinel-api ./cmd/server

# ── Stage 2: Runtime ───────────────────────────────────────
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata curl && \
    addgroup -S sentinel && \
    adduser -S -G sentinel sentinel

COPY --from=builder /sentinel-api /usr/local/bin/sentinel-api

USER sentinel

EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=5s --start-period=15s --retries=5 \
    CMD curl -sf http://localhost:8080/api/v1/health || exit 1

ENTRYPOINT ["sentinel-api"]
