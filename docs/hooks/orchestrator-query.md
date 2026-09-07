# The intent read path is an orchestrator query, not a hook

concord's intent registry has a write path and a read path. The **write path**
is hooked (`SubagentStart` → `RegisterPredicted`, `PostToolUse` → `AppendActual`).
The **read path** is not.

Answering "is anyone already doing this?" must reach the *subagent's* context
before it starts work — but the `SubagentStart` hook cannot inject context into
the subagent (it does not support `hookSpecificOutput.additionalContext`; see
ADR-0005). So the orchestrator does the read itself:

1. Before delegating a piece of work, the orchestrator calls `QueryIntent` with
   the proposed intent text and paths.
2. It reads back the candidate intents and their `path_overlap` / `divergent_paths`
   flags, judges semantic overlap itself (ADR-0006), and folds any warning
   ("agent X is already touching src/auth") into the delegation prompt.

There is no `concord-hook query` subcommand: the query belongs to whoever is
about to partition and delegate work, not to a lifecycle hook.

## Calling QueryIntent

`QueryIntent` is a normal Connect RPC on the daemon; it speaks JSON over HTTP, so
an orchestrator in any language can call it:

```sh
curl -sS \
  -H 'Content-Type: application/json' \
  -d '{"intent_text":"refactor auth","paths":["src/auth/login.go"]}' \
  http://127.0.0.1:8973/concord.v1.CoordinationService/QueryIntent
```

The response lists active intents, each with `pathOverlap` and any
`divergentPaths`. The orchestrator decides what to do with them; concord never
denies.

**Use repo-relative paths.** concord keys footprints on repo-relative,
forward-slash paths (the hook client canonicalizes with `git rev-parse
--show-toplevel`, case-folded on Windows/macOS). Query with the same form —
`src/auth/login.go`, not an absolute path — or overlaps will silently miss.
