# Stress / failure-hunt findings

Run the harness yourself: `go test -tags stress ./test/ -v -run TestStress` (needs Docker).
It reports `FINDING` (a real weakness) or `OK` per probe and never fails the build.

These are the weak points the version check + intent registry actually have, ranked.

> **Status (2026-09-07):** A, B, D, and H are **FIXED** and their stress probes now report OK.
> G is an accepted design limitation (see below). F was never a problem.

## A — Concurrent `AppendActual` loses ~97% of paths (SEVERE) — ✅ FIXED

`AppendActual` does read-record → append-in-Go → write-record. Under concurrency the
read-modify-write races: 100 concurrent touches on one actor kept **3 of 100** paths.

- **Impact:** with several subagents each touching many files, the *actual footprint*
  is mostly lost, which cripples divergence detection and actual-path overlap.
- **Cause:** the footprint is a JSON array rewritten wholesale (`store.go` `AppendActual` →
  `saveRecord`); concurrent writers clobber each other.
- **Fix direction:** make appends atomic on the store side — keep actual paths in a Redis
  **set** (`SADD` is atomic) keyed per actor, separate from the JSON record, and union them
  in `ListIntents`; or do the append in a Lua script / `WATCH`+`MULTI`. A single actor's
  own tool calls are serial, but subagents under one session can interleave, so this is real.

## B — Predicted (relative) never overlaps actual/query (absolute) (SEVERE) — ✅ FIXED

Predicted paths come from the delegation **prompt** and are repo-relative
(`src/auth/login.go`). Actual and query paths come from **hooks** and are absolute
(`C:/repo/src/auth/login.go`). For the *same file* they share no token, so
`path_overlap` is false.

- **Impact:** the orchestrator's pre-delegation `QueryIntent` (absolute paths) never
  matches a predicted footprint (relative), so predicted-based dedup silently never fires.
  Divergence is also wrong: predicted (relative) vs actual (absolute) look 100% divergent.
- **Cause:** no common path scale across the two ingestion points.
- **Fix direction:** normalize to one scale — best is **repo-relative** everywhere: the hook
  client resolves paths against the git repo root (`git rev-parse --show-toplevel`) before
  sending, and the orchestrator queries with repo-relative paths too. Absolute is the wrong
  common denominator (predicted paths from prompts may not exist on disk to absolutize).

## D — Case-insensitive filesystems split one file into many keys (MEDIUM) — ✅ FIXED

A read of `C:/Repo/Foo.txt` does not satisfy an edit of `C:/Repo/foo.txt` — the keys differ
by case though the OS treats them as one file.

- **Impact (this repo runs on Windows):** false blocks in the version check ("no recorded
  read") and missed overlaps in the registry when tools disagree on casing. Fails *safe*
  (blocks) for correctness, but is confusing and weakens dedup.
- **Fix direction:** normalize path case for the key on case-insensitive platforms (lowercase
  the volume+path on Windows/macOS) while preserving the original for messages. Pair this with
  the repo-relative normalization in B.

## H — Shell sweep clobbers a foreign out-of-band edit (SEVERE, correctness) — ✅ FIXED

The `PostToolUse` `Bash|PowerShell` sweep reconciled **every** file `git status --porcelain`
reported dirty. `git status` shows the whole working tree, not what the just-run command wrote,
so a file a **human's editor** had dirtied got reconciled to the acting actor's read-hash — and
that actor's next edit of it was then allowed, silently clobbering the human's uncommitted work.

- **Impact:** defeats User Story 3 (the top-ranked failure) whenever any shell command runs
  between the human's edit and the agent's edit — which is often. The T02 spike had wrongly
  called this case "covered, not a gap."
- **Cause:** the post-command git snapshot cannot attribute a write to the command; correct
  attribution needs the tree state *before* the command.
- **Fix (ADR-0008):** a `PreToolUse` `Bash|PowerShell` hook snapshots the dirty files' hashes;
  the post sweep reconciles only paths whose hash appeared or changed since (`hook.ReconcileTargets`).
  A file the command left untouched is excluded. Verified: probe H now reports OK, and an
  end-to-end run (real binaries + git repo + live daemon) blocks the agent's edit of a
  human-dirtied file (exit 2) while still allowing the file its own command wrote (exit 0).

## G — TTL cannot tell "thinking" from "dead" (BY DESIGN, real limitation)

An agent that is still working but silent longer than `CONCORD_INTENT_TTL` (default 600s)
disappears from the registry, so it stops deduplicating for the rest of its work.

- **Impact:** long single steps (a big build, a slow tool) blow the window and drop the
  record. Measured active p99.9 gap is 567s, so 600s is tight; a genuinely slow step exceeds it.
- **This is the accepted trade of the no-lease design** (a stale record must expire without a
  reaper). Mitigations: raise the default, or have `PreToolUse`/`PostToolUse` also refresh the
  record (any tool call = "still alive"), not just `AppendActual`.

## F — Throughput is a non-issue (OK)

1000 concurrent `CheckEdit` at concurrency 64: **~8,800 rps, p50 3.2ms, p99 27ms, 0 errors.**
The datastore is nowhere near a bottleneck — consistent with the budgets thesis (spawn
dominates, not Dragonfly). No action.

## Not covered here (needs real agents — see manual-live-tests.md)

- **Check-to-write TOCTOU:** `CheckEdit` runs at `PreToolUse`; the actual write happens after.
  A write landing in that window is not prevented. concord narrows the race, it does not close
  it. Only observable with real concurrent agents.
- **Semantic dedup quality:** overlap judgement is delegated to the model; its false-positive/
  negative rate is a property of the prompt, not testable here.
