# concord

A standalone Go service, backed by Dragonfly, that coordinates concurrent coding
agents through two independent layers:

- **The version check** — mandatory, blocking. Stops stale edits. It is the
  correctness layer.
- **The intent registry** — advisory, never blocks. Reduces duplicated work by
  reporting who is already working where.

It runs alongside a self-hosted Hindsight instance without being coupled to it.

## The guarantee

> **No edit-tool call proceeds when the target file's current content hash
> differs from the hash the acting holder recorded at its last read of it.**
> Out-of-band writes — shell commands and the human's editor — are reconciled
> best-effort so a subsequent edit-tool call sees the true current hash; a write
> that no concord hook ever observes is outside the guarantee.

The version check requires no cooperation from other writers. Your own editor
calls no hook, but it changes the file's hash — so an agent editing against your
uncommitted change is still caught. It holds no leases, TTLs, or fencing tokens,
and needs no liveness detection or reaper.

## A note on Dragonfly

Dragonfly is chosen for **access shape, not scale**. The state is ephemeral,
written at a high rate, and needs literal path-token matching plus fast expiry —
a Redis-compatible in-memory store fits it well. The live keyspace is tiny
(~12KB at the observed peak of 6 concurrent subagents; ~120KB with 10× headroom;
see [docs/budgets.md](docs/budgets.md)). Nothing here needs Dragonfly's scale;
the choice is about fit.

## How it works

concord runs as one long-lived local daemon. Claude Code invokes the thin
`concord-hook` client at hook points; each invocation makes one RPC to the daemon:

| Hook | Tool | RPC |
|---|---|---|
| `PreToolUse` | Edit/Write/MultiEdit/NotebookEdit | `CheckEdit` — blocks a stale edit (exit 2) |
| `PostToolUse` | Read | `RecordRead` — records the read-hash |
| `PostToolUse` | edit tools | `RecordRead` (advance own hash) + `AppendActual` |
| `PostToolUse` | Bash/PowerShell | `git status --porcelain` → `ReconcileFileChange` |
| `SubagentStart` | — | `RegisterPredicted` from the delegation prompt |

The intent **read** path is not a hook: the orchestrator calls `QueryIntent`
before delegating and folds the answer into the delegation prompt (see
[docs/hooks/orchestrator-query.md](docs/hooks/orchestrator-query.md) and ADR-0005).

## Quickstart

Prerequisites: Go 1.26+, a running Docker daemon (tests boot a real ephemeral
Dragonfly), and a Dragonfly instance for the daemon itself.

```sh
# One-time: install the protobuf/Connect codegen tools.
make setup

# Generate code and run the green gate (build, vet, tests).
make gate

# Run a Dragonfly for the daemon.
docker run -d -p 6379:6379 docker.dragonflydb.io/dragonflydb/dragonfly:latest

# Build and run the daemon.
go build -o bin/concord ./cmd/concord
go build -o bin/concord-hook ./cmd/concord-hook
CONCORD_DRAGONFLY_ADDR=127.0.0.1:6379 ./bin/concord
```

Then wire the hooks: put `concord-hook` on your `PATH` and merge
[docs/hooks/settings.sample.json](docs/hooks/settings.sample.json) into your
Claude Code settings. The daemon stops cleanly on Ctrl+C / SIGTERM.

### Configuration

| Env var | Default | Meaning |
|---|---|---|
| `CONCORD_ADDR` | `127.0.0.1:8973` | Daemon listen address (loopback only). |
| `CONCORD_DRAGONFLY_ADDR` | `127.0.0.1:6379` | Dragonfly address. |
| `CONCORD_INTENT_TTL` | `600s` | Silence window before an untouched intent record expires. |
| `CONCORD_RECORD_READS` | unset | Set truthy (on the hook env) to also record *read* footprint — the opt-in for exploration dedup. Off by default. |

## Scope

concord owns only in-flight coordination state. Out of scope by design: leases,
TTL-based locking, fencing, deadlock detection; durable memory and cross-session
retrieval (Hindsight's job); multi-node or cross-machine coordination (one daemon
per machine); and blocking on intent overlap in any form.

## Project docs

- [SPEC.md](SPEC.md) — the full specification.
- [CONTEXT.md](CONTEXT.md) — the domain glossary.
- [docs/adr/](docs/adr/) — architecture decision records.
- [DECISIONS.md](DECISIONS.md) — the running implementation-decision log.
- [docs/budgets.md](docs/budgets.md) — measured performance budgets.
- [TICKETS.md](TICKETS.md) — the implementation tickets.
