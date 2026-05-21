# Queue tuning analysis (20260520)

## Results summary (e2e latency @ key RPS)

| RPS | v1 (q=100) | B.1 (q=200) | B.2 (q=100 + d²×30) |
|-----|------------|-------------|---------------------|
| 20 | 1.57s | **1.48s** | **1.41s** |
| 25 | 1.80s | 1.71s | **1.64s** |
| 30 | 11.76s | **11.20s** | 12.59s |
| 35 | **24.57s** | 25.41s | 27.77s |
| 40 | **42.23s** | 42.81s | 45.74s |
| 50 | **73.97s** | 75.17s | 80.44s |

## Conclusions

1. **B.2 quadratic rejected** — `queueMsPerDepthSquared: 30` exploded predicted-cost at depth (median ttft diff −12s @ 30 RPS) and **worse** e2e above the knee.
2. **B.1 linear helped at 30 RPS** (−5% vs v1) — stronger queue penalty spreads load before saturation.
3. **B.2 helped only 20–25 RPS** — mild load; not the operating region we need to fix.
4. Hotspot unchanged: ~40% traffic to one pod (`xtxqt`); dq6fq ~7–8%.

## B.3 rerun (queued)

- **Config:** `queueMsPerRequest: 250` (linear only, no quadratic)
- **Hypothesis:** B.1 direction was right; 200 was not enough at the 30 RPS knee.
