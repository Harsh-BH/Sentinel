# 0002 — Queue topology and delivery semantics

**Status:** Accepted (the previously-documented declaration mismatch is now resolved — see history at the bottom of this doc)
**Implements:** `api/internal/publisher/rabbitmq.go`, `worker/internal/delivery/amqp/consumer.go`, `worker/internal/usecase/execute_job.go`

## Context

The API decouples submission from execution by enqueueing a message to RabbitMQ. The worker consumes from that queue, runs the job in nsjail, and persists the result. Sentinel's correctness contract is **at-least-once delivery, at-most-once *visible* result, and eventual completion**.

Stated precisely, because the previous wording ("exactly-once side effects") was not true and hid two real bugs:

- A job may be **executed** more than once. We do not prevent that; we make it harmless.
- A job's **result** is written at most once: every result write is guarded by `status NOT IN (terminal)`, so a duplicate execution cannot overwrite a finished job.
- A job **eventually reaches a terminal state**, guaranteed by a durable lease plus a reaper rather than by delivery semantics alone.

See [design doc 0005](./0005-job-lease-and-recovery.md) for the mechanism and for what the old Redis-lock design got wrong.

## Topology

```
                       sentinel.direct (direct exchange)
   API ──publish──▶  ──routing key "execute"──▶  execution_tasks (quorum, durable)
                                                          │
                                                          │  on dead-letter
                                                          ▼
                                                    sentinel.dlx (direct DLX)
                                                          │
                                                          ▼
                                                  dead_letter_queue
```

| Resource              | Type           | Durable | Notes |
|-----------------------|----------------|---------|-------|
| `sentinel.direct`     | direct exchange| yes     | Published to by API only |
| `execution_tasks`     | quorum queue   | yes     | `x-dead-letter-exchange: sentinel.dlx`, prefetch=1 on consumer |
| `sentinel.dlx`        | direct exchange| yes     | Receives nacked-without-requeue messages |
| `dead_letter_queue`   | classic queue  | yes     | Bound to `sentinel.dlx` with empty routing key |

Both API and worker independently call `QueueDeclare` for `execution_tasks` so either can boot first. Declarations are idempotent only if **all** arguments match exactly — see "Latent inconsistency" below.

## Delivery semantics

### Publisher side (API)

- The publish channel runs in **publisher confirms** mode (`Channel.Confirm(false)`). The handler waits up to 5 s for an ACK from the broker before returning 202 to the client. A timed-out or nacked publish surfaces as `503 Service Unavailable` and the corresponding job row is marked `INTERNAL_ERROR`.
- Messages are `DeliveryMode: Persistent` and the queue is quorum-typed, so an in-flight message survives a single broker node failure.
- The connection has a self-healing watcher (`watchConnection`) with exponential backoff capped at 30 s.

### Consumer side (Worker)

- `prefetch = WORKER_POOL_SIZE` on the single consumer per worker process. Backpressure is still the point — the broker will not hand over a `pool_size + 1`-th message until one is acked, and ack is after execution — but prefetch must equal pool size, not 1. At 1 the pod executes a single job at a time regardless of pool size. See [0003](./0003-scaling-architecture.md).
- **Manual ack, after execute** — the consumer wraps each `amqp.Delivery` in a `JobMessage` carrying `Ack`/`Nack` closures. The worker pool calls these only after the result has been persisted to Postgres. If the worker crashes mid-execution, RabbitMQ redelivers.
- On context cancellation during dispatch, the in-flight delivery is `Nack(requeue=true)` so it goes back to the queue, not to the DLQ.
- Reconnect uses exponential backoff on the consumer too, identical contract to the publisher.

### Idempotency (the "exactly-once side effects" guarantee)

Because the queue is at-least-once, a worker may receive a message it has already executed. Deduplication is a **conditional UPDATE on the `execution_jobs` row** — a lease — not a cache lock:

```sql
UPDATE execution_jobs
SET status = $1, attempts = attempts + 1,
    lease_expires_at = now() + make_interval(secs => $2)
WHERE job_id = $3 AND created_at >= $4 AND created_at < $5
  AND status NOT IN (terminal states)
  AND (lease_expires_at IS NULL OR lease_expires_at < now())
```

Zero rows updated means one of three things, and the worker distinguishes them: already terminal (ack, genuine duplicate), lease still live (ack, another delivery owns it), or no such row (dead-letter, poison message).

