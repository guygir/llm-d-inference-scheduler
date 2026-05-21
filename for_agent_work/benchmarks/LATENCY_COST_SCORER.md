# Latency-cost scorer

How the **latency-cost-scorer** estimates per-endpoint TTFT cost at scheduling time, how it plugs into the EPP, and what the cluster benchmarks show. Implementation lives in `llm-d-inference-scheduler` (`pkg/epp/framework/plugins/scheduling/scorer/latencycost`).

Related benchmark write-ups: [MM_SCORER_WEIGHT_PROFILE_BENCHMARK.md](MM_SCORER_WEIGHT_PROFILE_BENCHMARK.md) · **[SCORER_BENCHMARKING.md](SCORER_BENCHMARKING.md)** (all campaigns + figures).

---

## What problem it solves

Static `max-score-picker` profiles (e.g. `only_precise`, `8:1` precise:MM) rank endpoints by **cache affinity**. Under load, that can send many requests to the same “best cache” pod while its queue grows, which hurts TTFT.

**Latency-cost** keeps the same picker but changes the score: estimate **time-to-first-token (TTFT) cost** from (1) how much of this request would miss cache on each pod, plus (2) how backed up each pod is **right now**. Pick the endpoint with **lowest** predicted cost (mapped to highest score).

---

## Does it “know the future”?

**No.** The scorer does not read the benchmark JSONL ahead of time, does not see later requests in the trace, and does not use realized latency from the current request before routing it.

At **each** scheduling decision it only uses:

| Signal | Source | When available |
|---|---|---|
| Prompt size | Current `InferenceRequest` body (tokenized prompt or token hints) | Request arrival |
| Prefix cache match | `precise-prefix-cache-scorer` / prefix indexer for **this** request on **each** endpoint | Pre-schedule (same as static precise routing) |
| MM cache match | `mm-embeddings-cache-producer` for **this** request on **each** endpoint | Pre-schedule |
| Queue depth | `metrics-data-source` → `core-metrics-extractor` (`WaitingQueueSize`, `RunningRequestsSize`) | Snapshot at schedule time |

The benchmark **does** fix an input file (`va_t2_ordered_mt512.jsonl`) and reply order for repeatability. That is a test harness choice, not an input to the scorer. In production, request order is whatever clients send; the scorer still only sees the current request plus live endpoint state.

**`latency-observer`** (bundled in the benchmark EPP config) is separate: it passively records TTFT after responses and emits **structured log lines only**. It does **not** write back to the data layer, does **not** change coefficients, and does **not** affect the next request’s score in the current code. “Calibration” means a human or offline job reads logs and retunes `textMsPerUncachedToken` / `queueMsPerRequest` by hand — there is no closed-loop learner yet.

---

## Cost model (coefficients)

### v1 tuned (current default)

Configured in EPP (`epp-latency-cost.yaml`) after calibration run `20260519-lc-calibration-va-t2-rps`:

```yaml
- type: latency-cost-scorer
  parameters:
    baseMs: 120
    textMsPerUncachedToken: 2
    mmMsPerUncachedUnit: 175
    queueMsPerRequest: 100
    missingDataPolicy: fallback
    fallbackToPrecisePrefix: true
```

### v0 baseline (pre-calibration)

Snapshot: `configs/sole-scorer-benchmark/epp-latency-cost-v0.yaml`

| Parameter | v0 | v1 | Change |
|---|---:|---:|---|
| `baseMs` | 0 | 120 | +120 ms floor per pick (scheduler / proxy overhead not in cache or queue terms) |
| `textMsPerUncachedToken` | 1 | 2 | 2× — text miss cost was too low vs observed TTFT |
| `mmMsPerUncachedUnit` | 100 | 175 | 1.75× — `va_t2_ordered_mt512` is MM-heavy; largest miss component |
| `queueMsPerRequest` | 50 | 100 | 2× — error jumped at 30+ RPS when queues matter; snapshot depth understated wait |

**Why these values:** v0 showed **positive** `observed − predicted` TTFT everywhere (under-prediction). Median gap was ~**+247 ms @ 5 RPS** and ~**+1.43 s @ 30–50 RPS**; mean gap ~**+2.2 s @ 30+ RPS**. Routing still helped (low e2e vs static scorers), but the cost scale was optimistic. v1 raises all terms that move with load (queue, MM) plus a small `baseMs` so predicted costs sit closer to measured TTFT without changing the picker's relative logic drastically.

