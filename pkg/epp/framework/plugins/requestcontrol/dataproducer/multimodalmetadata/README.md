# Multimodal Metadata Producer

`multimodal-metadata-data-producer` is an opt-in producer for weighted multimodal encoder-cache affinity. It extracts multimodal request items, sends one batched UDS request to the tokenizer sidecar, and attaches request-scoped metadata containing `mm_hash`, placeholder count, dimensions, source, and exactness flags.

The existing `multimodal-encoder-cache-data-producer` remains the owner of `mm_hash -> set[pod]` placement history. Configure it with `weightSource: placeholder-count` to consume this metadata and keep `mm-cache-affinity-scorer` unchanged.

Example:

```yaml
- type: multimodal-metadata-data-producer
  parameters:
    modelName: Qwen/Qwen2-VL-2B-Instruct
    udsTokenizerConfig:
      socketFile: /tmp/tokenizer/tokenizer-uds.socket
    hashMode: vllm
    metadataTimeout: 150ms
    allowPreprocessFallback: false
    allowNonExactHashFallback: false
- type: multimodal-encoder-cache-data-producer
  parameters:
    cacheSize: 10000
    weightSource: placeholder-count
    requireExactMetadata: true
    fallbackWeight: unit
```

Defaults are conservative: weighted mode is opt-in, exact metadata is required by the cache producer, unsupported metadata falls back to unit weight, and full preprocessing fallback is disabled. This preserves PR #1 behavior unless the metadata producer and placeholder weighting are both configured.

The sidecar marks non-exact hashes and unsupported placeholder counters explicitly. Weighted scoring should only depend on placeholder counts when both the hash and count are exact, unless the profile intentionally opts into approximate behavior.
