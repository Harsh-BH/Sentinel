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
    flex bison \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

# Pin nsjail to an exact commit.
#
# This used to be `git clone --depth 1` of the default branch — i.e. whatever was
# on master the day the image happened to be built — under a comment claiming the
# build was pinned for reproducibility. nsjail is the security boundary of this
# entire system; two builds a week apart could contain different sandbox code
# with no record of the change.
ARG NSJAIL_REF=f78475530b46d0186111a9096b30725f816b55fe
RUN git clone https://github.com/google/nsjail.git && \
    cd nsjail && \
    git checkout --quiet "${NSJAIL_REF}" && \
    git submodule update --init --recursive --depth 1 && \
    make -j"$(nproc)" && \
    strip nsjail && \
    mv nsjail /usr/local/bin/nsjail && \
    /usr/local/bin/nsjail --help >/dev/null 2>&1 || true

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

# Copy sandbox nsjail configs and seccomp policies.
# This is the single copy of these files that reaches production — there is no
# ConfigMap duplicate (see infra/k8s/configmaps.yaml for why).
COPY sandbox/nsjail/ /etc/sentinel/nsjail/
COPY sandbox/policies/ /etc/sentinel/policies/

# Sandbox scratch space. In Kubernetes TMPDIR is pointed at a size-limited
# emptyDir instead; see infra/k8s/worker-deployment.yaml.
RUN mkdir -p /tmp/sentinel && chmod 1777 /tmp/sentinel

# NOTE: there is deliberately no /sandbox/rootfs here.
#
# A previous layer built one by copying /usr, /lib and /bin into it — roughly
# 400 MB — but the nsjail configs bind-mount those paths from the container root
# (`mount { src: "/usr" dst: "/usr" }`), so nothing ever read /sandbox/rootfs. It
# was pure image weight.

EXPOSE 9090

HEALTHCHECK --interval=10s --timeout=5s --start-period=20s --retries=5 \
    CMD curl -sf http://127.0.0.1:9090/readyz || exit 1

# Worker MUST run as root — nsjail needs CAP_SYS_ADMIN for namespace creation.
# nsjail itself drops privileges inside the sandbox via uidmap/gidmap.
ENTRYPOINT ["sentinel-worker"]
