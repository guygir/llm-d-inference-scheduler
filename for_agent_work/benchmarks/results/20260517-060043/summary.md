## Weighted MM + Precise Prefix Sweep Summary

This run compares `precise-prefix-cache-scorer` and `weighted-mm-embeddings-cache-scorer`
at 20 RPS. Ratios are written as `precise-prefix : weighted-MM`; for example,
`8:1` means precise prefix weight `8` and weighted MM weight `1`. `only weighted`
and `only precise` are true single-scorer endpoint controls, not zero-weight mixed
profiles.

The plot for this run is `weight_sweep_subplots.png`.

## Interpreting VisionArena vs Earlier Stress Tests

The `visionarena-baseline` result here favored `2:1`, meaning more weight on
precise prefix than weighted MM. This does not contradict the earlier 30 RPS
head-to-head stress result where weighted MM looked better. The two tests are not
the same workload shape:

- This sweep is a 20 RPS ratio-profile run over context-filtered VisionArena
  multi-turn payloads, with both scorers present for the mixed ratios. It measures
  which weight relation works best inside one combined `max-score-picker` profile.
- The earlier 30 RPS stress test was designed to create stronger encoder-cache
  pressure and make weighted MM's routing signal visible under higher load. It was
  closer to a stress comparison of scorer behavior than a calibrated ratio sweep.
- Precise prefix can win in this sweep when token/prompt reuse is already a strong
  routing signal, while weighted MM can win in stress cases where repeated image
  locality dominates and cache state diverges more sharply between pods.

So VisionArena remains the most important real workload in this benchmark set, but
the exact conclusion depends on the VisionArena variant and pressure level. The
safe read is that weighted MM is useful, but the combined profile should not blindly
overweight it for general VisionArena traffic.

## Workloads

- `visionarena-baseline` and `visionarena-repeat` are the most important real-workload
  signals in this sweep. They are derived from VisionArena multi-turn VLM chat data.
  `baseline` uses context-filtered multi-turn conversations; `repeat` adds controlled
  repeated-image pressure to test whether encoder-cache locality can dominate.
  The repeat variant behaved very differently: mixed weighted profiles were extremely
  slow, while `only precise` was best.

- `random-mm-small-mixed` and `random-mm-large-multi` are synthetic controls.
  `small-mixed` uses fewer/smaller images and moderate decode; `large-multi` uses
  multiple larger images per request to stress MM processing and routing pressure.
  The large variant is on a completely different latency scale and had partial
  success only, so it should not be compared directly to the small variant by raw
  latency alone.

- `docvqa-natural-reuse` and `docvqa-multipage` are DocVQA-derived workloads.
  `natural-reuse` repeats questions over the same document/page images and produced
  valid results; `multipage` attempted multiple page images per request but exceeded
  the model context limit, so it is invalid in this run and should be rerun with a
  shorter payload.

- `academic-variety` and `academic-repeat` are synthetic academic/image-variety
  workloads inspired by MMStar/MMMU/LLaVA-style prompts. `variety` emphasizes diverse
  images with limited reuse; `repeat` reuses a subset of images across prompts to
  isolate cache-locality behavior. Both favored higher precise-prefix weighting,
  though the repeat variant was more stable and slightly faster overall.

## Result Table

