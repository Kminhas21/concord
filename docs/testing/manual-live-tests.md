# Manual live tests — concord against real agents

These are hands-on tests to run in a **separate Claude Code session** with real subagents,
to see concord behave (and fail) under genuine agent traffic. The automated harness
(`go test -tags stress ./test/`) covers the store-level races; this file covers what only
real agents exercise.

## 0. Setup (once)

```sh
# Dragonfly + daemon
docker run -d -p 6379:6379 docker.dragonflydb.io/dragonflydb/dragonfly:latest
go build -o bin/concord ./cmd/concord && go build -o bin/concord-hook ./cmd/concord-hook
CONCORD_DRAGONFLY_ADDR=127.0.0.1:6379 ./bin/concord      # leave running in a terminal
```

Put `bin/` on `PATH` (so `concord-hook` resolves) and merge
`docs/hooks/settings.sample.json` into the Claude Code settings of a **scratch repo** you
don't mind editing. Open that repo in a new Claude Code session for the tests below.

### Observability (keep these open)

- **Daemon log** — the terminal running `./bin/concord`.
- **Live keys:** `docker exec <container> redis-cli --scan` and
  `... redis-cli monitor` to watch every command as agents work.
- **Query the registry any time:**
  ```sh
  curl -sS -H 'Content-Type: application/json' \
    -d '{"intent_text":"x","paths":["src/auth/login.go"]}' \
    http://127.0.0.1:8973/concord.v1.CoordinationService/QueryIntent | jq
  ```

## 1. Stale-edit block (correctness, single agent)

1. Ask the agent to **Read** `foo.go`.
2. In a terminal, change `foo.go` yourself (`echo x >> foo.go`).
3. Ask the agent to **Edit** `foo.go`.

**Expect:** the edit is blocked (exit 2) with "changed since you last read it; re-read and
retry"; the agent re-reads and proceeds. **Watch for:** if you changed the file via a path
with different casing than the agent used, the block message may say "no recorded read"
instead — that is finding **D**.

## 2. Concurrent subagents on the same file (the core scenario)

Ask the orchestrator to spawn **two subagents in parallel**, both told to edit the *same*
function in the *same* file (e.g. "add a doc comment to `Handler.Serve`").

**Expect:** whichever writes second is blocked on a stale hash and re-reads. **Watch for:**
the check-to-write **TOCTOU** window — if both `CheckEdit`s pass before either writes, the
second write can still land on top. Run it several times; concord narrows this race but does
not close it. Note how often (if ever) you see a silent overwrite.

## 3. Pre-delegation dedup (advisory)

1. Spawn subagent A with a delegation prompt naming files: "refactor `src/auth/login.go`".
2. Before spawning B, have the orchestrator call `QueryIntent` (curl above) with the paths it
   is about to give B.

**Expect (per the design):** the query reports A as overlapping. **Watch for finding B:** if
the orchestrator queries with **absolute** paths while A's predicted footprint is the
**relative** path from its prompt, `pathOverlap` is `false` and dedup misses. Try the query
with both relative and absolute paths and compare — this is the clearest way to see the
path-scale mismatch live.

## 4. Predicted-vs-actual divergence

Give a subagent a prompt scoped to `src/auth/**`, then have it actually edit
`src/db/schema.go`. Query the registry.

**Expect:** `divergentPaths` lists `src/db/schema.go`. **Watch for:** because of finding B,
if predicted is relative and actual is absolute, *everything* shows as divergent — so
divergence is noisy until path scales are unified.

## 5. Shell reconcile (Bash hole)

In a **git** scratch repo: have the agent Read `x.go`, then run `gofmt -w x.go` via **Bash**,
then Edit `x.go`.

**Expect:** the edit is allowed — the `PostToolUse` git-status sweep reconciled the agent's
own shell write. Now repeat in a **non-git** directory: the sweep finds nothing, so the agent
is falsely blocked and must re-read (accepted efficiency gap).

## 6. Fail-open (availability)

Stop the daemon (Ctrl+C). Ask the agent to edit a file.

**Expect:** the edit proceeds (exit 0) with a stderr warning "version check unavailable" —
concord never bricks editing. Restart the daemon and confirm protection resumes.

## 7. Long-step TTL (finding G)

Set a short window: restart the daemon with `CONCORD_INTENT_TTL=15s`. Have a subagent
register intent, then sit idle > 15s (or run one long tool call), then query.

**Expect:** the record is gone — an active-but-slow agent stops deduplicating. Decide whether
to raise the default or refresh the record on every tool call.

## 8. Footprint under load (finding A)

Have one subagent touch many files quickly (e.g. "add a trailing newline to every file under
`pkg/`"). Query its footprint.

**Expect (bug):** far fewer paths than files touched — concurrent `AppendActual` loses writes.
This is the most impactful bug to fix; the automated harness reproduces it deterministically
(`go test -tags stress ./test/ -run TestStress/A`).

---

## How to test "accurately" against live agents — method

1. **One scratch repo, real subagents.** Use the Task tool to spawn 2–4 subagents with
   overlapping file scopes; that is the only way to exercise concurrency, TOCTOU, and dedup.
2. **Three observers, always on:** daemon log (decisions), `redis-cli monitor` (every store
   op), `QueryIntent` via curl (registry state). A finding you can't see in one of these
   isn't confirmed.
3. **Vary the axis you're probing:** same file vs same dir vs different dir; relative vs
   absolute paths; matching vs mismatched casing; fast vs slow steps; daemon up vs down.
4. **Repeat racy tests** (2 and the TOCTOU note) 10+ times — races are probabilistic.
5. **Record outcomes against `stress-findings.md`** so a fix can be checked against the same
   scenario.
