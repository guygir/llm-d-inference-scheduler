# MM Scorer Weight Profile Benchmark

**Master index (all runs + PNGs):** [SCORER_BENCHMARKING.md](SCORER_BENCHMARKING.md)

## Goal

Find a reliable `max-score-picker` profile for multimodal traffic by comparing
`precise-prefix-cache-scorer` against the multimodal cache scorers:

- `weighted-mm-embeddings-cache-scorer`
- `mm-embeddings-cache-scorer`

Ratios are written as `precise-prefix : MM`. For example, `8:1` means precise
prefix weight `8` and MM scorer weight `1`. Endpoint controls are true
single-scorer profiles: `only weighted`, `only unweighted`, and `only precise`
do not keep another scorer present with weight `0`.

VisionArena-derived workloads are the primary signal. Synthetic families
(RandomMM, DocVQA-derived, Academic-derived) are useful controls but should not
dominate production profile choice.

**Latency-cost scorer (design + “does it know the future?” + timing):**
[LATENCY_COST_SCORER.md](LATENCY_COST_SCORER.md).

## Runs In This Report

| Run ID | MM scorer | RPS | Workloads | Artifact |
|---|---|---:|---|---|
| `20260517-060043` | weighted | 20 | 8 mixed variants | [plot](results/20260517-060043/weight_sweep_subplots.png) |
| `20260517-unweighted-190505` | unweighted | 20 | 8 mixed variants | [plot](results/20260517-unweighted-190505/unweighted_sweep_subplots.png) |
| `20260518-va-weighted-parallel` | weighted | 20 | 8 VisionArena profiles | [plot](results/20260518-va-weighted-parallel/artifacts/visionarena_weighted_sweep_subplots.png) |
| `20260518-va-unweighted-parallel` | unweighted | 20 | 8 VisionArena profiles | [plot](results/20260518-va-unweighted-parallel/artifacts/visionarena_unweighted_sweep_subplots.png) |
| `20260518-va-weighted-40rps` | weighted | 40 | 6 VisionArena profiles | [plot](results/20260518-va-weighted-40rps/artifacts/visionarena_weighted_40rps_subplots.png) |
| `20260518-va-unweighted-40rps` | unweighted | 40 | 6 VisionArena profiles (+ incomplete `va_t6`) | [plot](results/20260518-va-unweighted-40rps/artifacts/visionarena_unweighted_40rps_subplots.png) |
| `20260519-latency-cost-30rps` | latency-cost + baselines | 30 | `va_t4_ordered_mt512` table | [summary](results/20260519-latency-cost-30rps/summary.md) |
| `20260519-va-lc-20rps` | latency-cost vs `1:0`…`0:1` | 20 | 8 VisionArena profiles | [summary](results/20260519-va-lc-20rps/artifacts/summary.md) · [vs weighted](results/20260519-va-lc-20rps/artifacts/visionarena_latency_vs_weighted_20rps_subplots.png) · [vs precise](results/20260519-va-lc-20rps/artifacts/visionarena_latency_vs_precise_20rps_subplots.png) |
| `20260519-va-lc-40rps` | latency-cost vs `1:0`…`0:1` | 40 | 8 VisionArena profiles | [summary](results/20260519-va-lc-40rps/artifacts/summary.md) · [vs weighted](results/20260519-va-lc-40rps/artifacts/visionarena_latency_vs_weighted_40rps_subplots.png) · [vs precise](results/20260519-va-lc-40rps/artifacts/visionarena_latency_vs_precise_40rps_subplots.png) |
| `20260519-sole-scorer-va-t2-rps` | sole scorers × RPS grid | 10–40 | `va_t2_ordered_mt512` | [summary](results/20260519-sole-scorer-va-t2-rps/artifacts/summary.json) · [latency](results/20260519-sole-scorer-va-t2-rps/artifacts/latency_vs_rps.png) · [pods (count)](results/20260519-sole-scorer-va-t2-rps/artifacts/pod_distribution_count.png) · [pods (weighted)](results/20260519-sole-scorer-va-t2-rps/artifacts/pod_distribution_weighted.png) |

Raw tables: each run’s `artifacts/summary.md` and `artifacts/summary.json`.

---

