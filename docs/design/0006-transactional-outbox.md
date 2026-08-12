# 0006 — Transactional outbox for the submit path

**Status:** accepted
**Related:** [0002](./0002-queue-and-delivery-semantics.md) (delivery semantics), [0005](./0005-job-lease-and-recovery.md) (guarded writes)

## Problem

Submitting a job was two writes to two systems, with no relationship between
them:

```go
repo.Create(ctx, job)        // 1. Postgres: INSERT ... status = QUEUED
publisher.Publish(ctx, msg)  // 2. RabbitMQ: publish to sentinel.direct
```

Neither ordering is correct, because there is no ordering that makes two
independent systems commit atomically.

**The window that loses work.** The row commits, then the process is killed — or
the broker refuses the publish — before the message is out. Postgres now holds a
`QUEUED` row that no queue message corresponds to. Nothing will ever consume it.
The client was told `202 Accepted`, and the job sits `QUEUED` until a human
notices.

**The window that invents work.** Reverse the order and a published message can
reference a row that was never committed. The worker consumes it, fails to find
the job, and the message either dies in the DLX or is retried forever.

The old code made the first case worse by handling the publish failure this way:

- mark the job `INTERNAL_ERROR`
- return `503` to the client

So a transient RabbitMQ blip — a rolling restart, a network hiccup, a failover —
became a *permanently failed job*, even though the submission was valid and the
row was already durable. The system had a dependency on the broker being up at
the exact instant of submission, which is precisely the property a queue is
supposed to remove.

## Decision

Write the queue message into Postgres in the **same transaction** as the job row,
then deliver it from there.

```
BEGIN
  INSERT INTO execution_jobs (...)            -- the job
  INSERT INTO job_outbox (job_id, payload)    -- its queue message
COMMIT                                        -- both, or neither
```

After the commit the API attempts an **inline publish** on the hot path, so the
latency profile is unchanged in the normal case. If that publish succeeds, the
outbox row is deleted. If it fails, nothing is retried inline — the row simply
stays, and the API still returns `202`, because the work is already durable.

A **relay** goroutine (`usecase/relay_outbox.go`) sweeps the table on an
interval, claims rows older than `min_age` with `FOR UPDATE SKIP LOCKED`,
publishes them, and deletes what it confirms. `SKIP LOCKED` is what makes it safe
to run one relay per API replica without them fighting over the same rows.

Delivery is therefore guaranteed by the commit, not by the broker being
reachable. The commit is the point of no return, and it involves exactly one
system.

## Why this does not create duplicates

It does — deliberately. A crash between "publish succeeded" and "delete the
outbox row" means the relay publishes it again. That is at-least-once, the same
contract [0002](./0002-queue-and-delivery-semantics.md) already assumes, and it
is absorbed by the machinery [0005](./0005-job-lease-and-recovery.md) already
built: the lease claim, and the `status NOT IN (terminal)` guard on every write.

Choosing at-least-once here is deliberate. The alternative — deleting the outbox
row in the same transaction as the publish — is impossible, because the publish
is not transactional. Given a choice between "possibly twice" and "possibly
never", possibly-twice is the one that a terminal-state guard can make safe.

## Consequences

- `POST /api/v1/submissions` **succeeds while RabbitMQ is down.** Verified in
  `scripts/integration-test.sh`: stop the broker, submit, get `202`, see the row
  held in `job_outbox`; start the broker, watch the relay deliver it and the job
  run to `SUCCESS`.
- Submission is one round trip to Postgres, not one to Postgres plus one to
  RabbitMQ. The publish is still attempted inline, but it is no longer in the
  critical path for correctness.
- `job_outbox` is a queue in a database, so it must be kept small. It is: rows
  are deleted on confirmed delivery, so steady-state depth is ~0 and the table
  stays in cache. It grows only while the broker is unreachable — which is
  exactly when growth is the point.
- A row that fails `max_attempts` times stops being retried and is left in place
  with its `last_error` for an operator. Silently dropping it would recreate the
  original bug in a new location.
- The relay's floor on row age (`min_age`) exists so it does not race the inline
  publish for a row the request goroutine is still working on. Without it, every
  submission gets published twice under normal operation.

## Alternatives considered

**Two-phase commit across Postgres and RabbitMQ.** Correct on paper. In practice
it needs XA support, a transaction coordinator, and recovery logic for
in-doubt transactions, and it converts a broker outage into a blocked database
transaction. Far more machinery than one table.

**Publish first, then insert.** Trades lost work for phantom work. Worse: a
phantom message is consumed immediately, while a lost row is at least still
visible in the database.

**Retry the publish inline until it succeeds.** Holds an HTTP request open across
a broker outage, exhausts the connection pool, and still loses the job if the
process dies. It converts a durability problem into an availability problem.

**Postgres `LISTEN/NOTIFY` as the queue.** Tempting, since [0007](./0007-push-based-status-updates.md)
already uses it for status updates. But `NOTIFY` is fire-and-forget: a payload
delivered while no worker is listening is gone, with no persistence, no redelivery,
and no dead-letter path. Adequate for "the status changed, go look", unacceptable
for the work itself.
