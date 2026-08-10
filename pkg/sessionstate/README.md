# Session state

`sessionstate` is the bounded logical-state layer for session-centric routing. It stores no prompt
payloads and does not claim engine KV truth.

## Phase 1 contract

- Nodes are immutable SHA-256 lineage coordinates scoped by `TenantScope`.
- Aliases map typed external IDs to nodes.
- Residencies are short-lived endpoint estimates with a generation captured when the endpoint is
  selected.
- Coverage examines only explicit candidate endpoints and returns typed covered/requested extents
  separately for each endpoint/tier.
- String coordinates, endpoint registrations, candidate count, ancestry depth, and total coverage
  work are independently bounded.
- Maximal-tip compaction removes an ancestor only when a descendant carries at least its extent with
  equal-or-stronger evidence.
- Missing ancestry, stale endpoint generations, incompatible units, or exhausted work budgets fail
  cold; callers must publish zero affinity rather than infer coverage.

The default bounds are independent 100,000-entry node, alias, and residency budgets, 32 tips per
endpoint/scope across tiers, 8,192 nodes per ancestry, and 1,000,000 traversal steps per coverage
call. Nodes are removed leaf-first so retained ancestry never dangles. Expiry uses an indexed heap
and cooperative cleanup batches.

Every residency row has store-assigned `ObservedAt` and `ExpiresAt` timestamps. Expiry removes that
one node/endpoint/tier estimate; it does not unregister the endpoint generation. Endpoint lifecycle
delete/replacement is the separate operation that purges all estimates for the endpoint.

Capacities are global soft-affinity budgets, not per-tenant reservations. A noisy tenant can make
another tenant cold through eviction, but tenant scopes are never compared. Per-tenant fair-share
quotas remain a benchmark and operator-policy decision.