## Mixed-Workload Sweeps (20 RPS)

### Weighted MM + Precise Prefix

![Weighted MM sweep](results/20260517-060043/weight_sweep_subplots.png)

| Workload | Best profile | Avg latency | Notes |
|---|---:|---:|---|
| VisionArena baseline | `2:1` | `1.590s` | Strong real-workload signal. |
| VisionArena repeat | `only precise` | `1.846s` | Mixed profiles were very slow. |
| RandomMM small/mixed | `1:2` | `1.204s` | Synthetic control. |
| RandomMM large/multi | `1:1` | `37.795s` | Synthetic stress; partial success. |
| DocVQA natural reuse | `8:1` | `0.802s` | Synthetic-derived. |
| DocVQA multipage | invalid | n/a | Payload exceeded context in weighted run. |
| Academic variety | `2:1` | `0.719s` | Synthetic-derived. |
| Academic repeat | `8:1` | `0.749s` | Synthetic-derived. |

### Unweighted MM + Precise Prefix

![Unweighted MM sweep](results/20260517-unweighted-190505/unweighted_sweep_subplots.png)

| Workload | Best profile | Avg latency | Notes |
|---|---:|---:|---|
| VisionArena baseline | `only precise` | `1.385s` | Strong real-workload signal. |
| VisionArena repeat | `8:1` | `1.019s` | Precise-heavy mixed profile. |
| RandomMM small/mixed | `only unweighted` | `1.319s` | Avoids token-producer/render. |
| RandomMM large/multi | `only unweighted` | `21.388s` | Synthetic heavy workload. |
| DocVQA natural reuse | `8:1` | `0.802s` | Similar to weighted. |
| DocVQA multipage | `only unweighted` | `0.573s` | Fixed shorter payload. |
| Academic variety | `only unweighted` | `0.575s` | Synthetic; no render path. |
| Academic repeat | `only unweighted` | `0.571s` | Synthetic; no render path. |

---

## VisionArena-Only Sweeps (20 RPS)

Eight realistic VisionArena profiles: varying turns, `max_tokens`, ordered vs
shuffled order.

### Weighted MM + Precise Prefix @ 20 RPS

![VisionArena weighted MM @ 20 RPS](results/20260518-va-weighted-parallel/artifacts/visionarena_weighted_sweep_subplots.png)

| VisionArena profile | Best profile | Avg | p95 |
|---|---:|---:|---:|
| `va_t2_ordered_mt256` | `only precise` | `0.843s` | `1.446s` |
| `va_t2_ordered_mt512` | `only precise` | `1.291s` | `3.029s` |
| `va_t4_ordered_mt256` | `8:1` | `1.003s` | `2.161s` |
| `va_t4_ordered_mt512` | `1:1` | `1.582s` | `4.499s` |
| `va_t4_shuffled_mt256` | `only precise` | `0.844s` | `1.360s` |
| `va_t4_ordered_deep_mt512` | `2:1` | `1.517s` | `3.760s` |
| `va_t6_ordered_mt256` | `2:1` | `1.012s` | `2.482s` |
| `va_t6_ordered_mt512` | `8:1` | `1.754s` | `5.090s` |

Winners across profiles: `only precise` (3), `2:1` (2), `8:1` (2), `1:1` (1).

### Unweighted MM + Precise Prefix @ 20 RPS

![VisionArena unweighted MM @ 20 RPS](results/20260518-va-unweighted-parallel/artifacts/visionarena_unweighted_sweep_subplots.png)

| VisionArena profile | Best profile | Avg | p95 |
|---|---:|---:|---:|
| `va_t2_ordered_mt256` | `only unweighted` | `0.868s` | `1.494s` |
| `va_t2_ordered_mt512` | `only unweighted` | `1.451s` | `3.199s` |
| `va_t4_ordered_mt256` | `only unweighted` | `0.971s` | `1.850s` |
| `va_t4_ordered_mt512` | `2:1` | `1.713s` | `4.124s` |
| `va_t4_shuffled_mt256` | `only unweighted` | `0.839s` | `1.484s` |
| `va_t4_ordered_deep_mt512` | `8:1` | `1.734s` | `3.979s` |
| `va_t6_ordered_mt256` | `8:1` | `1.044s` | `2.017s` |
| `va_t6_ordered_mt512` | `8:1` | `1.748s` | `4.160s` |

