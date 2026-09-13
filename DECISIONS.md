# Decisions

Running log of implementation decisions. Small, reversible choices live here; hard-to-reverse architectural ones get an ADR under `docs/adr/` and are linked from here. Append-only, newest at the bottom.

Format per entry:

```
## YYYY-MM-DD — <decision>
**Decision:** what was chosen.
**Why:** the reason.
**Alternatives:** what was rejected and why.
**Links:** ADRs, tickets, spec sections.
```

---

## 2026-09-06 — RPC transport is Connect-Go over protobuf
**Decision:** One `.proto` service contract, generated with buf into `gen/`, shared by the daemon, the `concord-hook` client, and the tests.
**Why:** A single typed contract is the one test seam; Connect handlers test as plain HTTP; the compiled hook client imports the generated client. Matches the "RPC for better experience" goal.
**Alternatives:** A hand-written Go interface + JSON/HTTP (no codegen, faster start) and `net/rpc`/gob (stdlib, but opaque wire) — rejected for weaker typing/DX at the shared contract.
**Links:** ADR-0007; TICKETS T01.

## 2026-09-06 — Tests run against real Dragonfly via testcontainers
**Decision:** Every test boots an ephemeral real Dragonfly container; no in-memory fake.
**Why:** User chose fidelity over inner-loop speed — the store's real behaviour (TTL, SINTER) is exercised directly.
**Alternatives:** miniredis (pure-Go, no Docker, fast) — rejected; it would test a fake, not Dragonfly.
**Consequence:** The Docker daemon must be running for any test to pass. The harness prints a clear "start Docker" message when it is not.
**Links:** SPEC "Testing Decisions"; TICKETS T01.

