# 0007 — Push-based status updates (Postgres `LISTEN/NOTIFY`)

**Status:** accepted
**Related:** [0004](./0004-data-model-and-partitioning.md) (partitioned reads), [0006](./0006-transactional-outbox.md)

## Problem

The WebSocket endpoint `GET /api/v1/submissions/:id/stream` was advertised as
real-time. It was a polling loop wearing a WebSocket as a costume:

```go
ticker := time.NewTicker(500 * time.Millisecond)
for range ticker.C {
    job, _ := getJobUC.Execute(ctx, id)   // SELECT, every 500ms, per connection
    if changed(job) { conn.WriteJSON(job) }
}
```

Three problems, in increasing order of how much they matter:

1. **Latency is a function of the poll interval, not of the event.** A job that
   finishes 5 ms after a tick is reported 495 ms later.
2. **Load scales with connections, not with work.** 200 idle viewers waiting on
   long-running jobs cost 400 `SELECT`s per second against a partitioned table,
   whether or not anything has changed. The database is asked "has this changed
   yet?" hundreds of times to hear "no".
3. **The cost is paid where it hurts most.** Every one of those reads carries a
   `created_at` predicate to get partition pruning ([0004](./0004-data-model-and-partitioning.md)),
   so each is cheap on its own — which is exactly what makes the aggregate easy
   to miss until the connection count grows.

There was no notification path from worker to API at all. The worker writes the
result to Postgres and acks; the API had no way to learn about it except by
asking again.

## Decision

Let the database announce the change, since it is the one component that
witnesses every write.

**A trigger on the table** (`migrations/001_initial_schema.up.sql`) fires
`pg_notify` on the `job_status` channel whenever `status`, `stdout`, `stderr`, or
the metrics columns change. It fires on the same statement that made the change,
inside the same transaction — so if the write rolls back, no notification is
sent, and there is no window where a client is told about a status that does not
exist.

**One listener per API process** (`delivery/http/notifier.go`) holds a dedicated
connection, `LISTEN job_status`, and fans each payload out to the in-process
subscribers registered for that `job_id`. One connection per replica, not one per
viewer.

**The WebSocket handler subscribes** instead of polling. It sends current state
on connect, then forwards what arrives.

### The safety poll stays

Not because the trigger is unreliable, but because `NOTIFY` is fire-and-forget:
a payload published while the listener connection is down is not queued, not
replayed, and not recoverable. So the handler keeps a **5-second** poll as a
backstop — 10× cheaper than before, and it bounds worst-case staleness if a
notification is ever missed.

This is the important design point, and it is why the interval was lowered rather
than removed: the push is the fast path, the poll is the correctness floor. A pure
push implementation would be a system whose correctness depends on a connection
never dropping.

### Payload carries the ID, not the data

`pg_notify` payloads are capped at 8000 bytes, and `stdout`/`stderr` are capped at
64 KB each — so a payload carrying the result would silently fail to send on
exactly the jobs anyone cares about. The notification carries `job_id` and
`created_at`; the handler then reads the row, with a `created_at` predicate so
the read still prunes partitions.

That read happens **once per actual change**, instead of twice per second per
viewer.

## Consequences

- Terminal status reaches the browser in **single-digit milliseconds** after the
  commit rather than up to 500 ms later. Measured end-to-end: a job sleeping
  2000 ms was reported at 2021 ms wall-clock, against a `updated_at - created_at`
  of 2029 ms.
- Read volume dropped ~80% in a 5-connection / 20-second measurement (41 index
  scans against a 200-scan baseline), and the gap widens with connection count,
  because the new cost is per-change while the old cost was per-connection.
- The trigger is now load-bearing for latency. `make k8s-verify-schema` guards it
  from being dropped when the k8s init ConfigMap is regenerated.
- Notifications are process-local after the listener receives them. With N API
  replicas, every replica gets every notification and discards those it has no
  subscriber for. Wasteful in principle; at this scale one `LISTEN` connection per
  replica is far cheaper than what it replaced.
- The listener reconnects with backoff if its connection drops, and the 5-second
  poll covers the gap while it is down. This is the path the safety poll exists
  for, and the only one that is expected in normal operation.

## Alternatives considered

**Worker → API HTTP callback.** Requires the worker to know API addresses, breaks
when the API is behind a rolling deploy, and needs its own retry logic. The
database is already in the path of every write and already knows.

**RabbitMQ fanout for status events.** A second exchange, a second consumer in the
API, and a second thing to get wrong — for events that are already visible in
Postgres. Worth revisiting if status updates ever need to leave the cluster.

**Redis pub/sub.** Same shape as `LISTEN/NOTIFY`, with the same fire-and-forget
weakness, plus a dependency the worker deliberately does not have
([0005](./0005-job-lease-and-recovery.md) removed it).

**Drop the poll entirely.** Rejected: it makes a missed notification permanent.
Worst case would be a client hanging on a job that finished, which is a worse
failure than a 5-second delay.
