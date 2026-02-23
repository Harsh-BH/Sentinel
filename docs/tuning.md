# Performance Tuning Guide

This guide covers the key tuning parameters for running Sentinel under load. All values shown are defaults — adjust based on your hardware, workload, and monitoring data.

---

## Table of Contents

- [API Server](#api-server)
- [Worker Service](#worker-service)
- [PostgreSQL Connection Pool](#postgresql-connection-pool)
- [RabbitMQ](#rabbitmq)
- [Redis](#redis)
- [KEDA Auto-Scaling](#keda-auto-scaling)
- [nsjail Sandbox Limits](#nsjail-sandbox-limits)
- [Tuning Methodology](#tuning-methodology)

---

## API Server

| Variable | Default | Description |
|----------|---------|-------------|
| `API_PORT` | `8080` | HTTP listen port |
| `API_READ_TIMEOUT` | `10s` | Max time to read request body |
| `API_WRITE_TIMEOUT` | `30s` | Max time to write response (includes WebSocket) |
| `API_RATE_LIMIT` | `100` | Max requests per minute per IP |
| `GIN_MODE` | `debug` | Set to `release` in production |

### Recommendations

- **Rate limit**: Start at 100/min for dev, increase to 500-1000 for production behind a CDN.
- **Write timeout**: Must be ≥ your longest expected WebSocket stream (30s is safe for 10s execution + polling).
- **GIN_MODE**: Always set to `release` in production — disables debug logging and improves throughput by ~15%.

---

## Worker Service

| Variable | Default | Description |
|----------|---------|-------------|
| `WORKER_POOL_SIZE` | `4` | Concurrent goroutines executing sandboxed code |
| `WORKER_METRICS_PORT` | `9090` | Prometheus metrics HTTP port |
| `WORKER_DEFAULT_TIME_LIMIT_MS` | `5000` | Default execution wall-clock limit |
| `WORKER_DEFAULT_MEMORY_LIMIT_KB` | `262144` | Default memory limit per execution (256 MB) |

### Worker Pool Sizing

The `WORKER_POOL_SIZE` determines how many concurrent nsjail sandboxes run on a single worker pod.

**Formula**: `WORKER_POOL_SIZE = min(CPU_CORES, RAM_GB / 0.5)`

| Instance Type | CPUs | RAM | Recommended Pool Size |
|---------------|------|-----|----------------------|
| 2 CPU / 4 GB | 2 | 4 GB | 2–3 |
| 4 CPU / 8 GB | 4 | 8 GB | 4–6 |
| 8 CPU / 16 GB | 8 | 16 GB | 8–12 |

**Key insight**: Each nsjail sandbox is CPU-bound during execution and memory-bound at rest. Don't exceed available cores — context switching hurts p99 latency.

### K8s Resource Requests

Match resource requests to pool size:

```yaml
resources:
  requests:
    cpu: "1"                    # 1 core per 2 pool workers
    memory: "512Mi"             # 256MB per pool worker + overhead
  limits:
    cpu: "4"
    memory: "2Gi"
```

---

## PostgreSQL Connection Pool

The API server uses `pgxpool` for connection pooling. The pool is configured via the `DATABASE_URL` connection string.

### pgx Pool Defaults

| Parameter | Default | Description |
|-----------|---------|-------------|
| `pool_max_conns` | `4` (pgx default) | Maximum open connections |
| `pool_min_conns` | `0` | Minimum idle connections |
| `pool_max_conn_lifetime` | `1h` | Max lifetime of a connection |
| `pool_max_conn_idle_time` | `30m` | Max idle time before close |

### Tuning via Connection String

```
postgres://sentinel:pass@localhost:5432/sentinel?sslmode=disable&pool_max_conns=20&pool_min_conns=5
```

### Recommendations

| Deployment | API Replicas | Pool Size per Replica | Total Connections |
|------------|-------------|----------------------|-------------------|
| Dev (single) | 1 | 10 | 10 |
| Staging | 2 | 15 | 30 |
| Production | 3 | 20 | 60 |

**Rule of thumb**: `pool_max_conns = (2 × CPU_CORES) + effective_spindle_count`  
For SSDs: `pool_max_conns ≈ CPU_CORES × 3`

**PostgreSQL `max_connections`** must be ≥ sum of all pool sizes + overhead:
```
max_connections = (API_replicas × pool_size) + (Worker_replicas × 2) + 10  # overhead for replication, monitoring
```

---

## RabbitMQ

### Channel & Prefetch Tuning

| Parameter | Default | Description |
|-----------|---------|-------------|
| Worker prefetch count | `1` (in consumer code) | Messages fetched ahead per consumer |
| Channel multiplexing | 1 channel per worker | Each worker goroutine uses its own channel |

### Prefetch Count

The prefetch count controls how many messages each worker goroutine will buffer:

| Prefetch | Behavior | Use When |
|----------|----------|----------|
| 1 | Fair dispatch, higher latency | Execution time varies (default) |
| 5 | Batch dispatch, lower overhead | Uniform fast executions |
| 10+ | High throughput, poor fairness | Only if executions are very fast (<100ms) |

**Sentinel default**: Prefetch 1 is optimal because execution times vary widely (100ms Python print vs 10s C++ compile). Higher prefetch causes head-of-line blocking.

### Queue Tuning

| Setting | Value | Description |
|---------|-------|-------------|
| Queue type | Quorum | Replicated across RabbitMQ nodes for durability |
| DLX | `execution_tasks.dlx` | Dead-letter exchange for failed messages |
| TTL | None (infinite) | Messages wait until consumed |
| Max length | None | KEDA handles backpressure via scaling |

### Memory & Disk

```
# rabbitmq.conf
vm_memory_high_watermark.relative = 0.6
disk_free_limit.absolute = 1GB
```

---

## Redis

| Parameter | Default | Description |
|-----------|---------|-------------|
| `maxmemory` | `128mb` | Maximum memory usage |
| `maxmemory-policy` | `allkeys-lru` | Eviction policy |
| `appendonly` | `yes` | Persistence mode |

### Usage in Sentinel

Redis serves two purposes:
1. **Idempotency locks**: `ZADD NX` with TTL to prevent duplicate submissions
2. **Rate limiting**: Sliding window counter per IP

Both are short-lived keys (60s–5min TTL), so 128MB is sufficient for most workloads.

**Scaling estimate**: Each key ≈ 200 bytes → 128MB supports ~670K concurrent rate-limit windows.

---

## KEDA Auto-Scaling

### ScaledObject Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `pollingInterval` | `5s` | How often KEDA checks queue depth |
| `cooldownPeriod` | `60s` | Wait before scaling to 0 |
| `minReplicaCount` | `2` | Minimum worker pods |
| `maxReplicaCount` | `50` | Maximum worker pods |
| Queue trigger value | `15` | Target messages per worker |

### Scaling Behavior

| Direction | Policy | Description |
|-----------|--------|-------------|
| **Scale Up** | Max of: +5 pods or +100% every 30s | Aggressive ramp during spikes |
| **Scale Down** | -2 pods every 60s (120s stabilization) | Gentle cooldown to avoid thrashing |

### Tuning Recommendations

**For bursty workloads** (e.g., class of students submitting homework):
```yaml
pollingInterval: 3           # React faster
trigger value: 5             # Scale earlier
scaleUp: +10 pods / 15s      # Ramp aggressively
```

**For steady workloads** (e.g., continuous CI/CD integration):
```yaml
pollingInterval: 10          # Less API overhead
trigger value: 30            # Each worker handles more
scaleDown stabilization: 300 # 5 min cooldown
```

### Calculating Max Workers

```
max_workers = maxReplicaCount × WORKER_POOL_SIZE
# Default: 50 × 4 = 200 concurrent executions
```

Ensure backing infrastructure can handle this:
- **PostgreSQL**: max_connections ≥ 200 + overhead
- **RabbitMQ**: Channel limit defaults to 2047 (fine)
- **Node resources**: Each worker pod needs ~0.5–1 CPU and ~512MB RAM

---

## nsjail Sandbox Limits

| Parameter | Python | C++ (compile) | C++ (run) |
|-----------|--------|---------------|-----------|
| Wall clock time | 10s | 10s | 10s |
| Memory | 256 MB | 512 MB | 256 MB |
| PIDs | 64 | 64 | 64 |
| CPU cores | 1 | 1 | 1 |
| Disk (tmpfs) | 64 MB | 128 MB | 64 MB |

### Adjusting Limits

Edit `sandbox/nsjail/python.cfg` and `sandbox/nsjail/cpp.cfg`:

```protobuf
time_limit: 10          # Wall clock seconds
rlimit_as: 256          # Memory in MB
cgroup_pids_max: 64     # Max processes
cgroup_cpu_ms_per_sec: 1000  # CPU milliseconds per second (1000 = 1 core)
```

---

## Tuning Methodology

### Step 1: Establish Baseline

```bash
# Start the full stack
make up

# Run load test with low concurrency
k6 run scripts/load-test.js --vus 10 --duration 2m
```

### Step 2: Identify Bottlenecks

Open Grafana dashboards at [http://localhost:3001](http://localhost:3001):

1. **Sentinel Overview**: Check execution latency percentiles
2. **Worker Health**: Look for CPU/memory saturation
3. **Infrastructure**: Check PostgreSQL connection count, RabbitMQ queue depth

### Step 3: Iterate

| Symptom | Likely Bottleneck | Fix |
|---------|------------------|-----|
| High p99 latency, low CPU | pgx pool exhaustion | Increase `pool_max_conns` |
| Queue depth growing | Not enough workers | Increase `maxReplicaCount` or `WORKER_POOL_SIZE` |
| Worker CPU at 100% | Pool too large for node | Decrease `WORKER_POOL_SIZE` or add nodes |
| OOM kills in workers | Memory limit too low | Increase pod memory limits |
| RabbitMQ memory alarm | Too many queued messages | Scale workers faster (lower KEDA trigger value) |
| Redis errors | Eviction under load | Increase `maxmemory` |

### Step 4: Validate

```bash
# Full load test
k6 run scripts/load-test.js

# Should meet:
# - 0% HTTP failures
# - < 5% execution errors
# - p99 total duration < 30s
```
