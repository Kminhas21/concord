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
