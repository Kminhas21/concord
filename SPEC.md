# SPEC: concord

> A standalone Go service, backed by Dragonfly, that coordinates concurrent coding agents through two independent layers: a mandatory version check that blocks stale edits, and an advisory intent registry that reduces duplicated work. Runs alongside a self-hosted Hindsight instance without being coupled to it.

Terms in this document are defined in `CONTEXT.md`. Decisions are recorded in `docs/adr/`.

---

## Problem Statement

When several coding agents work the same repository at once — subagents under one orchestrator, separate sessions, and the human's own editor all touching files — two failures recur:

1. **Stale edits.** An agent reads a file, another writer changes it, and the first agent then edits from content that no longer exists on disk. The edit silently clobbers the change it never saw. The worst case is an agent versus the human's own editor, because that writer cannot be made to cooperate with any lock or claim protocol.
2. **Duplicated work.** Two agents, partitioned imperfectly, unknowingly do the same or overlapping work. Where a compiler or test suite catches the omission this is merely wasteful; where nothing catches it, it is a silent correctness hole.

Existing coordination designs answer both with the same locking mechanism, and that mechanism is wrong for both: it assumes every writer opts in (the human's editor never will), and it must guess a lease TTL for a process whose liveness it cannot observe through an event-only hook surface.

## Solution

Two independent layers, each doing one job (ADR-0001).

**Layer 1 — the version check (mandatory, blocking).** A `PreToolUse` hook on the edit tools re-hashes the target file and compares it against the read-hash the acting holder recorded at its last read. A mismatch means stale content: the hook blocks the edit (exit 2) and tells the agent to re-read and retry. It holds no leases, has no TTL, needs no liveness detection or reaper. Its defining property is that it requires no cooperation from the other writer — the human's editor calls no hook, but it moves the hash, so the holder is still caught (ADR-0002).

**Layer 2 — the intent registry (advisory, never blocks).** Two records per actor: a *predicted footprint* written once at subagent start from the delegation prompt, and an *actual footprint* appended as the actor touches paths. It answers "is anyone already doing this?" and reports who and what. It never denies. Divergence between predicted and actual footprint is the primary signal: where real work leaves predicted scope, that gap names the coupling the partition missed.

The read path for Layer 2 runs through the **orchestrator**, which queries the registry before delegating and folds the answer into the delegation prompt — because the `SubagentStart` hook cannot inject context directly (ADR-0005).

### The guarantee (honest about scope)

> **No edit-tool call proceeds when the target file's current content hash differs from the hash the acting holder recorded at its last read of it.** Out-of-band writes — shell commands and the human's editor — are reconciled best-effort so a subsequent edit-tool call sees the true current hash; a write that no concord hook ever observes is outside the guarantee.

(The reconciliation mechanism changed during implementation — see the T02 spike, `docs/spikes/filechanged.md`: `CheckEdit` compares against the live on-disk hash, and a synchronous `PostToolUse` `Bash|PowerShell` git-status sweep advances the writing actor's own read-hash. The `FileChanged` hook was rejected as async and unreliable. The sweep attributes the command's writes by **content delta** — a `PreToolUse` `Bash|PowerShell` snapshot of the dirty tree, diffed after — so it reconciles only the files the command itself changed, never a file a human's editor dirtied out of band (ADR-0008).)

### What concord permits to break, and why each is acceptable

- **A stale intent record.** Degrades dedup efficiency only; correctness is untouched (settled invariant). A record for a dead agent simply expires on silence.
- **A missed dedup opportunity** — two agents doing overlapping work despite the registry. Acceptable **only within safe scope** (refactors and mechanical work), where the verification pass makes the omission loud. This is the precondition, not an assumption.
- **A shell write in a non-git working directory**, where the git-status sweep finds nothing to reconcile. The guarantee is deliberately scoped so this is a known gap, not a broken promise; the human's verification pass remains the backstop.
- **A reported overlap that isn't real** — a false positive in the advisory layer. It costs one wasted look and never denies a write.
- **A version check skipped because the daemon is down (fail-open).** The `PreToolUse` hook exits 0 — allowing the edit — when it cannot reach the daemon or cannot hash the target, so a crashed daemon or a transient I/O error never bricks editing. "Mandatory, blocking" therefore means *enforced whenever the daemon is reachable*, not unconditionally; the protection is only as available as the daemon. This trades the guarantee for availability, deliberately, and is the one case where a stale edit can slip through without a block.

### The verification-pass precondition

concord's safe-scope claim depends on a condition concord does not itself provide: **skipping or deduplicating work is safe only where an omission is detectable.** The orchestrator MUST run the repository's own compiler and test suite on the merged result of delegated work; that run is what turns a silent omission into a loud failure. concord informs; it does not verify. Safe scope is therefore exactly the work where omission is loud — refactors and mechanical changes — and explicitly excludes open-ended exploration, where omission is silent.

---

## User Stories

**Stale-edit protection (Layer 1)**

1. As an orchestrator, I want each subagent's edits blocked when the target file changed since that subagent last read it, so that concurrent subagents cannot silently clobber each other.
2. As a subagent, I want to be told to re-read and retry when my edit is blocked, so that I can recover without human intervention.
3. As a human developer, I want an agent's edit blocked when it would overwrite a change I just made in my own editor, so that my uncommitted work is never silently lost — even though my editor speaks no coordination protocol.
4. As a subagent, I want my own prior read of a file to satisfy the check for my own later edit, so that I am never blocked by my own reads.
5. As a subagent, I want another agent's read of a file to NOT satisfy my check, so that staleness is judged per-actor and not shared.
6. As an orchestrator, I want the check to require no lease, TTL, or liveness signal, so that a crashed or abandoned agent leaves nothing held and no reaper is needed.
7. As a human developer, I want a file rewritten by a shell command (`sed -i`, `go fmt ./...`, a build script) to be reconciled so that the next agent edit sees the true current hash, so that the Bash hole is closed on a best-effort basis.
8. As a subagent, I want the block to carry a clear message naming the file and the reason, so that I act on it correctly.

**Work deduplication (Layer 2)**

9. As an orchestrator, I want to record a subagent's predicted footprint from its delegation prompt at spawn time, so that its intended scope is registered without relying on model compliance.
10. As an orchestrator, I want to query "is anyone already working on this intent or these paths?" before delegating, so that I can avoid handing out overlapping work.
11. As an orchestrator, I want the registry to report who and what overlaps, never to deny, so that a coarse advisory record never carries correctness weight.
12. As a subagent, I want my actual footprint appended as I touch paths, so that the registry reflects real work, not just predictions.
13. As an orchestrator, I want divergence between a subagent's predicted and actual footprint surfaced, so that I can see coupling the partition missed.
14. As an orchestrator, I want path overlap matched literally on path tokens, so that overlaps on concrete files are exact and cheap.
15. As an orchestrator, I want candidate intent strings returned to me so that I (the model) can judge semantic overlap in context, so that no similarity threshold has to be tuned and no embedding infrastructure is needed.
16. As an orchestrator running exploration work, I want to opt a subagent into read-footprint recording, so that exploration dedup has the read footprint it needs — while refactor agents pay only for write recording by default.
17. As an operator, I want intent records to refresh their TTL on every touch and expire after a configurable silence window (default 10 minutes), so that abandoned intent clears itself without a reaper.

**Operation and integration**

18. As an operator, I want concord to run as a single long-lived local daemon reached over localhost RPC, so that per-hook latency is dominated by a thin client spawn, not by starting the service.
19. As an operator, I want concord to run alongside Hindsight without coupling to it, so that neither project's releases block the other.
20. As an operator, I want durable memory and cross-session retrieval delegated to Hindsight, so that concord owns only in-flight state.
21. As an operator, I want the README to state plainly that Dragonfly is chosen for access shape, not scale, so that no one mistakes the tiny live keyspace for a scaling requirement.
22. As an operator, I want the hook overhead to stay a negligible fraction of turn time, so that coordination never becomes a latency tax.
23. As a developer of concord, I want the whole service driven through one RPC seam, so that behavior is testable without touching Claude Code internals.

---

## Implementation Decisions

**Language and shape.** A standalone Go service (ADR-0003). It runs as one long-lived local daemon; each hook invocation spawns a thin compiled Go client that makes a single RPC to the daemon over localhost TCP on a configurable port (ADR-0007).

**Store.** Dragonfly, holding read-hashes and intent records (ADR-0004). Chosen for access shape — high write rate, literal path-token matching, fast TTL expiry — not for scale, which the README must disclaim.

**The RPC surface (the one test seam, ADR-0007).** A typed RPC contract serves both the hook clients and the tests. The operations, at minimum:

- `RecordRead(actor_id, path, hash)` — a holder records its read-hash for a path.
- `CheckEdit(actor_id, path, current_hash) -> {allow | block, message}` — the version check; block when `current_hash` ≠ the holder's recorded read-hash.
- `ReconcileFileChange(actor_id, path, new_hash)` — driven by the `PostToolUse` git-status sweep, but only for paths the command actually wrote (attributed by content delta against a `PreToolUse` snapshot, ADR-0008); advances the writing actor's own read-hash after its out-of-band shell write. (`CheckEdit` already sees other writers' out-of-band changes because it compares against the live on-disk hash.)
- `RegisterPredicted(actor_id, intent_text, predicted_paths)` — write the predicted footprint once at subagent start.
- `AppendActual(actor_id, path)` — append to the actual footprint; refreshes the record TTL.
- `QueryIntent(intent_text, paths) -> {overlaps: [{actor_id, intent_text, paths}], ...}` — literal path-token intersection plus the candidate intent strings for the caller to judge; never denies.

**Actor identity.** Holder state is keyed on `actor_id` = `agent_id` for a subagent, `session_id` for a top-level session, compounded with the file path. Human editors and shell processes have no actor id; they record nothing but still move file hashes and are therefore caught by the version check without participating.

**Hooks.**
- `PreToolUse` on the edit tools (`Edit`, `Write`, `MultiEdit`, `NotebookEdit`) → `CheckEdit`; block with exit 2 on mismatch.
- `PostToolUse` (or read-capturing surface) → `RecordRead` on reads and `AppendActual` on writes. Edit-tool writes are recorded by default; read recording is an opt-in per-actor mode for exploration agents only (the peak-load measurement shows even all-tool recording costs ~0.5% of wall time, so the default is a cleanliness choice, not a cost one).
- `PreToolUse` on `Bash`/`PowerShell` → snapshot the hashes of the currently-dirty files (per actor), so the post sweep can attribute the command's own writes by content delta (ADR-0008).
- `PostToolUse` on `Bash`/`PowerShell` → run `git status --porcelain`, then `ReconcileFileChange` only for files whose hash appeared or changed since the pre snapshot, to close the Bash hole best-effort. A file a human's editor dirtied and the command left untouched is excluded, so reconciliation never advances the actor onto a change it did not make (User Story 3). With no snapshot it reconciles nothing (correctness before avoiding a false self-block). This is synchronous (it completes before the next tool call), unlike the rejected `FileChanged` hook (see the T02 spike, `docs/spikes/filechanged.md`). In a non-git directory it finds nothing, and those shell writes stay outside the guarantee.
- The intent read path is NOT a hook: `SubagentStart` cannot inject context (ADR-0005), so the orchestrator calls `QueryIntent` itself before delegating.

**Intent matching.** Path overlap is a literal set-intersection over path tokens in Dragonfly. Semantic intent overlap is judged by the querying model from the returned candidate strings — concord embeds nothing and tunes no threshold (ADR-0006).

**Intent TTL.** Refresh-on-touch, expire-on-silence, configurable; default silence window **10 minutes (600s)**. Justified by measurement: within active work, inter-tool-call gaps reach p99.9 at 567s, so a 600s window covers 99.9% of legitimate pauses while clearing an abandoned agent's record promptly.

**Measured budgets (from this machine's transcripts; see `docs/budgets.md`).**

| Quantity | Value |
|---|---|
| Edit calls/min | p50 ≈ 0 (bursty); busiest single minute 8/min |
| All tool calls/min | p50 1.3; busiest single minute 28/min |
| Inter-tool gap, active work | p50 7.6s, p99 403s, p99.9 567s |
| Median turn duration | ~896s (~15 min) |
| Hook spawn overhead | compiled ~11ms p50; python 24ms; node 36ms |
| Live intent keyspace | ~12KB at 6 concurrent subagents (~120KB at 10× headroom) |

The hook client must therefore be a **compiled binary** (not a python/node script) to keep per-hook overhead near 11ms; the Dragonfly round-trip is sub-millisecond and not the bottleneck.

---

## Testing Decisions

**What makes a good test here.** Tests exercise external behavior at the RPC seam — the sequence of calls a hook client would make — and assert on responses (allow/block, reported overlaps), never on internal storage layout. A test that reaches into Dragonfly key structure is testing implementation and is disallowed.

**The seam.** One seam: the concord RPC surface (Q9, ADR-0007). Every test drives the service through `RecordRead` / `CheckEdit` / `ReconcileFileChange` / `RegisterPredicted` / `AppendActual` / `QueryIntent` against an ephemeral Dragonfly instance. This is the highest seam available and exercises both layers fully without touching Claude Code hook internals. The thin hook shell clients are kept dumb enough (marshal hook JSON → one RPC → exit code) to not warrant their own tests.

**Modules tested.** The version-check logic and the intent-registry logic, both through the RPC surface. Representative scenarios to cover:

- `CheckEdit` allows when the holder's read-hash matches; blocks when it differs.
- One actor's `RecordRead` does not satisfy another actor's `CheckEdit`.
- `ReconcileFileChange` (simulating a shell/editor write) causes a previously-matching holder's next `CheckEdit` to block.
- `QueryIntent` reports overlapping actors and never returns a deny.
- `AppendActual` refreshes TTL; a record with no touch past the silence window is gone.
- Divergence: an actual footprint outside the predicted footprint is surfaced by a query.

**Prior art.** None yet — greenfield repo. Establish the ephemeral-Dragonfly-per-test-run harness as the pattern the rest of the suite follows. Implementation proceeds test-first (TDD) over the RPC surface.

---

## Out of Scope

- Leases, TTL-based locking, fencing tokens, deadlock detection. Rejected in favor of the content hash (ADR-0002).
- Blocking on intent overlap, in any form. The registry only informs (ADR-0001).
- Durable memory and retrieval over past sessions — Hindsight's job (ADR-0003).
- Kubernetes, multi-node, cross-machine coordination. Exactly one daemon per machine (ADR-0007).
- Embedding-based or vector semantic matching, and any tunable similarity threshold (ADR-0006).
- Parsing arbitrary shell commands for target paths. The Bash hole is handled by the git-status reconcile sweep or left explicitly outside the guarantee — never by fragile command parsing.
- Exploration as safe scope for dedup. Dedup is safe only where omission is loud.

---

## Further Notes

**First implementation task: promote the throwaway budget parser.** The budgets above came from a throwaway parser over `~/.claude/projects/`. Its output is captured in `docs/budgets.md`; the parser itself is disposable. Re-measure if the TTL default or hot-path decision is ever revisited.

**The Bash-hole mechanism was resolved by the T02 spike** (`docs/spikes/filechanged.md`): `FileChanged` was rejected (filename-scoped, unconfirmed payload, and — decisively — asynchronous, so it can race the next check). Reconciliation instead uses live-hashing at `CheckEdit` plus a synchronous `PostToolUse` git-status sweep. The sweep attributes the command's writes by content delta against a `PreToolUse` snapshot (ADR-0008), so a foreign out-of-band edit is never reconciled to the acting actor; a review found the original whole-tree sweep could do exactly that and defeat User Story 3. Shell writes in non-git directories remain outside the guarantee, which the guarantee statement already permits.

**The name.** "Lease manager" described a rejected architecture; the service is named **concord** — coordination without locking.
