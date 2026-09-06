# concord — Tickets

Tracer-bullet vertical slices derived from `SPEC.md`. Each cuts a complete path through the RPC seam + Dragonfly + a test and is verifiable on its own. Ordered by dependency (blockers first). Vocabulary is defined in `CONTEXT.md`; decisions in `docs/adr/` and `DECISIONS.md`.

**Two independent chains after T01** — version check (T03 → T04 / T05) and intent registry (T06 → T07 → T08) — converge at T09. T01 and T02 have no blockers and can start in parallel.

Legend: every ticket is `ready-for-agent`.

---

## T01 — Scaffold + RPC seam + Dragonfly test harness

**What to build:** A running concord daemon that serves one trivial `Ping` RPC over Connect, plus the test harness that boots an ephemeral Dragonfly (testcontainers) and drives the daemon through the generated client. This is the TDD foundation everything else stands on.

**Blocked by:** None (can start immediately).

- [ ] `proto/concord/v1/coordination.proto` defines the service with a `Ping` RPC; `buf generate` produces committed code under `gen/`.
- [ ] `cmd/concord` boots on `CONCORD_ADDR` and serves Connect over localhost.
- [ ] `test` harness exposes `StartDragonfly(t)` and fails with a clear "start Docker" message when the daemon is unavailable.
- [ ] A Connect-client test calls `Ping` end-to-end and passes.
- [ ] `Makefile` targets `setup`, `generate`, `gate`; `.github/workflows/ci.yml` runs the green gate with a Dragonfly service.

## T02 — `FileChanged` capability spike

**What to build:** A documented answer to the two unverified platform facts the correctness story leans on, so T05 and T09 build on fact, not hope.

**Blocked by:** None (can start immediately).

- [ ] Determine whether the `FileChanged` hook payload carries the changed file path.
- [ ] Determine whether `FileChanged` fires before the next tool call or races it.
- [ ] Record the finding + the fallback ("Bash writes stay outside the guarantee") in `DECISIONS.md` and `docs/spikes/filechanged.md`.

## T03 — Version check: `RecordRead` + `CheckEdit`

**What to build:** The core correctness behaviour — a holder records the hash of a file at read time, and a later edit is allowed only if the file is unchanged.

**Blocked by:** T01.

- [ ] `RecordRead(actor_id, path, hash)` persists a holder's read-hash.
- [ ] `CheckEdit` allows when the current hash equals the holder's read-hash.
- [ ] `CheckEdit` blocks when they differ, returning a message telling the actor to re-read and retry.

## T04 — Version check: per-actor isolation

**What to build:** Staleness judged per actor — one agent's read never satisfies another agent's edit check.

**Blocked by:** T03.

- [ ] Actor A's `RecordRead` on a path does not satisfy actor B's `CheckEdit` on that path.
- [ ] An actor's own prior read does satisfy its own later `CheckEdit`.

## T05 — Version check: reconcile an actor's own out-of-band writes (`ReconcileFileChange`)

**What to build:** Prevent a false self-block: when an actor rewrites files via a shell command, that actor's own next edit should still be allowed, while everyone else stays protected. (Per the T02 spike, the *victim* case is already covered because `CheckEdit` compares against the live on-disk hash; this ticket handles only the writer's own read-hash.)

**Blocked by:** T03. (T02 spike settled the mechanism: a synchronous `PostToolUse` `Bash|PowerShell` git-status sweep drives this, not `FileChanged`.)

- [ ] `ReconcileFileChange(actor_id, path, new_hash)` advances that actor's stored read-hash for the path.
- [ ] After reconciliation, the writing actor's `CheckEdit` on that path is allowed again.
- [ ] Another actor's stored read-hash is unaffected — they remain blocked against the new content.

## T06 — Intent registry: `RegisterPredicted` + `QueryIntent`

**What to build:** The advisory dedup read path — register a subagent's predicted footprint, then answer "is anyone already doing this?" without ever denying.

**Blocked by:** T01.

- [ ] `RegisterPredicted(actor_id, intent_text, predicted_paths)` writes the predicted footprint once.
- [ ] `QueryIntent(intent_text, paths)` returns actors whose path tokens intersect (literal), plus the candidate intent strings for the caller to judge semantically.
- [ ] `QueryIntent` never denies — it only reports.

## T07 — Intent registry: `AppendActual` + TTL (refresh-on-touch, expire-on-silence)

**What to build:** The live footprint and its self-cleaning lifetime — actual paths accumulate as work happens, and an abandoned record clears itself.

**Blocked by:** T06.

- [ ] `AppendActual(actor_id, path)` appends to the actual footprint.
- [ ] Each touch refreshes the record's TTL.
- [ ] A record untouched past the configured silence window (default 600s; short value injected in tests) expires.

## T08 — Intent registry: divergence surfacing

**What to build:** The primary coupling signal — when real work leaves predicted scope, a query shows the gap.

**Blocked by:** T06, T07.

- [ ] An actual footprint entry outside an actor's predicted footprint is surfaced by a query as divergence.

## T09 — Hook clients + settings wiring

**What to build:** The thin compiled binaries that connect Claude Code's hooks to the daemon, plus the wiring that installs them.

**Blocked by:** T03, T05, T06, T07.

- [ ] `concord-hook` subcommands read hook JSON on stdin, make one RPC, set the correct exit code (exit 2 to block on stale edits): `pre-tool-use` (live-hash the target file → `CheckEdit`), `post-tool-use` (`AppendActual`; and for `Bash|PowerShell`, run `git status --porcelain` → `ReconcileFileChange` per changed file), `subagent-start` (`RegisterPredicted`).
- [ ] Actor id is resolved as `agent_id ?? session_id` from the hook payload.
- [ ] A sample `settings.json` wires the hooks (no `FileChanged` — see the T02 spike); a note documents that the intent read path is an orchestrator query, not a hook.

## T10 — Daemon operationalization + README

**What to build:** Making the daemon pleasant to run and honestly documented.

**Blocked by:** T01.

- [ ] Config via `CONCORD_ADDR` / `CONCORD_DRAGONFLY_ADDR` / `CONCORD_INTENT_TTL` with flag overrides; graceful shutdown.
- [ ] `README.md` states the guarantee verbatim and the "Dragonfly is chosen for access shape, not scale" disclaimer.