## 2026-09-06 — "Verification pass" is a runtime term only; the dev gate is the "green gate"
**Decision:** Reserve "verification pass" for the concord runtime concept (the orchestrator runs the target repo's compiler+tests on merged work). Call our own pre-commit dev gate the "green gate": buf-generate-clean, gofmt/vet/lint clean, build, `go test ./...`, plus the ticket's acceptance tests.
**Why:** The two senses collided and the glossary must stay precise (domain-modeling).
**Alternatives:** Reusing "verification pass" for both — rejected as ambiguous.
**Links:** CONTEXT.md (Verification pass); SPEC "Testing Decisions".

## 2026-09-06 — Content hashing is SHA-256, hex-encoded, over raw file bytes
**Decision:** A single shared helper in `internal/hashing` computes the read-hash and the pre-edit re-hash identically.
**Why:** The version check is only correct if both sides hash the exact same way; one function removes drift.
**Alternatives:** xxhash/BLAKE3 (faster) — rejected as premature; hashing is not on the measured hot path (spawn dominates).
**Links:** ADR-0002; TICKETS T03.

## 2026-09-06 — Actor id is `agent_id ?? session_id`
**Decision:** The hook client resolves the actor id from the hook payload: `agent_id` for a subagent, else `session_id` for a top-level session.
**Why:** `agent_id` is the confirmed unique per-subagent identifier; top-level sessions have only `session_id`. Human editors and shell processes have neither and record nothing.
**Links:** ADR-0005; CONTEXT.md (Actor id); TICKETS T09.

## 2026-09-06 — T01: daemon listens on loopback `127.0.0.1:8973` by default
**Decision:** `CONCORD_ADDR` defaults to `127.0.0.1:8973`; the daemon binds loopback only.
**Why:** concord is one daemon per machine, never networked (ADR-0007); loopback avoids exposing it and sidesteps firewall prompts.
**Alternatives:** `0.0.0.0` (rejected — needless exposure); a Unix socket (rejected — cross-platform friction on Windows).
**Links:** TICKETS T01; ADR-0007.

## 2026-09-06 — T01: Connect served over HTTP/1.1, no h2c
**Decision:** The daemon serves the Connect handler on a plain `net/http` server; no h2c/HTTP-2 wiring.
**Why:** Connect unary RPC works over HTTP/1.1, which is enough for a localhost daemon + a thin unary client. Avoids an extra dependency (`golang.org/x/net/http2/h2c`).
**Alternatives:** h2c to also accept gRPC clients — deferred; no gRPC client is planned.
**Links:** TICKETS T01; ADR-0007.

## 2026-09-06 — T01: tests get Dragonfly from testcontainers, image pinned by tag
**Decision:** The `test.StartDragonfly` harness boots `docker.dragonflydb.io/dragonflydb/dragonfly:latest` via testcontainers-go; CI relies on the same harness (the ubuntu runner's Docker), not a `services:` container.
**Why:** One code path produces Dragonfly for both local and CI runs; no divergence between "what tests spin up" and "what CI provides."
**Alternatives:** A GitHub Actions `services:` Dragonfly (rejected — would bypass the harness and drift from local behaviour). Digest-pinning the image is deferred; `:latest` is acceptable pre-1.0.
**Links:** TICKETS T01; DECISIONS "tests run against real Dragonfly".

## 2026-09-06 — T01: repo normalizes line endings to LF via `.gitattributes`
**Decision:** `* text=auto eol=lf` plus explicit `*.go/*.proto/*.md eol=lf`.
**Why:** Generated code is committed; the CI gate runs `buf generate` then `git diff --exit-code`. Without normalization, a Windows CRLF checkout would diff against Linux LF codegen and fail the gate spuriously.
**Links:** TICKETS T01; the green gate.

## 2026-09-06 — T02 spike: do not build correctness on `FileChanged`
**Decision:** Reject `FileChanged` as the Bash-hole mechanism. Reconcile out-of-band writes two ways instead: (a) `CheckEdit` compares against the file's live on-disk hash, and (b) a synchronous `PostToolUse` `Bash|PowerShell` hook runs `git status --porcelain` and calls `ReconcileFileChange` for the writing actor.
**Why:** `FileChanged` fires on Bash/external writes (good) but is filename-scoped (not "any file"), its input schema for the changed path is unconfirmed, and — decisively — its timing is unstated and async hooks exist, so it can race the next `CheckEdit`. An unbounded race is disqualifying for a correctness layer. The git-status sweep is synchronous (PostToolUse completes before the next tool call) and needs no watch list.
**Alternatives:** `FileChanged`-driven reconciliation (rejected: async race, filename scope); parsing shell commands for paths (rejected in SPEC: fragile).
**Residual (accepted):** non-git dirs get no sweep → an actor's own shell writes there may cause a false block (efficiency, not correctness). Anything neither hook sees stays outside the guarantee, as SPEC states.
**Links:** docs/spikes/filechanged.md; TICKETS T05/T09; SPEC "The guarantee".

## 2026-09-06 — T03 design: `CheckEdit` compares against the live on-disk hash
**Decision:** The `concord-hook` client computes the target file's current SHA-256 from disk immediately before calling `CheckEdit(actor_id, path, current_hash)`; the daemon only compares `current_hash` to the actor's stored read-hash. The daemon never reads files.
**Why:** Live-hashing makes every out-of-band write visible to all *other* holders for free (their stored read-hash won't match live disk), so no separate "canonical hash" store or reconciliation is needed for the victim case. Keeping file I/O in the client keeps the daemon a pure state comparator and testable purely through the RPC seam.
**Consequence:** `ReconcileFileChange(actor_id, path, new_hash)` exists only to advance the *writing* actor's own read-hash after its out-of-band write (case b above), preventing a false self-block.
**Links:** TICKETS T03/T05; ADR-0002; docs/spikes/filechanged.md.

## 2026-09-06 — T03: CheckEdit rules for the "no recorded read" case
**Decision:** When an actor has no recorded read of a path, `CheckEdit` allows the edit if `current_hash` is empty (the file does not exist → new-file creation) and blocks it otherwise (an existing file the actor never read has no freshness basis). Found read-hash: allow on exact match, block on mismatch.
**Why:** Blocking every unrecorded edit would break legitimate file creation via the Write tool; allowing every unrecorded edit would let an actor clobber an existing file it never read. Splitting on `current_hash == ""` distinguishes the two using only what the client already sends — no file I/O in the daemon.
**Alternatives:** Always block on no-read (rejected — breaks creation); always allow on no-read (rejected — misses the never-read-existing-file case); pass an explicit `file_exists` flag (deferred — empty hash already encodes it).
**Links:** TICKETS T03; DECISIONS "CheckEdit compares against the live on-disk hash".

## 2026-09-06 — T03: read-hash key layout and no expiry
**Decision:** Read-hashes live at `readhash:{actor_id}\x1f{path}` (unit-separator between the two fields) with no TTL. The store is exposed as a narrow `ReadHashStore` interface; `RedisStore` is the go-redis implementation.
**Why:** The unit separator makes actor/path keys collision-proof regardless of contents. No TTL matches ADR-0002 (the version check holds nothing on a timer); a leftover read-hash for a dead actor is harmless. A narrow interface keeps the service depending on behaviour, not on go-redis, and keeps tests at the RPC seam.
**Links:** TICKETS T03; ADR-0002.

## 2026-09-06 — T05: ReconcileFileChange is a distinct RPC, actor-scoped
**Decision:** `ReconcileFileChange(actor_id, path, new_hash)` is its own RPC even though it currently just advances the calling actor's read-hash (same store write as `RecordRead`). It touches only that actor's record.
**Why:** Its meaning and driver differ from a tool Read — it is fired by the `PostToolUse` git-status sweep after a shell write (T09), not by a Read tool. Keeping it separate keeps the contract self-documenting and leaves room for reconcile-only behaviour later (e.g. only-if-record-exists) without overloading RecordRead.
**Alternatives:** Merge into RecordRead (rejected — conflates two callers/meanings).
**Links:** TICKETS T05/T09; docs/spikes/filechanged.md.

## 2026-09-06 — T06: path overlap = same file or same directory
**Decision:** A path expands to tokens `{normalized full path, parent dir}`; two footprints overlap when their token sets intersect. A bare top-level file contributes only its full path (the "." dir is dropped), so unrelated root files don't collide.
**Why:** Exact-file-only overlap misses agents working different files in the same directory — the common "are we in the same area?" case. Full segment tokenization is the opposite failure: everything under `src/` would collide. `{file, parent-dir}` is the useful middle. Over-reporting is safe anyway — the layer never denies.
**Alternatives:** Exact full-path match only (too tight); per-segment tokens (too loose — top dirs collide); Redis SINTER with a reverse index (deferred — keyspace is ~6 actors, a Go scan is trivial).
**Links:** TICKETS T06; ADR-0006; internal/coordination/overlap.go.

## 2026-09-06 — T06: QueryIntent returns all active intents as candidates
**Decision:** `QueryIntent` returns every active intent record, each with a `path_overlap` flag; it does not filter to overlapping ones, and it ignores the caller's `intent_text` server-side.
**Why:** Semantic overlap is the caller's (the model's) judgement (ADR-0006), so the server must hand back candidate `intent_text`s even when paths don't overlap. The literal `path_overlap` flag is the only matching the server does. The tiny keyspace makes returning all records cheap.
**Consequence:** If the keyspace ever grows large, add server-side candidate filtering; not needed at measured scale.
**Links:** TICKETS T06; ADR-0006; docs/budgets.md.

## 2026-09-06 — T06: intent records stored as JSON + an `intents` set
**Decision:** Each record is a JSON blob at `intent:{actor_id}`; an `intents` Redis set enumerates active actors. `RedisStore` implements both `ReadHashStore` and `IntentStore`.
**Why:** A JSON blob keeps the whole footprint (predicted + actual) in one value, simple to read and rewrite; the set gives O(1) enumeration without SCAN. One store type over one Dragonfly connection keeps wiring trivial while the service still depends on two segregated interfaces.
**Links:** TICKETS T06/T07.

## 2026-09-06 — T07: intent TTL is a store-level policy, refreshed on every write
**Decision:** `NewRedisStore(addr, intentTTL)` holds the silence window; every `PutPredicted` and `AppendActual` writes the record with `SET ... EX=intentTTL`, so any touch refreshes expiry atomically. Read-hashes remain TTL-less. Default 600s, overridable via `CONCORD_INTENT_TTL` (a Go duration string).
**Why:** Refresh-on-touch, expire-on-silence is the agreed lifetime (measured p99.9 active gap = 567s → 600s window). Setting the TTL on the value write makes refresh a side effect of the write with no extra round trip. Tests inject sub-second TTLs to exercise expiry quickly.
**Alternatives:** A separate reaper/sweeper (rejected — the version-check philosophy is to hold nothing that needs reaping; Redis expiry does it); per-call TTL parameters (rejected — TTL is a store policy, not a per-call concern).
**Links:** TICKETS T07; docs/budgets.md; ADR-0002.

## 2026-09-06 — T07: AppendActual dedups and self-creates; the actor set may hold dead ids
**Decision:** `AppendActual` appends a path only if absent, and creates a bare record if the actor has none yet; either way it refreshes TTL. When a record key expires, its id can linger in the `intents` set — `ListIntents` skips ids whose key is gone (MGet miss).
**Why:** Dedup keeps a repeatedly-touched file from bloating the footprint. Self-creation tolerates AppendActual arriving before RegisterPredicted. Leaving dead ids in the set is harmless (they're filtered on read) and avoids needing expiry notifications; the set is tiny.
**Consequence:** If the set ever grows unbounded across long uptimes, prune it opportunistically during ListIntents. Not needed at measured scale.
**Links:** TICKETS T07; docs/budgets.md.

## 2026-09-06 — T08: divergence is per-path, surfaced on every QueryIntent match
**Decision:** Each `IntentMatch` carries `divergent_paths`: the actor's actual paths that share no token with any predicted path (same overlap rule). It is computed on every query, not stored. With no predicted scope, all actual paths are divergent.
**Why:** Divergence is the primary coupling signal (SPEC §2); surfacing the specific paths tells the caller *what* left scope, not just that something did. Computing it at query time keeps records simple and always-consistent with the current footprint.
**Alternatives:** A single `diverged` bool (rejected — loses which paths); precomputing/storing divergence (rejected — must recompute on every predicted/actual change anyway).
**Links:** TICKETS T08; ADR-0006; internal/coordination/overlap.go.

## 2026-09-06 — T09a: client logic lives in pure packages, tested off the seam
**Decision:** The hook client's real logic — file hashing (`internal/hashing`), hook-JSON parsing, actor-id resolution, tool classification, and `git status --porcelain` parsing (`internal/hook`) — lives in pure packages with plain unit tests. `cmd/concord-hook` (T09b) stays thin glue over them plus the generated Connect client.
**Why:** The RPC seam is for the service; the hook client also carries non-trivial logic that would otherwise be untested "glue." Isolating it as pure functions makes it testable without a daemon or Docker, keeping the CLI itself dumb enough to not need its own tests.
**Detail:** git-status rename lines (`R old -> new`) resolve to the new path.
**Links:** TICKETS T09; ADR-0005 (actor id).

## 2026-09-06 — T09b: correctness hooks fail open when the daemon is unreachable
**Decision:** `concord-hook pre-tool-use` exits 0 (allowing the edit) with a stderr warning if it cannot reach the daemon or errors internally; it exits 2 only on an actual stale-edit verdict. Advisory subcommands always exit 0.
**Why:** concord is an assist, not a gatekeeper. A crashed daemon or a hashing error must not brick the user's ability to edit. The guarantee is already scoped to "when concord observes"; unavailability is one of the cases it does not cover.
**Alternatives:** Fail closed (rejected — a down daemon would halt all edits).
**Links:** TICKETS T09; SPEC "The guarantee"; cmd/concord-hook.

## 2026-09-06 — T09b: the hook client normalizes all paths to absolute (bug caught in live test)
**Decision:** `concord-hook` resolves every path to an absolute, cleaned, forward-slash form before sending it to the daemon — for edit-tool `file_path` and for the repo-relative paths from `git status --porcelain`.
**Why:** A live end-to-end test showed edit tools pass absolute paths while the git-status sweep yields repo-relative ones; without normalization the two produced different keys and reconcile silently missed. `filepath.Abs` (resolved against the hook's working directory = repo root) makes the keys agree.
**Links:** TICKETS T05/T09; cmd/concord-hook normalize().

## 2026-09-06 — T09b: predicted-path extraction is best-effort and prompt-shaped
**Decision:** `ExtractPredictedPaths` pulls slash-containing tokens from the delegation prompt. It targets repo-relative paths (`src/auth/login.go`); a Windows drive-letter absolute path (`C:/...`) is mangled because `:` is not a path char. Left as-is.
**Why:** The predicted footprint is advisory — a missed or malformed predicted path only weakens dedup, never correctness — and real delegation prompts use repo-relative paths. Handling drive letters isn't worth the regex complexity.
**Links:** TICKETS T09; internal/hook.ExtractPredictedPaths.

## 2026-09-06 — T10: serving is a testable run(ctx, listener, cfg); signals only in main
**Decision:** The daemon's serve-and-shutdown lifecycle lives in `run(ctx, ln, cfg)`, which serves on a caller-supplied listener until the context is cancelled, then calls `srv.Shutdown` and closes the store. `main` builds the config, opens the listener, and derives ctx from `signal.NotifyContext(os.Interrupt, SIGTERM)`.
**Why:** A test cancels the context and asserts `run` returns, deterministically verifying graceful shutdown with no OS-signal delivery — which is unreliable on Windows (Git Bash `kill -TERM` hard-terminates rather than delivering a catchable signal). Passing the listener in lets the test bind `127.0.0.1:0` and learn the port.
**Alternatives:** Testing via real signals (rejected — flaky/undeliverable on Windows); no shutdown test (rejected — the one ops behaviour worth proving).
**Links:** TICKETS T10; cmd/concord.

## 2026-09-06 — Review fix: NotebookEdit targets notebook_path, not file_path
**Decision:** Added `ToolInput.NotebookPath` and `Input.EditPath()` (file_path, falling back to notebook_path); the hook client uses `EditPath()` for the version check and footprint on all edit tools.
**Why:** Code review found NotebookEdit — listed as a covered edit tool (SPEC L99, `IsEditTool`) — carried an empty `file_path`, so `preToolUse` exited 0 without checking. NotebookEdit edits were silently unprotected: a real Layer-1 correctness hole.
**Links:** code review (Spec finding 2); internal/hook.EditPath; cmd/concord-hook.

## 2026-09-06 — T11: exploration read-footprint opts in via CONCORD_RECORD_READS
**Decision:** When `CONCORD_RECORD_READS` is truthy in the hook's environment, `post-tool-use` on a `Read` also calls `AppendActual`, adding reads to the actual footprint. Off by default.
**Why:** SPEC user story 16 / CONTEXT ("reads only in exploration mode") require read footprint for exploration dedup, but only for opted-in agents. The code review found this slipped (no ticket). An env var is the available opt-in surface: Claude Code hook config is global, so there is no per-actor hook flag — an orchestrator sets `CONCORD_RECORD_READS` in the environment of an exploration run. Verified live: a Read with the flag set appears in QueryIntent; without it, it does not.
**Alternatives:** A per-actor server-side mode (rejected — the actor's mode isn't known at read time without extra registration); always recording reads (rejected — the measured hot path is fine but it needlessly bloats footprint for the common refactor case).
**Links:** TICKETS T11; SPEC user story 16; internal/hook.RecordReads; cmd/concord-hook.

## 2026-09-07 — Bug A fix: actual footprint is a Redis set, so appends are atomic
**Decision:** Actual paths now live in a per-actor Redis **set** (`intent:{actor}:actual`, `SADD`), separate from the predicted JSON doc (`intent:{actor}`). `ListIntents` reads both (GET + SMEMBERS in one pipeline) and combines them; the intent TTL is refreshed on both keys on every write.
**Why (diagnosis):** the old `AppendActual` did GET-json → append-in-Go → SET-json; concurrent touches on one actor raced (last-writer-wins). The stress harness reproduced it deterministically — 100 concurrent appends kept **3 of 100** paths. `SADD` is atomic and set-valued, so concurrent appends neither race nor duplicate. Confirmed: the regression test (`TestAppendActualIsAtomicUnderConcurrency`, 50/50) and the stress probe (100/100) both pass.
**Consequence:** the stored JSON doc no longer carries actual paths. A subagent's own calls are serial, but subagents under one session interleave — the real contended case.
**Links:** docs/testing/stress-findings.md (A); internal/store/store.go; TICKETS T07.

## 2026-09-07 — Bugs B & D fix: the hook client canonicalizes every path
**Decision:** `concord-hook` maps every path to one daemon key via `canonical()`: **repo-relative** (`hook.RepoRelative`, using `git rev-parse --show-toplevel`) and **case-folded** on Windows/macOS (`hook.FoldCase`). The real filesystem path is still used for hashing; only the *key* is canonicalized. Orchestrators must query with repo-relative paths too (documented in `docs/hooks/orchestrator-query.md`).
**Why (diagnosis):**
- **B:** predicted paths come from the delegation prompt (repo-relative) while actual/query paths came from hooks (absolute via `filepath.Abs`), so they shared no tokens — pre-delegation dedup never fired and everything looked divergent. Unifying on repo-relative aligns them. Verified live: predicted + actual both `src/auth/login.go`, `overlap=true`, `divergent=none`.
- **D:** path keys were case-sensitive, so on a case-insensitive filesystem `Foo.txt` and `foo.txt` became two keys (false blocks, missed overlap). Folding case on those platforms collapses them to one key.
**Fix seam:** the bugs are client-side (the service is correct given consistent input), so the regressions are pure unit tests (`TestRepoRelative`, `TestFoldCase`) plus the stress probes B/D, updated to apply the same canonicalization and now reporting OK. `git status --porcelain` is now run with `-C <root>` so its paths are root-relative and reconcile keys match.
**Consequence:** in a non-git directory `repoRoot()` is "" and paths stay absolute — the same accepted gap as the shell-reconcile sweep.
**Links:** docs/testing/stress-findings.md (B, D); internal/hook.RepoRelative/FoldCase; cmd/concord-hook.canonical.

## 2026-09-07 — Review fix (US3 hole): shell reconcile attributes writes by content delta
**Decision:** The `PostToolUse` `Bash|PowerShell` sweep no longer reconciles every dirty file. A new `PreToolUse` `Bash|PowerShell` hook snapshots the hashes of the currently-dirty files to a per-actor temp file (`os.TempDir()/hook.SnapshotName(actor)`); the post sweep re-hashes the dirty tree and reconciles only the paths whose hash appeared or changed since the snapshot (`hook.ReconcileTargets`). With no snapshot it reconciles nothing.
**Why (diagnosis):** the review found `git status --porcelain` reports the whole dirty tree, not what the just-run command wrote, so the sweep could not distinguish an actor's own shell write from a file a **human's editor** dirtied out of band. It reconciled both, advancing the actor's read-hash onto a change it never made — so the actor's next edit of that file flipped from BLOCK to ALLOW and clobbered the human's uncommitted work. This defeated User Story 3 whenever any shell command ran between the human's edit and the agent's edit — the exact case the T02 spike wrongly called "covered, not a gap." Reproduced by stress probe H (BLOCK→ALLOW), and the content-delta snapshot fixes it (probe H now OK).
**Fix seam:** attribution is a pure function (`hook.ReconcileTargets`), unit-tested (`TestReconcileTargets`), plus `hook.SnapshotName` (`TestSnapshotName`); only the git call and temp-file I/O sit in the thin client. Verified end-to-end with the real binaries + a git scratch repo + a live daemon: after a human out-of-band edit and an unrelated agent shell command, the agent's edit of the human-dirtied file is **blocked (exit 2)**, while its edit of the file its own command wrote is **allowed (exit 0)** — T05 preserved.
**Alternatives:** a read-hash guard (reconcile only files the actor never read) — rejected: it reintroduces a false self-block for shell rewrites of previously-read files (weakens T05); scoping the guarantee to accept the hole — rejected by the user in favour of the proper fix.
**Consequence:** the thin client is now stateful across a command's two hook spawns (one temp file per actor). Residual gap shrinks to a human editing *during* the command's execution window. Two `git status` runs + hashing the dirty set per shell call — negligible vs. spawn (budgets).
**Links:** ADR-0008; code review (Spec finding); docs/testing/stress-findings.md (H); internal/hook.ReconcileTargets/SnapshotName; cmd/concord-hook.reconcileShellWrites/snapshotDirtyState.

## 2026-09-07 — Review fix (intents-set leak): ListIntents self-heals the active-id set
**Decision:** `ListIntents` now `SREM`s from the `intents` active-id set every id whose predicted and actual keys have both expired (the branch that previously only `continue`d). Cleanup happens opportunistically on the read path.
**Why (diagnosis):** the review found the `intents` set is never trimmed and has no TTL of its own, while `PutPredicted`/`AppendActual` only ever `SADD` to it. Records expire (Redis TTL) but their ids linger in the set forever, so on a long-lived daemon the set grows without bound and every `QueryIntent` pipelines a `GET`+`SMEMBERS` per dead id — O(all actors ever seen). No correctness impact (dead ids are already skipped), purely memory + latency.
**Fix seam (exception, agreed with user):** the fix has **no observable effect at the RPC seam** — `QueryIntent` already returns correct results for expired actors, so the only regressable signal is the internal set membership, which the "never test key layout" rule normally forbids. By agreement this one fix gets a white-box store test (`internal/store/store_test.go`, `TestListIntentsEvictsExpiredIDFromActiveSet`) asserting the id leaves the set after expiry; it is marked as the deliberate exception it is. Red before the fix (id lingered), green after.
**Consequence:** a touch racing the eviction simply re-adds the id (every write `SADD`s), so the self-heal is safe under the advisory layer's "a stale intent record is permitted" invariant. Consistent with the no-reaper philosophy (ADR-0002): Redis expiry clears the records, the read path clears the index.
**Links:** ADR-0002; code review (bug 2); internal/store/store.go ListIntents.

## 2026-09-07 — Review fix (bug 4): the git-status parser decodes C-quoted paths
**Decision:** `ParseGitStatusPorcelain` now decodes git's C-style path quoting (`unquoteGitPath`: standard escapes + `\NNN` octal), and the hook client runs `git status` with `-c core.quotepath=false`.
**Why (diagnosis):** git quotes any path with unusual bytes — a non-ASCII byte is octal-escaped (`"caf\303\251.go"`) under the default `core.quotepath`, and a space forces quoting (`"with space.go"`) regardless. The old parser stripped the surrounding quotes but left the escapes, so the reconcile key (`caf\303\251.go`) never matched what the edit tool sent (`café.go`) — a silently missed reconcile for any file with a non-ASCII or spaced name. Confirmed against real `git status` output.
**Fix seam:** `ParseGitStatusPorcelain` is a pure client helper already unit-tested; the regression is `TestParseGitStatusPorcelainUnquotes` (octal UTF-8, spaced, quoted-rename, plain), red before / green after. `quotepath=false` is belt-and-suspenders (unquoted common case + readable logs); the decoder handles whatever git still quotes.
**Links:** code review (bug 4); internal/hook.ParseGitStatusPorcelain/unquoteGitPath; cmd/concord-hook.dirtyHashes.

## 2026-09-07 — Review clarification (bug 3): the version check fails open, documented
**Decision:** No behaviour change; the guarantee wording now states plainly that the `PreToolUse` version check fails **open** — it exits 0 (allows the edit, with a stderr warning) when the daemon is unreachable or the target cannot be hashed. Documented in README ("Availability") and SPEC ("What concord permits to break").
**Why:** the review noted "mandatory, blocking" over-promised: the fail-open path (already in the client since T09, so a crashed daemon never bricks editing) means enforcement is only as available as the daemon, which the guarantee never disclosed. This is a deliberate availability-over-guarantee trade, kept; only the honesty gap is fixed. No test — it is a documentation change, and the fail-open behaviour itself is unchanged and exercised manually (manual-live-tests §6).
**Links:** code review (bug 3); README "Availability"; SPEC "What concord permits to break"; cmd/concord-hook.preToolUse.

## 2026-09-08 — Pre-merge review follow-ups on fix/code-review-findings
**Decision:** Two cheap fixes before merging the branch, from the second two-axis review:
1. **Finish bug 3:** `CLAUDE.md`'s line-1 "mandatory blocking version check" was the one place still over-promising after SPEC/README were softened. Now qualified: enforced whenever the daemon is reachable, fails open otherwise.
2. **Cover the load-bearing disk mechanism:** added `TestShellReconcileAttributesByContentDelta` (`cmd/concord-hook/main_test.go`) — an integration test that drives `snapshotDirtyState` → temp file → `reconcileShellWrites` against a real git tree + Dragonfly, asserting the human-dirtied file stays BLOCK and the command's own write is ALLOWED. Previously only the pure `ReconcileTargets` was unit-tested; the review flagged the cross-process/temp-file/actor-keying path as manual-only. It memoizes `repoRoot()` per process, so it is the sole `repoRoot` caller in that test binary (a single integration test, by design).
**Deferred (post-merge, lower risk for a single-user dev tool):** harden the snapshot temp file (per-user dir or `O_EXCL` — predictable name in a world-shared temp dir, symlink-follow) and record the per-actor-vs-per-command and stale-snapshot-if-Post-never-fires windows in ADR-0008.
**Links:** code review (second pass); cmd/concord-hook/main_test.go; CLAUDE.md; ADR-0008.

## 2026-09-12 — CI stages 1-2: caching/concurrency + lint and race jobs
**Decision:** Upgrade `.github/workflows/ci.yml` from one `gate` job to three parallel jobs — `gate` (codegen check, gofmt, vet, build, tests), `lint` (golangci-lint, config in `.golangci.yml`, pinned v1.64.8), `race` (`go test -race`) — plus concurrency-cancel, least-privilege `permissions: contents: read`, `workflow_dispatch`, and module caching.
**Why:** the `race` job is the on-point guard for this repo (bug A was a data race); `lint` catches a class of issues the gate misses. Verified locally: `golangci-lint run ./...` is clean and `go test ./...` passes. The `-race` job could NOT be verified locally — the race detector needs cgo + a C toolchain, and this Windows dev box has `CGO_ENABLED=0` and no gcc; it runs on the ubuntu runner (cgo on by default).
**Deferred (later stages):** OS matrix (Windows + Linux for the path/case logic), GoReleaser CD on tags, SHA-pinned actions, Dependabot. Branch protection (require `gate`) is a repo setting, not the workflow file.
**Links:** .github/workflows/ci.yml; .golangci.yml.

## 2026-09-12 — CI stage 3: OS matrix (ubuntu + windows)
**Decision:** `gate` becomes a `strategy.matrix` over `[ubuntu-latest, windows-latest]` (`fail-fast: false`). Linux runs the full suite (Dragonfly via testcontainers) plus the OS-independent codegen-currency and gofmt checks; Windows runs `go build`/`go vet` (portability) and tests only the Docker-free packages (`internal/hook`, `internal/hashing`, `cmd/concord`) — the path/case logic that bugs B and D lived in. A workflow-level `defaults.run.shell: bash` makes every OS use Git Bash, so the gofmt `$(...)` check and multi-command exit semantics behave identically (Windows runners default to PowerShell otherwise).
**Why:** Windows CI exists to catch the OS-sensitive path canonicalization (RepoRelative/FoldCase); those live in pure, Docker-free packages. testcontainers' Dragonfly is a Linux container and Windows runners can't run it reliably, so the integration tests stay Linux-only. Verified on a real Windows box (this dev machine): `go build ./...`, `go vet ./...`, and `go test ./internal/hook/... ./internal/hashing/... ./cmd/concord` all pass.
**Alternatives:** full suite on Windows (rejected — flaky/slow Linux-container support); Windows build-only (rejected — wouldn't exercise the OS-sensitive logic); a dedicated windows job instead of a matrix (rejected — user chose the matrix to learn it).
**Links:** .github/workflows/ci.yml; DECISIONS "CI stages 1-2".