| Variant | Ratio | Success | Avg | p50 | p75 | p95 | Max |
|---|---:|---:|---:|---:|---:|---:|---:|
| academic-repeat | only weighted | 3600/3600 | 0.797s | 0.568s | 0.821s | 2.068s | 7.243s |
| academic-repeat | 1:8 | 3600/3600 | 0.824s | 0.593s | 0.879s | 2.032s | 8.961s |
| academic-repeat | 1:4 | 3600/3600 | 0.817s | 0.586s | 0.851s | 2.371s | 7.299s |
| academic-repeat | 1:2 | 3599/3600 | 0.776s | 0.567s | 0.833s | 1.876s | 8.822s |
| academic-repeat | 1:1 | 3600/3600 | 0.780s | 0.572s | 0.825s | 2.226s | 7.241s |
| academic-repeat | 2:1 | 3598/3600 | 0.764s | 0.554s | 0.802s | 2.274s | 7.106s |
| academic-repeat | 4:1 | 3600/3600 | 0.771s | 0.551s | 0.802s | 2.397s | 7.700s |
| academic-repeat | 8:1 | 3600/3600 | 0.749s | 0.544s | 0.800s | 1.743s | 7.254s |
| academic-repeat | only precise | 3600/3600 | 0.764s | 0.560s | 0.797s | 1.898s | 6.128s |
| academic-variety | only weighted | 3600/3600 | 0.835s | 0.598s | 0.883s | 2.311s | 8.794s |
| academic-variety | 1:8 | 3600/3600 | 0.956s | 0.693s | 1.082s | 2.496s | 9.742s |
| academic-variety | 1:4 | 3600/3600 | 0.795s | 0.560s | 0.819s | 2.080s | 7.350s |
| academic-variety | 1:2 | 3598/3600 | 0.814s | 0.562s | 0.835s | 2.505s | 8.108s |
| academic-variety | 1:1 | 3599/3600 | 0.724s | 0.536s | 0.755s | 1.725s | 6.506s |
| academic-variety | 2:1 | 3600/3600 | 0.719s | 0.524s | 0.743s | 1.785s | 5.426s |
| academic-variety | 4:1 | 3600/3600 | 0.734s | 0.526s | 0.747s | 1.805s | 7.999s |
| academic-variety | 8:1 | 3600/3600 | 0.722s | 0.534s | 0.749s | 1.700s | 6.315s |
| academic-variety | only precise | 3599/3600 | 0.812s | 0.575s | 0.853s | 2.428s | 8.243s |
| docvqa-multipage | only weighted | 0/0 |  |  |  |  |  |
| docvqa-multipage | 1:8 | 0/3600 |  | 0.000s | 0.000s | 0.000s |  |
| docvqa-multipage | 1:4 | 0/3600 |  | 0.000s | 0.000s | 0.000s |  |
| docvqa-multipage | 1:2 | 0/3600 |  | 0.000s | 0.000s | 0.000s |  |
| docvqa-multipage | 1:1 | 0/3600 |  | 0.000s | 0.000s | 0.000s |  |
| docvqa-multipage | 2:1 | 0/3600 |  | 0.000s | 0.000s | 0.000s |  |
| docvqa-multipage | 4:1 | 0/3600 |  | 0.000s | 0.000s | 0.000s |  |
| docvqa-multipage | 8:1 | 0/3600 |  | 0.000s | 0.000s | 0.000s |  |
| docvqa-multipage | only precise | 0/0 |  |  |  |  |  |
| docvqa-natural-reuse | only weighted | 3600/3600 | 1.392s | 0.937s | 1.524s | 4.378s | 15.592s |
| docvqa-natural-reuse | 1:8 | 3600/3600 | 1.562s | 1.072s | 1.844s | 4.620s | 15.420s |
| docvqa-natural-reuse | 1:4 | 3600/3600 | 1.072s | 0.749s | 1.099s | 3.437s | 15.519s |
| docvqa-natural-reuse | 1:2 | 3600/3600 | 0.921s | 0.677s | 0.932s | 2.448s | 8.630s |
| docvqa-natural-reuse | 1:1 | 3600/3600 | 0.944s | 0.663s | 0.933s | 3.029s | 9.115s |
| docvqa-natural-reuse | 2:1 | 3600/3600 | 0.823s | 0.606s | 0.803s | 2.231s | 7.928s |
| docvqa-natural-reuse | 4:1 | 3600/3600 | 0.856s | 0.616s | 0.828s | 2.751s | 6.379s |
| docvqa-natural-reuse | 8:1 | 3599/3600 | 0.802s | 0.589s | 0.791s | 2.265s | 5.526s |
| docvqa-natural-reuse | only precise | 3600/3600 | 1.164s | 0.781s | 1.173s | 3.901s | 14.587s |
| random-mm-large-multi | only weighted | 2380/3600 | 44.090s | 45.079s | 63.585s | 79.840s | 83.600s |
| random-mm-large-multi | 1:8 | 2382/3600 | 45.761s | 45.557s | 65.187s | 79.285s | 84.868s |
| random-mm-large-multi | 1:4 | 2382/3600 | 40.526s | 39.077s | 56.475s | 74.768s | 81.720s |
| random-mm-large-multi | 1:2 | 2383/3600 | 38.972s | 39.681s | 54.803s | 70.586s | 74.290s |
| random-mm-large-multi | 1:1 | 2383/3600 | 37.795s | 36.610s | 53.215s | 69.786s | 74.638s |
| random-mm-large-multi | 2:1 | 2384/3600 | 41.259s | 42.314s | 58.133s | 73.279s | 78.987s |
| random-mm-large-multi | 4:1 | 2384/3600 | 40.464s | 41.350s | 57.828s | 73.355s | 78.060s |
| random-mm-large-multi | 8:1 | 2383/3600 | 40.694s | 41.110s | 57.806s | 72.911s | 78.961s |
| random-mm-large-multi | only precise | 2383/3600 | 39.462s | 37.708s | 56.971s | 73.020s | 78.665s |
| random-mm-small-mixed | only weighted | 3599/3600 | 1.282s | 1.050s | 1.447s | 2.947s | 7.612s |
| random-mm-small-mixed | 1:8 | 3600/3600 | 1.265s | 1.052s | 1.412s | 2.845s | 6.052s |
| random-mm-small-mixed | 1:4 | 3599/3600 | 1.210s | 0.993s | 1.312s | 2.709s | 9.293s |
| random-mm-small-mixed | 1:2 | 3600/3600 | 1.204s | 0.982s | 1.310s | 2.795s | 7.741s |
| random-mm-small-mixed | 1:1 | 3600/3600 | 1.218s | 1.015s | 1.356s | 2.682s | 5.805s |
| random-mm-small-mixed | 2:1 | 3599/3600 | 1.222s | 1.000s | 1.357s | 2.782s | 8.353s |
| random-mm-small-mixed | 4:1 | 3600/3600 | 1.276s | 1.018s | 1.371s | 3.272s | 10.466s |
| random-mm-small-mixed | 8:1 | 3598/3600 | 1.341s | 1.030s | 1.494s | 3.499s | 7.405s |
| random-mm-small-mixed | only precise | 3599/3600 | 1.226s | 1.006s | 1.360s | 2.764s | 7.612s |
| visionarena-baseline | only weighted | 3600/3600 | 1.883s | 1.494s | 2.488s | 5.102s | 7.768s |
| visionarena-baseline | 1:8 | 3600/3600 | 1.773s | 1.392s | 2.378s | 4.780s | 8.543s |
| visionarena-baseline | 1:4 | 3600/3600 | 2.037s | 1.403s | 2.414s | 7.156s | 12.638s |
| visionarena-baseline | 1:2 | 3600/3600 | 1.708s | 1.336s | 2.300s | 4.968s | 8.430s |
| visionarena-baseline | 1:1 | 3600/3600 | 1.659s | 1.404s | 2.326s | 4.056s | 6.867s |
| visionarena-baseline | 2:1 | 3600/3600 | 1.590s | 1.281s | 2.190s | 4.414s | 7.930s |
| visionarena-baseline | 4:1 | 3599/3600 | 1.723s | 1.322s | 2.254s | 5.045s | 9.542s |
| visionarena-baseline | 8:1 | 3599/3600 | 1.624s | 1.328s | 2.221s | 4.208s | 8.786s |
| visionarena-baseline | only precise | 3600/3600 | 1.711s | 1.418s | 2.365s | 4.195s | 7.687s |
| visionarena-repeat | only weighted | 2981/3600 | 111.457s | 110.745s | 163.431s | 207.689s | 216.560s |
| visionarena-repeat | 1:8 | 2981/3600 | 103.638s | 102.067s | 156.555s | 199.282s | 208.375s |
| visionarena-repeat | 1:4 | 2981/3600 | 103.350s | 101.868s | 155.177s | 200.129s | 209.084s |
| visionarena-repeat | 1:2 | 2980/3600 | 104.321s | 103.414s | 152.682s | 194.008s | 201.346s |
| visionarena-repeat | 1:1 | 2981/3600 | 96.403s | 94.180s | 146.683s | 187.421s | 195.821s |
| visionarena-repeat | 2:1 | 2981/3600 | 107.331s | 106.361s | 159.778s | 202.504s | 211.264s |
| visionarena-repeat | 4:1 | 2981/3600 | 103.546s | 102.793s | 152.029s | 192.346s | 200.241s |
| visionarena-repeat | 8:1 | 2981/3600 | 102.002s | 100.045s | 154.313s | 198.897s | 208.088s |
| visionarena-repeat | only precise | 2981/3600 | 1.846s | 1.199s | 2.149s | 6.872s | 15.456s |