Winners across profiles: `only unweighted` (4), `8:1` (3), `2:1` (1).

---

## VisionArena-Only Sweeps (40 RPS)

Same ratio grid at **40 RPS** (180s benchmark window, 20s warmup). Average
latencies move from ~1s to multi-second and tens of seconds under saturation,
which changes which profile wins.

### Weighted MM + Precise Prefix @ 40 RPS

![VisionArena weighted MM @ 40 RPS](results/20260518-va-weighted-40rps/artifacts/visionarena_weighted_40rps_subplots.png)

| VisionArena profile | Best profile | Avg | p95 |
|---|---:|---:|---:|
| `va_t2_ordered_mt256` | `only weighted` | `12.281s` | `19.574s` |
| `va_t2_ordered_mt512` | `8:1` | `36.212s` | `61.964s` |
| `va_t4_ordered_mt256` | `only precise` | `8.192s` | `10.723s` |
| `va_t4_ordered_mt512` | `1:2` | `43.920s` | `78.910s` |
| `va_t4_shuffled_mt256` | `1:1` | `3.934s` | `7.600s` |
| `va_t4_ordered_deep_mt512` | `1:4` | `42.288s` | `77.122s` |

Winners: `only weighted` (1), `8:1` (1), `only precise` (1), `1:2` (1),
`1:1` (1), `1:4` (1). No single ratio dominates under load.

### Unweighted MM + Precise Prefix @ 40 RPS

![VisionArena unweighted MM @ 40 RPS](results/20260518-va-unweighted-40rps/artifacts/visionarena_unweighted_40rps_subplots.png)

| VisionArena profile | Best profile | Avg | p95 |
|---|---:|---:|---:|
| `va_t2_ordered_mt256` | `only unweighted` | `7.396s` | `12.321s` |
| `va_t2_ordered_mt512` | `2:1` | `34.114s` | `56.691s` |
| `va_t4_ordered_mt256` | `2:1` | `2.889s` | `5.888s` |
| `va_t4_ordered_mt512` | `2:1` | `33.754s` | `60.463s` |
| `va_t4_shuffled_mt256` | `4:1` | `2.487s` | `6.730s` |
| `va_t4_ordered_deep_mt512` | `4:1` | `36.484s` | `67.323s` |

Winners: `2:1` (3), `4:1` (2), `only unweighted` (1).

Note: both 40 RPS lanes completed six VisionArena profiles. The unweighted
summary also lists `va_t6_ordered_mt256`, but that profile produced no
successful requests in the 40 RPS run (empty result files).

---

## Interpretation

### 1. There is no universal static ratio

Across weighted and unweighted MM, 20 RPS and 40 RPS, and different VisionArena
shapes, the best profile changes. Prefix-cache and MM-cache scores are not in
comparable units; tuning weights is effectively manual unit conversion.

### 2. VisionArena @ 20 RPS favors precise-heavy routing

For a **combined** profile that includes precise prefix (and therefore
token-producer + `/render`):

- **Weighted MM:** `only precise`, `2:1`, and `8:1` are the robust winners.
- **Unweighted MM:** among mixed profiles, `8:1` and `2:1` win most often;
  `only unweighted` wins many profiles but compares an architecture without
  render/tokenization to mixed profiles that pay that cost.

Conservative production candidates from 20 RPS VisionArena: **`2:1` or `8:1`**
with precise prefix. Keep **`only precise`** as a baseline.

### 3. VisionArena @ 40 RPS is a different regime

At 40 RPS, the system is saturated:

- Average latencies rise roughly **10–40×** vs 20 RPS on the same profiles.
- Best profiles fragment: precise-heavy (`2:1`, `4:1`, `8:1`, `only precise`),
  MM-heavy (`1:1`, `1:2`, `1:4`), and endpoint-only (`only weighted` /
  `only unweighted`) all win on at least one profile.
- This aligns with the earlier **30 RPS stress** observation that weighted MM
  can help when encoder-cache pressure dominates—here under overload, MM signal
  and queue behavior matter more than at 20 RPS.

