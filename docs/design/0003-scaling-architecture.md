# 0003 — Scaling architecture

**Status:** Accepted
**Implements:** `infra/k8s/keda-scaledobject.yaml`, `infra/k8s/api-deployment.yaml`, `infra/k8s/worker-deployment.yaml`, `worker/internal/pool/pool.go`

## Context

Sentinel's load profile is bursty (think a programming class hitting submit at the same moment) and trivially parallel (one job per worker goroutine, no shared state in the hot path). The scaling challenge is matching capacity to queue depth quickly without thrashing, and not melting the upstream stateful services (Postgres, RabbitMQ) when we do.

## Two-level concurrency

Workers concurrency is structured at two levels:

1. **Process-level (KEDA)** — `worker-deployment` replicas scale based on RabbitMQ queue depth.
2. **Goroutine-level (worker pool)** — each pod runs `WORKER_POOL_SIZE` goroutines, each consuming with prefetch=1.

Total in-flight executions at any time: `replicas × pool_size`. Default: `min=2 × pool_size=4 = 8` to `max=50 × pool_size=4 = 200`.

The split matters:
- Pod-level scaling is **slow** (image pull, startup, KEDA polling interval). It absorbs sustained load.
- Goroutine-level scaling is **instant** (channel send) but capped by per-pod resources. It absorbs spike-shaped load that arrives within a single KEDA cooldown.

## KEDA configuration

```yaml
triggers:
- type: rabbitmq
  metadata:
    queueName: execution_tasks
    mode: QueueLength
    value: "15"            # target messages per replica
minReplicaCount: 2         # always-warm baseline
maxReplicaCount: 50
pollingInterval: 5         # seconds
cooldownPeriod: 60         # seconds before scale-to-min
advanced:
  horizontalPodAutoscalerConfig:
    behavior:
      scaleUp:
        policies:
        - type: Pods
          value: 5
          periodSeconds: 30
        - type: Percent
          value: 100
          periodSeconds: 30
        selectPolicy: Max     # +5 pods OR +100%, whichever is bigger
      scaleDown:
        stabilizationWindowSeconds: 120
        policies:
        - type: Pods
          value: 2
          periodSeconds: 60
```

### Why these numbers

- **`minReplicaCount: 2`** — single-replica leaves no headroom during a KEDA poll-interval gap; two pods means a queue spike is consumed by goroutine-level concurrency immediately even before scale-up fires.
- **`value: 15`** — the lower this is, the more aggressive scale-up. 15 messages/replica means a backlog of 30 with 2 replicas triggers the first scale step. Tuned to avoid scaling on a single user submitting a tight loop of 10 tasks.
- **scale-up `Max(+5 pods, +100%)`** — lets us double quickly while small (2→4→8→16→32→50 in five steps) and add a fixed buffer when we're large (40→45→50 in two steps).
- **scale-down stabilization `120s`** — long enough that a queue dip from a fast batch completing doesn't immediately drop replicas we'll need 30 s later.

### API replicas (CPU-based HPA)

The API is stateless and CPU-bound on JSON marshaling and DB I/O. We use a vanilla CPU HPA (target 60%, min 3, max 10) — not KEDA — because the API's load is uncorrelated with queue depth.

## Backpressure mechanics

Backpressure flows backward through the system:
1. **Sandbox saturation** — when a goroutine is busy in `cmd.Run`, it does not pull a new message from the channel.
2. **Channel saturation** — the worker pool feeds from a buffered channel sized `2 × pool_size`. When full, the AMQP consumer blocks on channel send.
3. **AMQP consumer blocked** — with `prefetch=1`, no new message is pulled from the broker. The unacked count stops growing.
4. **Queue depth grows** — KEDA observes this on its next poll and adds replicas.

This chain means **the queue is the load-shedding buffer**. If RabbitMQ itself is saturated, publisher confirms time out at the API and clients see 503 — but no jobs silently disappear and no workers drop work mid-execution.

The chain breaks if any link auto-acks or has unbounded prefetch. Don't change those.

## Capacity math

| Component                  | Per-job cost                              | Bottleneck @ 200 concurrent |
|----------------------------|-------------------------------------------|-----------------------------|
| Worker CPU                 | ~1 core × wall-clock duration             | 200 cores worth of node compute |
| Worker memory              | 256 MB sandbox + ~50 MB Go runtime        | ~60 GB cluster-wide |
| Postgres connections       | 1 active per worker for status update     | `max_connections ≥ 200 + API pool + headroom` |
| Postgres write QPS         | ~3 writes/job (QUEUED, RUNNING, terminal) | ~600 writes/s sustained |
| RabbitMQ messages          | 1 in + 1 ack                              | Trivial (RMQ handles 10k+/s) |
| Redis ops                  | 1 SETNX + 1 GET                           | Trivial |

The two real ceilings are **node CPU/memory** (you can't run more sandboxes than the cluster can host) and **Postgres `max_connections`** (each worker keeps an open connection in pgxpool). Tuning both is documented in [`tuning.md`](../tuning.md).

## Alternatives considered

- **HPA on CPU instead of queue depth** — fails to scale up before pods are saturated. By the time CPU is at 80%, queue is already deep and latency spiked. Queue-depth-driven scaling reacts to the leading indicator (work waiting), not the lagging one (work hurting).
- **Single big worker pool, no pod-level scaling** — wastes CPU at idle (always pay max), can't survive a node failure, and violates Kubernetes resource hygiene. Rejected.
- **Per-language queues** — would let us scale Python and C++ independently. Rejected for now: the workloads aren't differentiated enough to be worth doubling the topology. Reconsider if we add languages with very different cost profiles (e.g. JVM warm-up).
- **Knative serverless** — interesting, but cold start latency (1–3 s) dominates execution time for short jobs, and per-request scaling defeats the goroutine-pool model that gives us cheap concurrency.
- **Pre-warmed sandbox pool inside each worker** — could shave 10–50 ms off cold sandbox spawn. Not implemented because nsjail spawn is ~5 ms in our measurements; the work isn't worth it.
