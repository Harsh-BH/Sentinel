# =============================================================================
# Project Sentinel — Execution Worker Dockerfile
# Multi-stage build:
#   Stage 1: Build nsjail from source (Debian + build tools)
#   Stage 2: Build Go worker binary
#   Stage 3: Minimal runtime with nsjail, Python 3, g++
# =============================================================================

# ── Stage 1: Build nsjail ───────────────────────────────────
FROM debian:bookworm-slim AS nsjail-builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    git build-essential cmake pkg-config \
    libprotobuf-dev protobuf-compiler \
    libnl-3-dev libnl-route-3-dev \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build
RUN git clone --depth 1 https://github.com/google/nsjail.git && \
    cd nsjail && \
    make -j$(nproc) && \
    mv nsjail /usr/local/bin/nsjail

# ── Stage 2: Build Go worker ──────────────────────────────
FROM golang:1.23-bookworm AS go-builder

WORKDIR /build

# Cache Go modules (uses repo-root context so paths include worker/)
COPY worker/go.mod worker/go.sum ./
RUN go mod download && go mod verify

COPY worker/ .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-w -s" -o /sentinel-worker ./cmd/worker

# ── Stage 3: Runtime ──────────────────────────────────────
FROM debian:bookworm-slim

# Install language runtimes + nsjail runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    # Python runtime
    python3 python3-minimal \
    # C++ compiler
    g++ \
    # nsjail runtime deps
    libprotobuf32 libnl-3-200 libnl-route-3-200 \
    # Utilities
    ca-certificates procps curl \
    && rm -rf /var/lib/apt/lists/* \
    && ln -sf /usr/bin/python3 /usr/bin/python

# Copy nsjail binary from builder
COPY --from=nsjail-builder /usr/local/bin/nsjail /usr/bin/nsjail

# Copy worker binary from builder
COPY --from=go-builder /sentinel-worker /usr/local/bin/sentinel-worker

# Copy sandbox nsjail configs and seccomp policies
COPY sandbox/nsjail/ /etc/sentinel/nsjail/
COPY sandbox/policies/ /etc/sentinel/policies/

# Create sandbox temp directory
RUN mkdir -p /tmp/sentinel && chmod 1777 /tmp/sentinel

# Create sandbox rootfs with required system directories
# nsjail bind-mounts these into the namespace
RUN mkdir -p /sandbox/rootfs/{bin,usr,lib,lib64,dev,proc,tmp,home/runner,etc} && \
    cp -a /usr /sandbox/rootfs/ && \
    cp -a /lib /sandbox/rootfs/ && \
    cp -a /lib64 /sandbox/rootfs/ 2>/dev/null || true && \
    cp -a /bin /sandbox/rootfs/ && \
    cp -a /etc/alternatives /sandbox/rootfs/etc/ 2>/dev/null || true && \
    chmod 1777 /sandbox/rootfs/tmp

EXPOSE 9090

HEALTHCHECK --interval=10s --timeout=5s --start-period=20s --retries=5 \
    CMD curl -sf http://localhost:9090/healthz || exit 1

# Worker MUST run as root — nsjail needs CAP_SYS_ADMIN for namespace creation.
# nsjail itself drops privileges inside the sandbox via uidmap/gidmap.
ENTRYPOINT ["sentinel-worker"]
