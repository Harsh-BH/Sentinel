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
FROM golang:1.22-bookworm AS go-builder

WORKDIR /build

COPY worker/go.mod worker/go.sum ./
RUN go mod download

COPY worker/ .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s" -o /sentinel-worker ./cmd/worker

# ── Stage 3: Runtime ──────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    # Python runtime
    python3 python3-minimal \
    # C++ compiler
    g++ \
    # nsjail runtime deps
    libprotobuf32 libnl-3-200 libnl-route-3-200 \
    # Utilities
    ca-certificates procps \
    && rm -rf /var/lib/apt/lists/* \
    && ln -sf /usr/bin/python3 /usr/bin/python

# Copy nsjail binary
COPY --from=nsjail-builder /usr/local/bin/nsjail /usr/bin/nsjail

# Copy worker binary
COPY --from=go-builder /sentinel-worker /usr/local/bin/sentinel-worker

# Copy sandbox configs and policies
COPY sandbox/nsjail/ /etc/nsjail/
COPY sandbox/policies/ /etc/nsjail/policies/

# Create sandbox temp directory
RUN mkdir -p /tmp/sentinel && chmod 1777 /tmp/sentinel

# Create sandbox rootfs directories
RUN mkdir -p /sandbox/rootfs/{bin,usr,lib,lib64,dev,proc,tmp,home/runner} && \
    cp -a /usr /sandbox/rootfs/ && \
    cp -a /lib /sandbox/rootfs/ && \
    cp -a /lib64 /sandbox/rootfs/ 2>/dev/null || true && \
    cp -a /bin /sandbox/rootfs/ && \
    chmod 1777 /sandbox/rootfs/tmp

EXPOSE 9090

# Worker needs to run as root for nsjail namespace creation
# nsjail itself drops privileges inside the sandbox
ENTRYPOINT ["sentinel-worker"]
