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
