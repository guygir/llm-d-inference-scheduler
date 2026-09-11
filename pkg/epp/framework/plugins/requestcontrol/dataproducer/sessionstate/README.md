# Session State Producer

The `session-state-producer` tracks history for raw identities published by the
`agent-identity` request-header plugin, or for scoped identities published by a
named `session-manager`, and publishes per-request session state for scheduling
plugins through the request attribute store.

By default the producer uses the `agent-identity` attribute. When
`sessionIdentityProducer` is set, it instead requires that producer's
`SessionIdentity` and keys every lifecycle hook by `SessionTag`; it never falls
back to raw identity per request. `SessionIDDataKey` remains a separate
attribute produced by `session-id-producer`.

## Configuration

Both plugins must be enabled, and Alpha plugins must be allowed by the EPP
process. `agent-identity` is a required data dependency, so configuration
loading fails if no plugin declares that it produces the agent identity
attribute. Manager mode additionally requires
`--allow-experimental-plugins=true`.

```yaml
plugins:
- type: agent-identity
- type: session-state-producer
  parameters:
    evictionTtlSeconds: 3600
    evictionSweepSeconds: 300
```

To key state by the manager's scoped identity:

```yaml
plugins:
- type: agent-identity
- type: session-manager
  name: sessions
  parameters:
    deploymentID: payments-prod-eu1
    hmacKeyFile: /var/run/secrets/llm-d/session-manager-key
- type: session-state-producer
  parameters:
    sessionIdentityProducer: sessions
```

| Parameter | Default | Description |
|---|---:|---|
| `evictionTtlSeconds` | `3600` | Maximum session idle time before its state is removed. Set to `0` to disable eviction. |
| `evictionSweepSeconds` | `300` | Interval between idle-state scans. Must be greater than `0`. |
| `sessionIdentityProducer` | unset | Optional named producer of scoped `SessionIdentity`. Unset preserves raw `agent-identity` behavior. |

## Produced data

The producer writes `SessionStateDataKey` with a `sessionstate.SessionState`
value:

- `TurnsTaken`: requests dispatched before the current request.
- `Duration`: elapsed time since the session was first observed.
- `LastSeenAt`: time the preceding request was observed; for a new session it
  is the current request time.
- `InFlightRequests`: requests dispatched by EPP whose response lifecycle has
  not ended.
- `CompletedRequests`: requests whose responses ended naturally. A complete
  model error response is a natural completion.
- `TotalInputTokens`: prompt tokens reported by naturally completed responses.
- `TotalOutputTokens`: completion tokens reported by naturally completed
  responses.

The current request is marked as seen during data production. Its turn is
counted in `PreRequest` only after scheduling selects at least one target, so
the next request observes the increment. Multiple scheduling profiles still
count as one turn. The request is also added to `InFlightRequests` at that
point and removed when its response lifecycle ends. Client disconnects,
evictions, and internal errors do not contribute to `CompletedRequests` or the
token totals because their usage may be incomplete. A naturally completed
response without usage data contributes zero tokens.

State is local to one EPP replica. Idle session state is removed according to
the configured eviction TTL and sweep interval. Sessions with in-flight
requests are retained until those requests end.
