# Multimodal Embeddings Cache Scorer Plugin

**Types:** `mm-embeddings-cache-scorer`, `weighted-mm-embeddings-cache-scorer`

Scores candidate endpoints using multimodal embeddings cache match data produced
by `mm-embeddings-cache-producer` or `weighted-mm-embeddings-cache-producer`.

## What It Does

For each candidate endpoint, the scorer reads `EncoderCacheMatchInfo` and computes:

```text
score = matchedItemSize / totalRequestItemSize
```

For the unweighted producer path, every unique multimodal item has size
`1`, so the score is the fraction of unique request multimodal hashes that are
likely cached on the endpoint.

For the weighted producer path, item size comes from the vLLM-rendered multimodal
placeholder length in `TokenizedPrompt.MultiModalFeatures`, so larger MM items can
contribute more strongly to endpoint affinity.

This produces a normalized score in the range `[0, 1]`:

- higher score: more request multimodal content is expected to reuse endpoint-local
  embeddings cache
- lower score: less multimodal cache reuse is expected

If the attribute is missing, has the wrong type, or total request item size is zero,
the endpoint receives score `0`.

## Inputs Consumed

This scorer consumes:

- `MultiModalEncoderCacheMatchInfoKey` (`EncoderCacheMatchInfo`)
- `WeightedMultiModalEncoderCacheMatchInfoKey` (`EncoderCacheMatchInfo`) for the
  weighted scorer

The attribute is produced by the matching producer before scheduling.

## Configuration

This plugin does not define any plugin-specific parameters.

**Configuration Example:**

```yaml
plugins:
  - type: mm-embeddings-cache-producer
    parameters:
      cacheSize: 10000
  - type: mm-embeddings-cache-scorer
  - type: max-score-picker
schedulingProfiles:
  - name: decode
    plugins:
      - pluginRef: mm-embeddings-cache-scorer
        weight: 1
      - pluginRef: max-score-picker
```

**Weighted Configuration Example:**

```yaml
plugins:
  - type: token-producer
    parameters:
      modelName: Qwen/Qwen2.5-1.5B-Instruct
      vllm:
        http: http://localhost:8000
  - type: weighted-mm-embeddings-cache-producer
    parameters:
      cacheSize: 10000
  - type: weighted-mm-embeddings-cache-scorer
  - type: max-score-picker
schedulingProfiles:
  - name: decode
    plugins:
      - pluginRef: weighted-mm-embeddings-cache-scorer
        weight: 1
      - pluginRef: max-score-picker
```

## Operational Notes

- The scorer does not hash request media and does not maintain cache state.
- It only converts producer-generated match data into endpoint scores.
- KV-prefix cache affinity remains owned by `precise-prefix-cache-scorer`.
