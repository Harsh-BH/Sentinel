# 0002 — Queue topology and delivery semantics

**Status:** Accepted (the previously-documented declaration mismatch is now resolved — see history at the bottom of this doc)
**Implements:** `api/internal/publisher/rabbitmq.go`, `worker/internal/delivery/amqp/consumer.go`, `worker/internal/usecase/execute_job.go`

## Context

The API decouples submission from execution by enqueueing a message to RabbitMQ. The worker consumes from that queue, runs the job in nsjail, and persists the result. Sentinel's correctness contract is **at-least-once delivery with exactly-once side effects** — no submission is silently dropped, but a job is never executed twice with two visible results.

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

- `prefetch=1` per consumer goroutine. This is intentional and load-bearing — it provides natural backpressure (workers stop pulling when busy) and avoids head-of-line blocking when one job is slow.
- **Manual ack, after execute** — the consumer wraps each `amqp.Delivery` in a `JobMessage` carrying `Ack`/`Nack` closures. The worker pool calls these only after the result has been persisted to Postgres. If the worker crashes mid-execution, RabbitMQ redelivers.
- On context cancellation during dispatch, the in-flight delivery is `Nack(requeue=true)` so it goes back to the queue, not to the DLQ.
- Reconnect uses exponential backoff on the consumer too, identical contract to the publisher.

### Idempotency (the "exactly-once side effects" guarantee)

Because the queue is at-least-once, a worker may receive a message it has already executed (e.g. it crashed after persisting results but before acking). Before executing, the worker takes a **Redis idempotency lock** keyed by `job_id` (`SET NX EX <ttl>`). On lock acquisition: execute. On lock-already-held or job-already-terminal in DB: skip execution and ack the message. This makes the execution side effect at-most-once even though delivery is at-least-once.

The Postgres write itself is idempotent because we update by primary key and the worker only writes terminal statuses; replays produce the same row state.

### Failure routing

| Failure mode                                | Delivery action |
|--------------------------------------------|-----------------|
| JSON unmarshal error (poison message)       | `Nack(requeue=false)` → DLX immediately |
| Sandbox returns `INTERNAL_ERROR`            | Persist as terminal, then `Ack` (no retry — see below) |
| Worker crash before ack                     | RabbitMQ redelivers; idempotency lock prevents duplicate side effects |
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
- **Exactly-once via 2PC** — RabbitMQ doesn't offer it, and emulating it (transactional outbox pattern) adds latency and complexity for a guarantee we don't need: at-least-once + idempotent side effects gets us the same observable behavior.