**This replaced a Redis `SETNX` lock with a 10-minute TTL, which was actively losing jobs.** The lock could not distinguish "in progress" from "finished", so when a worker crashed mid-execution the redelivered message saw a live lock, concluded "duplicate", ACKed, and discarded the job — leaving the row in `RUNNING` forever. It also lived in a Redis configured with `maxmemory-policy allkeys-lru`, so the key could be evicted before its TTL and permit a genuine double execution. Both failure modes are gone: the lease is durable, and expiry is what makes a dead worker's job reclaimable.

The Postgres write is safe under replay because every write is guarded on the current status, not because the worker only writes terminal states — it also writes `RUNNING`/`COMPILING`. `SetResult` and `SetStatus` both carry `AND status NOT IN (terminal)`, so a late duplicate is rejected at the database rather than silently clobbering a finished result. A rejected write returns `ErrAlreadyTerminal`, which the caller treats as a duplicate, not an error.

### Failure routing

| Failure mode                                | Delivery action |
|--------------------------------------------|-----------------|
| JSON unmarshal error (poison message)       | `Nack(requeue=false)` → DLX immediately |
| Sandbox returns `INTERNAL_ERROR`            | Persist as terminal, then `Ack` (no retry — see below) |
| Worker crash before ack                     | RabbitMQ redelivers. If the lease has expired the new worker reclaims and re-executes; if not, it acks as a duplicate and the **reaper** re-publishes once the lease lapses. Verified end-to-end by SIGKILLing a worker mid-job (`scripts/integration-test.sh`) |
| Worker shutting down (ctx done) mid-dispatch| `Nack(requeue=true)` → another worker picks it up |

**Why `INTERNAL_ERROR` is acked, not retried:** if the sandbox itself fails (nsjail bug, host issue), retrying the same payload usually fails the same way. We surface the error to the user, mark the job terminal, and let DLQ alerts catch systemic problems. Operators who want a retry can re-publish from `dead_letter_queue` after fixing the underlying issue.

## History — declaration mismatch (resolved)

For most of the project's life the API publisher and worker consumer each called `QueueDeclare("execution_tasks", ...)` with **different** `x-dead-letter-exchange` arguments (API: `sentinel.dlx`; worker: `dlx.execution_tasks`) plus a worker-only `x-dead-letter-routing-key`. RabbitMQ's `QueueDeclare` is idempotent **only when all arguments match exactly**, so whichever service declared first "won" and the other got `PRECONDITION_FAILED`. The bug stayed latent for a long time because the API consistently boots first under `make up` and the worker's failed declare is non-fatal at the consumer step — until a fresh broker plus reversed startup order surfaced it as a worker crash-loop.

This was fixed by aligning the worker's declaration to the API's (the API owns the canonical topology). The worker still calls `QueueDeclare` so it can boot before the API on a fresh broker, but with matching args. See `worker/internal/delivery/amqp/consumer.go::connect`.

If you ever need to change queue arguments, change them in **both** files at once and bump a version suffix on the queue name (`execution_tasks_v2`) so old in-flight messages drain naturally rather than colliding on the new declaration.

## Alternatives considered

- **Kafka** — better for replayable streams and partitioned consumers, but heavier to operate (ZooKeeper/KRaft, brokers, topics). Our workload is per-job, not per-partition; queue semantics fit better.
- **Redis Streams** — simpler ops, but no native quorum and ack/redelivery semantics are weaker. We already use Redis for idempotency; mixing roles risks one outage taking out two layers.
- **At-most-once (auto-ack)** — would lose jobs on worker crash. Rejected outright; submissions are user-visible work.
- **Auto-ack + DB-only retries** — replicates broker concerns into application code. Rejected.
- **Exactly-once via 2PC** — RabbitMQ doesn't offer it. Rejected, but note what we pay for that: the submit path writes Postgres then publishes, and those two writes cannot be atomic, so a crash in between strands a `QUEUED` row with no message.
- **Transactional outbox** — the standard fix for the above: write the message into a table in the same transaction as the job row, and have a relay publish it. Still rejected, because the reaper already covers the same failure with one periodic sweep instead of a second moving part. The trade-off is recovery *latency*: an outbox relay recovers in milliseconds, the reaper in up to `grace + sweep_interval`. Revisit if that latency ever matters.
