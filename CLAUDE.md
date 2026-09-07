# CLAUDE.md — concord

concord is a standalone Go service, backed by Dragonfly, that coordinates concurrent coding agents. Two independent layers: a **mandatory blocking version check** that stops stale edits (correctness), and an **advisory intent registry** that reduces duplicated work (never blocks). It runs alongside a self-hosted Hindsight instance without coupling to it.

**Status:** implemented (tickets T01–T12 complete) and under review. `SPEC.md` remains the source of truth; the daemon, `concord-hook` client, store, and RPC-seam + stress tests all exist.

## Read these first

- `SPEC.md` — the full specification: problem, solution, user stories, implementation and testing decisions, scope.
- `CONTEXT.md` — the domain glossary. **Use this vocabulary exactly.** Notably: *actor*, *holder*, *read-hash*, *version check*, *intent registry*, *predicted/actual footprint*, *touch*, *orchestrator*, *verification pass*. Do not reintroduce rejected words (lease, lock, claim, conflict).
- `docs/adr/` — the decisions and why. Respect them; don't silently reverse one.
- `docs/budgets.md` — measured performance budgets that pin the TTL and the hook-client choice.

## Non-negotiables (see ADRs before challenging)

- **Correctness is the content-hash version check, and nothing else.** No leases, TTLs, fencing, liveness detection, or reaper (ADR-0002). It must work against writers that call no hook — the human's own editor, shell commands.
- **The intent registry never blocks a tool call.** It only informs (ADR-0001). A stale intent record may cost efficiency, never correctness.
- **The two layers stay separate.** One mechanism cannot both deny and inform without false positives (ADR-0001).
- **No embeddings, no similarity threshold.** Semantic intent overlap is judged by the querying model; path overlap is a literal token intersection (ADR-0006).
- **concord informs; the orchestrator verifies.** Safe-scope dedup depends on the orchestrator running the repo's compiler+tests on the merged result. That verification pass is a precondition, not a concord feature.

## The guarantee (quote it honestly)

No edit-tool call proceeds when the target file's current content hash differs from the hash the acting holder recorded at its last read. Out-of-band writes (shell, editor) are reconciled best-effort — `CheckEdit` compares against the live on-disk hash, and a `PostToolUse` git-status sweep advances the writing actor's own read-hash; a write no concord hook observes is outside the guarantee.

## Building it

- **Language:** Go. **Store:** Dragonfly (for access shape, not scale — say so).
- **Shape:** one long-lived local daemon; each hook spawns a thin **compiled** client making one localhost RPC (ADR-0007). The compiled client matters — python/node startup blows the latency budget (`docs/budgets.md`).
- **One test seam:** the RPC surface. Drive all tests through it against an ephemeral Dragonfly. Never test internal key layout. Work **test-first (TDD)**.
- **Bash-hole mechanism (resolved, T02 spike):** `FileChanged` was rejected (async, can race the next check). Reconciliation uses live-hashing at `CheckEdit` + a synchronous `PostToolUse` git-status sweep. Shell writes in non-git dirs stay outside the guarantee.

## Working agreement

- Prefer skills when they apply (brainstorming before building, TDD before implementing, systematic-debugging before fixing).
- When you change the domain model, update `CONTEXT.md` inline. When you make a hard-to-reverse, surprising, real-trade-off decision, add an ADR.
- Don't add anything under "Out of scope" in `SPEC.md` without a decision to reverse that scope line.
