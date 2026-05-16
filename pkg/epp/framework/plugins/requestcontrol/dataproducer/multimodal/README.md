# Multimodal Embeddings Cache Producer Plugin

**Types:** `mm-embeddings-cache-producer`, `weighted-mm-embeddings-cache-producer`

Produces multimodal embeddings cache match data for downstream scheduling plugins.

## What It Does

For each request, the producer extracts stable multimodal item hashes from:

- `TokenizedPrompt.MultiModalFeatures`, when a `token-producer` is configured
- typed OpenAI chat-completions structured media blocks, as a lightweight fallback

It keeps an in-memory LRU map from multimodal hash to the set of pods that recently
handled that item. During scheduling, it attaches `EncoderCacheMatchInfo` to each
endpoint so scorers can prefer pods that are likely to have already processed the
same image, video, or audio input.

Repeated references to the same multimodal hash within one request count once.
The unweighted producer gives each unique item size `1`. The weighted producer
uses the `TokenizedPrompt.MultiModalFeatures` placeholder length for each item,
with a fallback size of `1`.

## Inputs Consumed

The unweighted producer does not declare required request data. It can use
`TokenizedPrompt.MultiModalFeatures` when another plugin already produced it, but
it remains usable without token-producer by falling back to typed structured media
blocks.

The weighted producer declares:

- `TokenizedPrompt`

This orders tokenization before weighted multimodal match data production so the
producer can use vLLM-rendered placeholder lengths from #890 metadata.

## Data Produced

This plugin produces:

- `MultiModalEncoderCacheMatchInfoKey` (`EncoderCacheMatchInfo`)
- `WeightedMultiModalEncoderCacheMatchInfoKey` (`EncoderCacheMatchInfo`) for the
  weighted producer

## Configuration

The producer supports the following runtime parameters:

- `cacheSize` (integer, default: `10000`): maximum number of multimodal hash entries
  retained in the best-effort pod-affinity cache.

**Configuration Example:**

```yaml
plugins:
  - type: token-producer
    parameters:
      modelName: Qwen/Qwen2.5-1.5B-Instruct
      vllm:
        http: http://localhost:8000
  - type: mm-embeddings-cache-producer
    parameters:
      cacheSize: 10000
  - type: mm-embeddings-cache-scorer
schedulingProfiles:
  - name: decode
    plugins:
      - pluginRef: mm-embeddings-cache-scorer
        weight: 4
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
schedulingProfiles:
  - name: decode
    plugins:
      - pluginRef: weighted-mm-embeddings-cache-scorer
        weight: 4
```

## Operational Notes

- The cache is a best-effort routing signal, not a correctness dependency.
- Endpoint delete events can remove stale pod entries when `endpoint-notification-source`
  is wired through `dataLayer`.
- The unweighted producer remains tokenizer-free for request shapes where typed
  media blocks are sufficient.
- The weighted producer requires `token-producer` because it relies on upstream
  multimodal metadata for exact placeholder lengths.