**Do not** pick a single static ratio from 40 RPS alone; use it to see how
routing preference shifts when TTFT pressure is high.

### 4. `only unweighted` is not a fair routing-only baseline

`mm-embeddings-cache-scorer` can run without `token-producer` and vLLM
`/render`. Precise prefix and weighted MM require rendered/tokenized metadata.
`only unweighted` often wins by avoiding that overhead, not only by better
affinity logic.

### 5. Latency-cost scorer (30 RPS table benchmark)

Isolated stack `mm-scorer-lc-*`, VisionArena payload `va_t4_ordered_mt512`, 30 RPS,
180s duration. Compares static profiles against `latency-cost-scorer` (EPP image
`latency-cost-d26abf26`).

| Scorer | p50 | p75 | p95 | Avg |
|---|---:|---:|---:|---:|
| Precise Prefix | 23.7s | 32.4s | 36.9s | 22.3s |
| Weighted MM 2:1 | 8.5s | 12.6s | 17.2s | 8.8s |
| Unweighted MM | 4.7s | 6.9s | 11.0s | 5.1s |
| **Latency Cost** | **1.2s** | **1.9s** | **2.8s** | **1.3s** |

Full table: [results/20260519-latency-cost-30rps/summary.md](results/20260519-latency-cost-30rps/summary.md).

**Caveats:** `only unweighted` avoids `/render`; latency-cost uses the full
token-producer + precise-prefix data path + predicted-cost scoring. Treat this as
a routing-signal validation under load, not a production SLO claim, until
coefficients are fit from `latency-observer` logs.

### 6. Latency-cost vs static ratios @ 20 RPS (8 VisionArena profiles)

Stack `mm-scorer-lc-*`, profiles: `latency cost`, `1:0`, `4:1`, `1:1`, `1:4`,
`0:1`. Full tables:
[20 RPS summary](results/20260519-va-lc-20rps/artifacts/summary.md).

![Latency-cost vs weighted-MM ratios @ 20 RPS](results/20260519-va-lc-20rps/artifacts/visionarena_latency_vs_weighted_20rps_subplots.png)

**Takeaway @ 20 RPS:** latency-cost is competitive or best on most profiles
(e.g. `va_t2_ordered_mt512` avg **1.07s** vs **1.24s** for `1:0` and **1.17s**
for `0:1`). Static max-score-picker ratios do not dominate uniformly.

### 7. Latency-cost vs static ratios @ 40 RPS (8 VisionArena profiles)

Full tables:
[40 RPS summary](results/20260519-va-lc-40rps/artifacts/summary.md).

![Latency-cost vs weighted-MM ratios @ 40 RPS](results/20260519-va-lc-40rps/artifacts/visionarena_latency_vs_weighted_40rps_subplots.png)

**Takeaway @ 40 RPS:** under saturation, latency-cost is often **orders of
magnitude** faster than static ratios on heavy profiles (e.g.
`va_t2_ordered_mt512`: **42s** avg vs **364s** for `1:0`). On lighter profiles
(e.g. `va_t4_ordered_mt256`) latency-cost stays ~**1.2s** while static ratios are
single-digit to tens of seconds. This supports queue-aware routing rather than
piling onto hot pods via max-score-picker.

**Caveats:** model pods restarted per variant in this sweep (unlike the sole-scorer
RPS grid which resets KV/MM cache via vLLM HTTP only). Compare methodology when
reading absolute numbers.

---

## 8. Sole-scorer RPS grid (`20260519-sole-scorer-va-t2-rps`)

**Setup:** `va_t2_ordered_mt512`, 180s per cell, 4 model pods, EPP image
`latency-cost-d26abf26`. Profiles: `only_precise`, `only_weighted`,
`only_unweighted`, `latency_cost` (sole active scorer each). RPS 10 / 20 / 30 /
40. Between cells: vLLM HTTP cache reset on all model pods (no model redeploy).
Routing census: stream EPP logs during each cell (`Request handled` lines);
coverage gate ≥ 90% vs benchmark HTTP 200.

**Artifacts:** generated on cluster (~228 KiB); copied locally under
`results/20260519-sole-scorer-va-t2-rps/artifacts/`. Full per-cell
`results.jsonl` / `routing_events.txt` remain on the runner (~248 MiB total) —
not copied.

