# 🚀 Sentinel Deployment Guide

> Step-by-step instructions for deploying Sentinel in development and production environments.

---

## Table of Contents

- [Prerequisites](#prerequisites)
- [Local Development (Docker Compose)](#local-development-docker-compose)
- [Production Deployment (k3s / Kubernetes)](#production-deployment-k3s--kubernetes)
  - [Cluster Setup](#cluster-setup)
  - [Image Registry](#image-registry)
  - [Apply Manifests](#apply-manifests)
  - [Monitoring Stack](#monitoring-stack)
  - [TLS & Ingress](#tls--ingress)
  - [Scaling Configuration](#scaling-configuration)
- [Configuration Reference](#configuration-reference)
- [Secrets Management](#secrets-management)
- [Post-Deployment Verification](#post-deployment-verification)
- [Maintenance & Operations](#maintenance--operations)
- [Troubleshooting](#troubleshooting)

---

## Prerequisites

### All Environments

| Tool | Version | Purpose |
|------|---------|---------|
| Git | 2.30+ | Clone the repository |
| Docker | 24.0+ | Build container images |
| Docker Compose | v2.20+ | Local development stack |
| Go | 1.23+ | Build API and worker binaries |
| Node.js | 20+ | Build the frontend |

### Production Only

| Tool | Version | Purpose |
|------|---------|---------|
| Linux host | Ubuntu 22.04+, Debian 12+ | Target deployment |
| Root access (sudo) | — | k3s installation |
| Helm | v3.14+ | Installed automatically by setup script |
| curl | — | Downloading components |

### Hardware Recommendations

| Environment | CPU | RAM | Disk | Notes |
|-------------|-----|-----|------|-------|
| Development | 2 cores | 4 GB | 20 GB | Laptop/VM is fine |
| Production (single-node) | 4 cores | 16 GB | 50 GB SSD | Small workloads |
| Production (multi-node) | 8+ cores/node | 32 GB+/node | 100 GB SSD | High throughput |

---

## Local Development (Docker Compose)

### Quick Start

```bash
# 1. Clone and configure
git clone https://github.com/Harsh-BH/Sentinel.git
cd Sentinel
cp .env.example .env   # Edit as needed

# 2. Start everything
make up

# 3. Verify
make health
```

### Service Endpoints

| Service | URL | Purpose |
|---------|-----|---------|
| Frontend | http://localhost:5173 | Monaco code editor UI |
| API | http://localhost:8080 | REST + WebSocket API |
| Prometheus | http://localhost:9091 | Metrics scraping |
| Grafana | http://localhost:3001 | Dashboards (admin/sentinel) |
| RabbitMQ Admin | http://localhost:15672 | Queue management (sentinel/sentinel_secret) |

### Selective Startup

```bash
# Infrastructure only (Postgres, RabbitMQ, Redis)
make up-infra

# Run services locally (better for debugging)
make dev-api       # Terminal 1
make dev-worker    # Terminal 2
make dev-frontend  # Terminal 3

# Add monitoring
make monitoring-up
```

### Database Migrations

```bash
# Apply migrations (requires running Postgres)
make migrate

# Rollback
make migrate-down
```

### Teardown

```bash
make down          # Stop containers
make down-clean    # Stop + delete volumes (full reset)
```

---

## Production Deployment (k3s / Kubernetes)

### Cluster Setup

Sentinel ships a fully automated setup script that installs:
- **k3s** v1.30 (lightweight Kubernetes)
- **nginx-ingress** controller (with metrics)
- **cert-manager** (Let's Encrypt TLS)
- **KEDA** (queue-based autoscaling)

```bash
# Full cluster setup (installs everything)
sudo make k8s-setup

# Or run the script directly with options
sudo ./scripts/setup-k3s.sh              # Full setup
sudo ./scripts/setup-k3s.sh --manifests  # Apply manifests only (cluster exists)
sudo ./scripts/setup-k3s.sh --teardown   # Remove everything
```

The script is idempotent — safe to run multiple times. It will skip already-installed components.

#### Environment Variables

Override versions if needed:

```bash
sudo K3S_VERSION=v1.30.0+k3s1 \
     KEDA_VERSION=2.14.0 \
     CERT_MANAGER_VERSION=v1.14.4 \
     NGINX_INGRESS_VERSION=4.10.0 \
     ./scripts/setup-k3s.sh
```

### Image Registry

Build and push images to your container registry:

```bash
# Default: ghcr.io/harsh-bh
make docker-build
make docker-push

# Custom registry
REGISTRY=your-registry.com/org TAG=v1.0.0 make docker-build docker-push
```

Update image references in the manifests if using a custom registry:

- `infra/k8s/api-deployment.yaml`
- `infra/k8s/worker-deployment.yaml`
- `infra/k8s/ingress.yaml` (frontend)

### Apply Manifests

```bash
# Apply all Sentinel resources via Kustomize
make k8s-apply

# Check status
make k8s-status

# Tail logs
make k8s-logs
```

#### Resource Application Order

Kustomize applies resources in this order (defined in `infra/k8s/kustomization.yaml`):

1. **Namespace** + ResourceQuota + LimitRange
2. **Secrets** + ConfigMaps
3. **PostgreSQL** StatefulSet (data layer)
4. **RabbitMQ** StatefulSet (3-node quorum cluster)
5. **Redis** StatefulSet (AOF persistence)
6. **API** Deployment (3 replicas)
7. **Worker** Deployment (KEDA-managed, 2–50 replicas)
8. **Ingress** + Frontend Deployment + Let's Encrypt
9. **KEDA ScaledObject** + TriggerAuthentication
10. **NetworkPolicies** (default deny-all + per-component rules)

### Monitoring Stack

The monitoring stack deploys to a separate `monitoring` namespace:

```bash
# Deploy Prometheus + Grafana + postgres-exporter
make k8s-monitoring-apply

# Check status
make k8s-monitoring-status

# Port-forward to access locally
make k8s-monitoring-portforward
# → Prometheus: localhost:9091
# → Grafana:    localhost:3001 (admin/sentinel)

# Remove monitoring
make k8s-monitoring-delete
```

#### Pre-built Dashboards

| Dashboard | Description |
|-----------|-------------|
| Sentinel Overview | Execution rates, errors, latencies, active workers |
| Worker Health | Per-pod metrics, duration heatmaps, CPU/memory |
| Infrastructure | PostgreSQL, RabbitMQ, Redis, node resources |

#### Alerting

Four alerts are configured by default:

| Alert | Fires When | Severity |
|-------|-----------|----------|
| SentinelQueueBacklog | Queue > 1000 msgs for 5m | Warning |
| SentinelWorkerDown | Active workers < 1 for 3m | Critical |
| SentinelHighErrorRate | Error rate > 10% for 5m | Warning |
| SentinelSandboxFailures | > 50 failures in 5m | Critical |

### TLS & Ingress

The Ingress manifest (`infra/k8s/ingress.yaml`) includes:

- **cert-manager ClusterIssuer** for Let's Encrypt (production)
- **TLS termination** at the ingress level
- **WebSocket support** via nginx annotations
- **CORS headers** for cross-origin frontend requests

#### Custom Domain

Edit `infra/k8s/ingress.yaml`:

```yaml
spec:
  tls:
    - hosts:
        - sentinel.yourdomain.com    # ← Your domain
      secretName: sentinel-tls
  rules:
    - host: sentinel.yourdomain.com  # ← Your domain
```

Point your DNS A record to the node's external IP or load balancer IP.

### Scaling Configuration

#### Worker Auto-Scaling (KEDA)

Workers scale based on RabbitMQ `execution_tasks` queue depth:

| Parameter | Value | File |
|-----------|-------|------|
| Min replicas | 2 | `keda-scaledobject.yaml` |
| Max replicas | 50 | `keda-scaledobject.yaml` |
| Queue trigger | 15 msgs/worker | `keda-scaledobject.yaml` |
| Scale-up | +5 pods or +100% every 30s | `keda-scaledobject.yaml` |
| Scale-down | -2 pods every 60s (120s stabilization) | `keda-scaledobject.yaml` |
| Idle replicas | 0 (scales to zero when idle) | `keda-scaledobject.yaml` |

#### API HPA (CPU-based)

| Parameter | Value |
|-----------|-------|
| Min replicas | 3 |
| Max replicas | 10 |
| Target CPU | 70% |

#### Tuning Scaling

See [docs/tuning.md](./tuning.md) for detailed scaling parameter adjustments.

---

## Configuration Reference

### API Server

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | 8080 | HTTP listen port |
| `READ_TIMEOUT` | 10s | HTTP read timeout |
| `WRITE_TIMEOUT` | 30s | HTTP write timeout |
| `GIN_MODE` | debug | Gin mode (debug/release) |
| `RATE_LIMIT` | 100 | Requests per minute per IP |
| `DATABASE_URL` | — | PostgreSQL connection string |
| `RABBITMQ_URL` | — | AMQP connection string |
| `REDIS_URL` | — | Redis connection string |

### Worker

| Variable | Default | Description |
|----------|---------|-------------|
| `POOL_SIZE` | 4 | Concurrent sandbox workers |
| `METRICS_PORT` | 9090 | Prometheus metrics port |
| `NSJAIL_PATH` | /usr/bin/nsjail | Path to nsjail binary |
| `NSJAIL_CONFIG_DIR` | /etc/nsjail | nsjail config directory |
| `NSJAIL_POLICY_DIR` | /etc/nsjail/policies | Seccomp policy directory |
| `DEFAULT_TIME_LIMIT_MS` | 5000 | Default execution timeout |
| `DEFAULT_MEMORY_LIMIT_KB` | 262144 | Default memory limit (256MB) |
| `DATABASE_URL` | — | PostgreSQL connection string |
| `RABBITMQ_URL` | — | AMQP connection string |
| `REDIS_URL` | — | Redis connection string |

### ConfigMaps (Kubernetes)

All configuration is managed via ConfigMaps in `infra/k8s/configmaps.yaml`:

- `api-config` — API server configuration
- `worker-config` — Worker pool configuration
- `nsjail-python-config` / `nsjail-cpp-config` — Sandbox protobuf configs
- `rabbitmq-config` — RabbitMQ server configuration
- `pg-init-scripts` — PostgreSQL initialization SQL

---

## Secrets Management

### Development

Secrets are in plaintext in `infra/k8s/secrets.yaml` for development convenience. **Never commit production secrets.**

### Production

Replace with external secret management:

```bash
# Option 1: kubectl create secret
kubectl create secret generic api-secrets \
  --namespace sentinel \
  --from-literal=DATABASE_URL='postgres://user:pass@host:5432/sentinel?sslmode=require' \
  --from-literal=RABBITMQ_URL='amqp://user:pass@host:5672/' \
  --from-literal=REDIS_URL='redis://:pass@host:6379/0'

# Option 2: Sealed Secrets
kubeseal --format yaml < secret.yaml > sealed-secret.yaml

# Option 3: External Secrets Operator (AWS SM, Vault, etc.)
```

---

## Post-Deployment Verification

### 1. Check Pods

```bash
make k8s-status
# All pods should be Running with READY 1/1
```

### 2. Health Check

```bash
# Via port-forward
kubectl port-forward -n sentinel svc/api 8080:8080 &
curl http://localhost:8080/api/v1/health
# Expected: {"status":"ok","services":{"postgres":"ok","rabbitmq":"ok","redis":"ok"}}
```

### 3. Submit Test Job

```bash
# Submit
JOB_ID=$(curl -s -X POST http://localhost:8080/api/v1/submissions \
  -H "Content-Type: application/json" \
  -d '{"language":"python","source_code":"print(42)"}' | jq -r '.job_id')

echo "Job ID: $JOB_ID"

# Poll until complete
for i in $(seq 1 30); do
  STATUS=$(curl -s http://localhost:8080/api/v1/submissions/$JOB_ID | jq -r '.status')
  echo "Attempt $i: $STATUS"
  [[ "$STATUS" == "SUCCESS" ]] && break
  sleep 1
done

# Verify output
curl -s http://localhost:8080/api/v1/submissions/$JOB_ID | jq .
```

### 4. Run Load Test

```bash
make load-test
# Runs k6 with 10→1000 VUs over 6 minutes
```

### 5. Run Security Audit

```bash
make security-audit
# Validates all 22 sandbox security tests
```

### 6. Check Monitoring

```bash
make k8s-monitoring-portforward

# Open Grafana
# http://localhost:3001 (admin / sentinel)
# → Sentinel Overview dashboard
```

---

## Maintenance & Operations

### Rolling Updates

```bash
# Update images
REGISTRY=ghcr.io/harsh-bh TAG=v1.1.0 make docker-build docker-push

# Update manifests and apply
make k8s-apply
# Kubernetes performs rolling updates automatically (maxSurge=1, maxUnavailable=0)
```

### Database Backup

```bash
# Exec into postgres pod
kubectl exec -n sentinel -it sentinel-postgres-0 -- \
  pg_dump -U sentinel sentinel > backup-$(date +%Y%m%d).sql
```

### Log Collection

```bash
# All Sentinel pods
make k8s-logs

# Specific component
kubectl logs -n sentinel -l app=api -f
kubectl logs -n sentinel -l app=worker -f --max-log-requests=50
```

### Scaling Manually

```bash
# Temporarily override KEDA
kubectl scale deployment/worker -n sentinel --replicas=20

# Scale API
kubectl scale deployment/api -n sentinel --replicas=5
```

---

## Troubleshooting

### Pods Not Starting

```bash
# Check pod events
kubectl describe pod -n sentinel <pod-name>

# Common causes:
# - ImagePullBackOff → Check registry credentials / image tags
# - CrashLoopBackOff → Check logs: kubectl logs -n sentinel <pod-name>
# - Pending → Check resource quotas: kubectl describe resourcequota -n sentinel
```

### API Returns 503

```bash
# Check backend health
kubectl exec -n sentinel deploy/api -- curl -s localhost:8080/api/v1/health

# Common causes:
# - PostgreSQL not ready → Check PG pod status and PVC
# - RabbitMQ not formed → Wait for quorum (3/3 pods must be ready)
# - Redis down → Check redis pod and memory usage
```

### Jobs Stuck in QUEUED

```bash
# Check worker pods
kubectl get pods -n sentinel -l app=worker

# Check RabbitMQ queue depth
kubectl exec -n sentinel sentinel-rabbitmq-0 -- \
  rabbitmqctl list_queues name messages consumers

# Common causes:
# - Workers CrashLooping → nsjail binary missing or no seccomp support
# - Queue not consuming → Check AMQP connection string in worker config
# - KEDA not scaling → Check ScaledObject: kubectl describe scaledobject -n sentinel
```

### High Latency

```bash
# Check Grafana dashboards for bottlenecks
# Common causes:
# - Database connection pool exhausted → Increase pgx pool size
# - Worker pool too small → Increase POOL_SIZE or max replicas
# - Queue backlog → Check KEDA scaling triggers
# See docs/tuning.md for parameter tuning guide
```

### Network Policy Issues

```bash
# Verify policies
kubectl get networkpolicy -n sentinel

# Test connectivity from a debug pod
kubectl run debug --rm -it --namespace sentinel --image=busybox -- sh
# Inside: wget -qO- http://api:8080/api/v1/health
```

### Monitoring Not Scraping

```bash
# Check Prometheus targets
kubectl port-forward -n monitoring svc/prometheus 9091:9090
# Open http://localhost:9091/targets — all should be UP

# Common causes:
# - NetworkPolicy blocking scrape → Check monitoring network policies
# - Wrong port/path → Verify prometheus-config.yaml scrape configs
# - Pod labels changed → Update relabel configs
```

### Full Reset

```bash
# Nuclear option: tear everything down and reinstall
sudo make k8s-teardown
sudo make k8s-setup
make k8s-monitoring-apply
```

---

## Further Reading

- [Architecture Documentation](./architecture.md) — System design, data flow, security model
- [API Reference](./api.md) — Full endpoint documentation with OpenAPI 3.0 spec
- [Performance Tuning Guide](./tuning.md) — Component-by-component tuning parameters
- [MASTER_PLAN.md](../MASTER_PLAN.md) — Complete project specification
