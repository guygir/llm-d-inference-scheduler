# Session state store

`session-state-store` is an Alpha, opt-in provider for bounded logical session lineage, typed aliases,
and short-lived endpoint residency estimates.

It is disabled unless configured and requires `--allow-experimental-plugins`. Configure exactly one
provider:

```yaml
plugins:
  - type: session-state-store
    name: central-session-state
    parameters:
      nodeCapacity: 100000
      aliasCapacity: 100000
      residencyCapacity: 100000
      maxTipsPerEndpoint: 32
      maxAncestryDepth: 8192
      maxCoverageSteps: 1000000
      nodeTTL: 1h
      aliasTTL: 1h
      estimateTTL: 2m
      cleanupInterval: 1m
      endpointReconcileInterval: 2m
```

Later session-aware producers refer to the configured name with `storePluginRef`. The provider stores
no request payloads and exposes only aggregate cardinalities through metrics and debug state.
