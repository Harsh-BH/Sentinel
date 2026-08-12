# 0005 — Job leases, duplicate handling, and crash recovery

**Status:** accepted
**Supersedes:** the Redis-idempotency-lock design described in [0002](./0002-queue-and-delivery-semantics.md)

## Problem

At-least-once delivery means a worker can receive a message it has already
executed. The original defence was a Redis lock: `SETNX sentinel:lock:<job_id>`
with a 10-minute TTL, taken before execution. Acquire it and you run; find it
held and you treat the message as a duplicate, ack it, and move on.

That design lost jobs, and it lost them in exactly the situation it existed to
handle.

**Failure 1 — a crashed worker's job is silently discarded.**

1. Worker A takes the lock and starts executing.
2. Worker A is killed. It never acks.
3. RabbitMQ correctly redelivers the message to worker B. *So far the system is
   working.*
4. Worker B finds the lock held, concludes "duplicate", and **acks** — throwing
   the only remaining copy of the work away.
5. Nobody executed the job. The row sits in `RUNNING` forever.

The root cause is that one boolean was being asked to answer two different
questions: *"is someone working on this right now?"* and *"has this already been
completed?"* A lock can only answer the first. Acking on the strength of it
answers the second.

**Failure 2 — the lock lived in an evictable cache.**

Redis was configured `maxmemory 128mb`, `maxmemory-policy allkeys-lru`, sharing
that budget with the rate limiter's sorted sets — which are far larger and far
hotter. LRU will evict a lock key, which is never read after it is written, well
before its TTL. An evicted lock permits a real double execution, and no database
write was guarded, so the second run could overwrite a finished result. A user
watching the page would see their answer appear and then change.

**Failure 3 — nothing recovered a stranded row.**

Submission writes Postgres and then publishes to RabbitMQ. Two systems, so not
one atomic operation: a crash in between leaves a `QUEUED` row with no message.
Nothing swept for these.

## Decision

Move deduplication out of the cache and into the row, as a **lease**, and add a
**reaper** as the recovery backstop.

Two columns on `execution_jobs`:

```sql
lease_expires_at TIMESTAMPTZ,   -- when the current owner's claim lapses
attempts         SMALLINT NOT NULL DEFAULT 0
```

**Claiming** is a single conditional UPDATE, which is the entire mutual-exclusion
mechanism:

```sql
UPDATE execution_jobs
SET status = $1, attempts = attempts + 1,
    lease_expires_at = now() + make_interval(secs => $2)
WHERE job_id = $3 AND created_at >= $4 AND created_at < $5
  AND status NOT IN (terminal states)
  AND (lease_expires_at IS NULL OR lease_expires_at < now())
```

Two workers racing on the same row serialise on Postgres' row lock; the loser
re-evaluates the `WHERE` against the winner's committed row, sees a live lease,
and matches zero rows. Zero rows is then disambiguated with one follow-up read:

| Outcome | Meaning | Action |
|---|---|---|
| 1 row | claimed | execute |
| 0 rows, status terminal | genuine duplicate | ack |
| 0 rows, lease live | another delivery owns it | ack |
| 0 rows, no such row | poison message | dead-letter |

**Every write is guarded.** `SetResult` and `SetStatus` both carry
`AND status NOT IN (terminal)`. A duplicate execution therefore cannot corrupt
anything: whichever run finishes first wins, and the loser's write is rejected
with `ErrAlreadyTerminal`, which the caller treats as a duplicate rather than an
error. This is what lets us stop trying to prevent double execution and merely
make it harmless.

**The reaper** (`api/internal/usecase/reap_jobs.go`) sweeps for rows nobody owns:

```sql
WHERE status IN ('QUEUED','COMPILING','RUNNING')
  AND ( (lease_expires_at IS NOT NULL AND lease_expires_at < now())
     OR (lease_expires_at IS NULL AND updated_at < now() - grace) )
ORDER BY created_at LIMIT $n
FOR UPDATE SKIP LOCKED
```

The two branches carry different evidence, which is why they need different
conditions. An **expired lease** is proof on its own that the owner is gone, so
that branch needs no grace period and recovers fast. A row with **no lease** is
either the dual-write gap or an already-reaped job, and nothing but elapsed time
distinguishes it from a job that is simply still queued — hence `grace`.

`FOR UPDATE SKIP LOCKED` makes the sweep safe on every API replica without
electing a leader: a replica that loses the race on a row skips ahead rather than
blocking behind the winner.

## Two mistakes worth recording

**The reaper must clear the lease, not take one.** The first implementation had
it take a reservation lease so concurrent reapers wouldn't double-publish. That
deadlocks recovery: the worker receiving the re-published message cannot claim a
job whose lease is live, so it drops the message as a duplicate, and the job
stays stuck until the reaper's own lease lapses — then repeats, forever, with
`attempts` never incrementing so the retry cap never trips. Clearing the lease
and relying on `updated_at` (bumped by the trigger) to suppress re-reaping is
both simpler and correct.

**`attempts` must be incremented by the reaper too.** Counting only worker
claims means a job re-published into a queue with no live consumer increments
nothing, so the cap never trips and each sweep stacks another duplicate message.
Incrementing in both places makes `attempts` mean "total delivery attempts",
which is the semantic the cap actually wants.

## Consequences

**Good**

- A crashed worker's job is recovered rather than discarded. Verified by
  SIGKILLing a worker mid-execution and asserting the job still reaches
  `SUCCESS` (`scripts/integration-test.sh`).
- A finished result can never be overwritten, whatever delivery does.
- **Redis is no longer on the execute path at all.** The worker doesn't connect
  to it. A Redis outage or eviction can no longer fail or duplicate a job; Redis
  now backs only rate limiting, where LRU eviction is appropriate.
- One fewer dependency in the worker, and one fewer network round trip per job:
  the claim is folded into the status update we were already doing.

**Costs**

- Recovery is not instant. Worst case is lease expiry plus one sweep interval
  (~35 s + 15 s with current settings) for a crashed worker, and `grace` (2 min)
  for a job stranded by the dual-write gap. An outbox would recover in
  milliseconds; we chose fewer moving parts over lower recovery latency.
- Double execution is now *possible by design* (a zombie worker plus a reclaim).
  It is wasted CPU, bounded by the terminal-status guard. Anything with external
  side effects would need real idempotency keys instead.
- Lease sizing is a tuning knob with teeth. Too short and a healthy worker loses
  its own lease mid-job and gets re-executed; too long and crash recovery
  crawls. It is currently `time_limit + 30 s` (`+ compile budget` for C++).
- Two more columns and one more index on a partitioned table.

## Alternatives considered

- **Keep the Redis lock, but check the DB for a terminal status too.** Would fix
  failure 1, but still leaves correctness depending on a cache that is
  configured to evict, and still needs a reaper for failure 3. Two sources of
  truth for "is this claimed" was the original bug; adding a tiebreak to it is
  not a fix.
- **Short Redis lock with a heartbeat (lease renewal).** Correct, and how a
  distributed lock manager does it. Rejected: it needs a renewal goroutine per
  job and still can't survive eviction, to replace something one SQL predicate
  already does against durable state.
- **`SELECT ... FOR UPDATE` around the whole execution.** Holds a database
  transaction open for the entire sandbox run — seconds — burning a connection
  per in-flight job and making Postgres the availability bottleneck. Rejected.
- **Transactional outbox for the submit path.** Genuinely better recovery
  latency. Deferred, not dismissed: the reaper covers the same failure, and the
  outbox adds a table plus a relay process. Revisit if recovery latency ever
  matters more than component count.