Re-validation run: `20260520-lc-calibration-tuned` (same RPS grid 5–50). Compare plots: [artifacts/](results/20260519-lc-calibration-va-t2-rps/artifacts/) (`summary_v0_baseline.json` + tuned overlay on PNGs).

**Interpretation:** v1 improves TTFT *prediction error* (see `ttft_diff_vs_rps.png`), not necessarily e2e latency — see [LATENCY_CALIBRATION_NOTES.md](LATENCY_CALIBRATION_NOTES.md). Scorer code and benchmarks live on git branch `latency-cost-scorer`; PR #901 work stays on `mm_scorer`.

**Predicted TTFT (ms)** for one endpoint:

```
cost = baseMs
     + uncached_text_tokens × textMsPerUncachedToken
     + uncached_mm_units      × mmMsPerUncachedUnit
     + (waiting + running)    × queueMsPerRequest
```

Where:

- `uncached_text_tokens = max(prefix_total_tokens - prefix_matched_tokens, 0)` (or full prompt if prefix data missing, per `missingDataPolicy`).
- `uncached_mm_units = max(mm_total_units - mm_matched_units, 0)` when MM data exists.
- `waiting + running` = endpoint queue metrics at pick time.

**Score for `max-score-picker`:** among the four endpoints, normalize so lowest cost → score `1`, highest → `0` (linear inverse). Ties → all score `1`.

This is a **heuristic**, not a learned model. Coefficients are hand-set (`DefaultConfig` in code). `latency-observer` logs `ttftMs` plus the same feature fields the scorer saw at dispatch time so you can compare **predicted** (`predictedTTFTMs` in scorer trace logs) vs **observed** offline — not online.

---

## EPP plugin graph (benchmark config)

```
Request → token-producer
       → datalayer: metrics + MM producer + precise-prefix indexer/scorer
       → scheduling profile "default":
            decode-filter
            latency-cost-scorer (weight 1)   ← only this scorer active in sole-scorer runs
            max-score-picker
```

Static baselines swap in `precise-prefix-cache-scorer`, `weighted-mm-embeddings-cache-scorer`, or `mm-embeddings-cache-scorer` with weight `1` instead.

**Image:** `mm-scorer-epp:latency-cost-d26abf26` for latency-cost cells; weight-sweep image for static sole-scorer cells.

---

## Why routing skew is expected (not oracle)

Under rising RPS, pods with lower predicted cost receive more traffic → shorter queues → stay cheaper. That feedback is intentional load balancing, not clairvoyance.

Example @ 40 RPS on sole-scorer grid (`va_t2_ordered_mt512`):

| Pod suffix | `only_precise` | `latency_cost` |
|---|---:|---:|
| dq6fq | ~25% | **~8%** |
| xtxqt | ~24% | **~41%** |
| 585zx / xc694 | ~25% each | ~25–26% each |

Static precise spreads evenly when pods are symmetric; latency-cost shifts load off persistently “expensive” endpoints (here `dq6fq`) onto “cheap” ones (`xtxqt`).

---

## Benchmark methodology: sole-scorer vs VA sweeps

| Aspect | Sole-scorer RPS grid (`20260519-sole-scorer-va-t2-rps`) | VA latency-cost sweeps (`20260519-va-lc-*`) |
|---|---|---|
| Model pods | **No redeploy** — HTTP cache reset between cells | **Model deployment restart per VisionArena variant** |
| EPP | ConfigMap patch + EPP rollout per profile (4×) | ConfigMap + EPP per profile × variant |
| Cells | 4 profiles × 4 RPS = 16 | 8 VA profiles × 9 scheduling profiles × RPS |
| Load window | 180s target + 20s warmup per cell | Same |

Skipping model redeploy saves **many minutes per variant** (model rollout waits up to 900s in the VA sweep script) and keeps the same four pods across cells so queue/cache signals are comparable.

---

## How long did the sole-scorer sweep take?

**Wall clock (cluster):** `2026-05-19T15:10:41Z` → `2026-05-19T17:24:51Z` ≈ **2 h 14 min** (`SOLE_SCORER_SWEEP_START` / `DONE` in `sole-scorer-rps-batch.log`).