### Mean e2e latency (s)

| Profile | 10 RPS | 20 RPS | 30 RPS | 40 RPS |
|---|---:|---:|---:|---:|
| `only_precise` | 16.8 | 134.2 | 237.6 | 365.1 |
| `only_weighted` | 17.6 | 134.0 | 201.3 | 339.1 |
| `only_unweighted` | 9.3 | 110.5 | 224.9 | 298.0 |
| `latency_cost` | **1.2** | **1.5** | **10.5** | **40.3** |

At 40 RPS, `latency_cost` is ~**9×** faster than `only_precise` on this workload
(same methodology as §7: cache reset only, shared pod fleet).

### Pod routing (% of EPP `Request handled`, count-based)

Short pod suffixes: `585zx`, `dq6fq`, `xc694`, `xtxqt`.

| Profile | 10 RPS | 20 RPS | 30 RPS | 40 RPS |
|---|---|---|---|---|
| `only_precise` | ~24–26% each | ~24–26% each | ~24–26% each | ~24–26% each |
| `only_weighted` | ~23–28% each | ~24–28% each | ~23–26% each | ~24–26% each |
| `only_unweighted` | ~24–26% each | ~24–26% each | ~23–26% each | ~24–26% each |
| `latency_cost` | xtxqt **35%**, dq6fq **11%** | xtxqt **37%**, dq6fq **10%** | xtxqt **40%**, dq6fq **8%** | xtxqt **41%**, dq6fq **8%** |

`latency_cost` keeps `585zx` / `xc694` near ~25–27% but **shifts load off `dq6fq`
onto `xtxqt` as RPS rises**. Static scorers stay near a fair four-way split.

**Why the skew is real (not a parse bug):** routing files differ by profile (no
shared `x-request-id` between runs). Coverage is ~100–101% for `only_precise` and
`latency_cost` at 40 RPS (7220 routed / 7200 HTTP 200). The pattern matches the
scorer: predicted TTFT cost = uncached text + uncached MM + **50 ms × queue
depth** (`queueMsPerRequest` default), then **lower cost → higher score** for
`max-score-picker`. Under load, pods that look cheaper get more traffic, which
keeps their queues shorter — a stabilizing feedback loop, but not an even split.
`only_precise` optimizes cache affinity, not queue depth, so it spreads requests
more uniformly when pods are symmetric.

**Not odd, but worth validating:** pull a sample of EPP trace lines for
`latency-cost scorer endpoint cost` on `dq6fq` vs `xtxqt` at 40 RPS and confirm
`dq6fq` consistently has higher `predictedTTFTMs` (queue and/or cache signals).
Coincidence: `only_precise` and `latency_cost` @ 40 RPS both route **1880**
requests to `585zx` with **zero** request-id overlap — same count, different
requests.

**Latency-weighted pod chart:** `pod_distribution_weighted.png` currently matches
the count chart because EPP `x-request-id` (UUID) does not join to benchmark
integer `request_id` (`unmatched_request_ids` = all rows). Fix if we add a
shared id in headers.

**Coverage caveats:** `only_weighted` / `only_unweighted` at 20–40 RPS show
>100% routed/HTTP-200 (extra EPP lines vs benchmark counter); count % still
usable but treat those cells as noisier.

Plots: [latency vs RPS](results/20260519-sole-scorer-va-t2-rps/artifacts/latency_vs_rps.png),
[pod count %](results/20260519-sole-scorer-va-t2-rps/artifacts/pod_distribution_count.png).

---

## Recommended Next Steps

1. **Production-ish default (20 RPS evidence):** try `2:1` or `8:1` precise:MM
   (weighted or unweighted MM depending on which scorer ships), with `only
   precise` as fallback baseline.
2. **High-load behavior (40 RPS evidence):** prefer **latency-cost** (or
   coefficients tuned from `latency-observer` logs) over new static ratios.
3. **Sole-scorer follow-up:** log-dive `dq6fq` vs `xtxqt` predicted costs at 40
   RPS; optionally tune `queueMsPerRequest` if skew is too aggressive for fairness
   SLOs.
