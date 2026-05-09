# Sentinel Design Docs

RFC-style design documents capturing the **why** behind Sentinel's architecture. Each doc is numbered, narrowly scoped, and explains both the chosen design and the rejected alternatives.

These complement (not replace) the higher-level [architecture overview](../architecture.md). Read those first if you want a flyover; come here when you want to know why a specific decision was made or what trade-off it carries.

## Index

| # | Title | Topic |
|---|-------|-------|
| [0001](./0001-sandbox-security.md) | Sandbox security model | nsjail layering, kafel seccomp, threat model |
| [0002](./0002-queue-and-delivery-semantics.md) | Queue topology and delivery semantics | Exchange/queue layout, ACK-after-execute, idempotency, DLX, known declaration mismatch |
| [0003](./0003-scaling-architecture.md) | Scaling architecture | KEDA + worker pool, prefetch=1 backpressure, capacity math |
| [0004](./0004-data-model-and-partitioning.md) | Data model and partitioning | `execution_jobs` schema, UUIDv7, partial index, status state machine |

## When to add a new design doc

Add one when:
- A change has non-obvious trade-offs (security, performance, durability) that future contributors will need to understand.
- A decision is hard to reverse (schema shape, queue topology, security primitives).
- Multiple alternatives were considered and the rejected ones are non-obvious.

Don't add one for ordinary code changes, refactors, or bug fixes — git history is the right place for those.

## Conventions

- One topic per file. Number sequentially; never renumber.
- Lead with **Status** (`Accepted` / `Superseded by NNNN` / `Deprecated`) and **Context**.
- Always include an **Alternatives considered** section, even if brief. The rejected paths are often more informative than the chosen one.
- Quote concrete file paths and line numbers when explaining. Implementation drifts; references like `worker/internal/executor/sandbox.go` outlive the prose.