**Per-cell wall time (benchmark start → done, includes routing log capture):**

| Profile | 10 RPS | 20 RPS | 30 RPS | 40 RPS |
|---|---:|---:|---:|---:|
| `latency_cost` | ~3.4 min | ~3.4 min | ~3.8 min | ~4.6 min |
| `only_precise` | (skipped) | ~9 min | ~12 min | ~16 min |
| `only_weighted` | ~4.5 min | ~8.7 min | ~11 min | ~15 min |
| `only_unweighted` | ~4.4 min | ~8 min | ~11.5 min | ~14 min |

Configured send window is **180s + 20s warmup**, but the benchmark process **waits for in-flight requests** (timeout 3600s). Saturated static profiles stretch cells to 9–16 minutes because queues are long. **Latency-cost cells finish near the nominal window** because routing avoids the hottest queues.

**EPP-only setup overhead:** ~47–80s per profile (config patch + rollout + optional render warmup) × 4 profiles ≈ **5–6 min** total — small vs wall clock.

**Was the sweep faster because of no redeploy?** Yes. The sweep script never restarts `mm-scorer-lc-model`; it only HTTP-resets caches on running pod IPs. The VA LC sweep restarts the model deployment **once per VisionArena variant** before profiling. For 8 variants that alone can add **tens of minutes to hours** of rollout time on top of benchmark cells. The sole-scorer grid traded that away to compare scorers on a **fixed pod fleet** with controlled cache state.

---

## Results snapshot (`va_t2_ordered_mt512`, mean e2e latency)

| Profile | 10 RPS | 20 RPS | 30 RPS | 40 RPS |
|---|---:|---:|---:|---:|
| `only_precise` | 16.8s | 134.2s | 237.6s | 365.1s |
| `only_weighted` | 17.6s | 134.0s | 201.3s | 339.1s |
| `only_unweighted` | 9.3s | 110.5s | 224.9s | 298.0s |
| **`latency_cost`** | **1.2s** | **1.5s** | **10.5s** | **40.3s** |

Artifacts (~228 KiB): [results/20260519-sole-scorer-va-t2-rps/artifacts/](results/20260519-sole-scorer-va-t2-rps/artifacts/).

---

## Prediction accuracy in the sole-scorer benchmark?

**Not checked.** The sweep captured EPP lines containing `Request handled` for pod routing only. It did **not** archive `latency observer ttft observation` or `latency-cost scorer endpoint cost` lines, and there is no post-run script joining predicted vs observed TTFT.

What we can say without that join:

- **Directionally successful:** `latency_cost` mean e2e latency at 40 RPS (~40s) vs `only_precise` (~365s) shows the heuristic routing helped under load even without observer feedback.
- **Not verified:** whether `predictedTTFTMs` was numerically close to measured `ttftMs` per request or per pod.

To check retroactively (if EPP logs are still retained on cluster): grep `latency observer ttft observation` and `latency-cost scorer endpoint cost` (scorer line is at **trace** verbosity in code) and join on `requestID`. Re-run with explicit log capture if logs have rotated.

---

## Limitations and follow-ups

1. **Heuristic coefficients** — not trained; tune with exported `latency-observer` logs or offline regression (manual step today).
2. **Queue metric lag** — metrics are as fresh as the data layer; very fast bursts may be slightly stale.
3. **Skew vs fairness** — aggressive `queueMsPerRequest` can starve an endpoint that the model consistently marks expensive; validate with cost logs (`predictedTTFTMs` in EPP trace).
4. **Weighted pod charts** — need a shared id between benchmark `request_id` and EPP `x-request-id` to weight by measured latency.
5. **Compare runs carefully** — absolute seconds differ when methodology differs (cache-reset-only vs model restart per variant).

---

## Code references

| Piece | Path |
|---|---|
| Scorer | `llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/latencycost/plugin.go` |
| Feature vector | `.../datalayer/attribute/latencycost/data_types.go` |
| Passive observer | `.../requestcontrol/latencyobserver/plugin.go` |
| Benchmark EPP config | `IGNORED/scorer-weight-sweep/configs/sole-scorer-benchmark/epp-latency-cost.yaml` |
| Sole-scorer orchestrator | `IGNORED/scorer-weight-sweep/scripts/cluster_sole_scorer_rps_sweep.py` |
