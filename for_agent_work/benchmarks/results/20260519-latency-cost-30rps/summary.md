# Latency-Cost Table Benchmark @ 30 RPS

Workload: `va_t4_ordered_mt512` (VisionArena filtered payload).

| Scorer | Success | Actual RPS | p50 | p75 | p95 | Max | Avg |
|---|---:|---:|---:|---:|---:|---:|---:|
| Precise Prefix | 5400/5400 | 0.00 | 23.724s | 32.356s | 36.910s | 40.769s | 22.267s |
| Weighted MM 2:1 | 5400/5400 | 0.00 | 8.544s | 12.566s | 17.192s | 21.546s | 8.778s |
| Unweighted MM | 5399/5400 | 0.00 | 4.744s | 6.916s | 10.954s | 16.255s | 5.062s |
| Latency Cost | 5399/5400 | 0.00 | 1.180s | 1.895s | 2.752s | 4.283s | 1.298s |

## Takeaway

Best average latency: **Latency Cost** (1.298s). Compare against the earlier 30 RPS stress table in `IGNORED/WEIGHTED_MM_SCORER_PR_NOTES.md`.
