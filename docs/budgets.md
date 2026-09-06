# Budgets (measured, not invented)

Derived from a throwaway parser over this machine's Claude Code transcripts in `~/.claude/projects/` (85 transcripts, 84 tool-bearing streams, 76 of them subagent streams). The parser is disposable; these numbers are the artifact. Re-measure before revisiting the TTL default or the hot-path decision.

## Results

| Quantity | Source | Value |
|---|---|---|
| Edit calls/min, p50 and max | edit tool calls per stream | p50 ≈ 0.02/min (bursty); per-stream max ≈ 1.0/min; **busiest single minute = 8 edits** |
| All tool calls/min | all tool calls per stream | p50 1.30/min, p99 25.7/min; **busiest single minute = 28 calls** |
| Inter-tool-call gap, active work (<600s) | consecutive tool-call timestamps, within a work burst | p50 **7.6s**, p90 78.8s, p99 **402.8s**, p99.9 **567.2s** |
| Inter-tool-call gap, raw | same, unfiltered | tail is abandonment, not think-time (p99 ≈ 3.1h, max ≈ 10.8 days) — **discarded** |
| Median turn duration | human prompt → last act before next human prompt | **≈ 896s (~15 min)** |
| Hook overhead ceiling | process-spawn microbenchmark, 30 runs each | compiled/native **10.7ms** p50 (14ms p90); python `-c pass` 24ms; node `-e ''` 36ms |
| Live intent keyspace | max concurrent subagents × 2 records × bytes | 6 concurrent × 2 × ~1KB ≈ **12KB**; ~120KB at 10× headroom |

## What the numbers decide

- **TTL default = 600s (10 min).** Active-work gaps reach p99.9 at 567s, so a 600s silence window covers 99.9% of legitimate pauses while promptly clearing abandoned records. The raw tail (hours to days) is laptops left open, not agents thinking, and is excluded.
- **Hook client must be a compiled binary.** At the 11ms compiled figure the per-hook cost is ~0.5% of wall time even at the 28-calls/min peak; a python (24ms) or node (36ms) client would multiply that for no benefit.
- **The datastore is not the bottleneck.** Local Dragonfly round-trips are sub-millisecond against an 11ms spawn — process spawn dominates, as the design anticipated. Recording every tool call (not just edits) costs ≈ 28 × 11ms ≈ 0.3s/min ≈ 0.5% at peak, so the edit-only default is a cleanliness choice, not a cost one.
- **Dragonfly is not justified by scale.** The live keyspace is kilobytes. The store is chosen for access shape; the README must say so.

## Method notes

- A "stream" is one transcript file: a main session or a single subagent. Tool calls are `tool_use` blocks in `assistant` messages; edit tools are `Edit`, `Write`, `MultiEdit`, `NotebookEdit`, `Update`.
- Human turns are `user` messages whose content is not a `tool_result` (tool returns are excluded so they don't count as prompts).
- Active-regime gaps filter to < 600s to separate think-time from session abandonment; the split is clearly bimodal.
- Concurrency is the max overlap of subagent-stream `[first-call, last-call]` intervals.
