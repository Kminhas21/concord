# The shell reconcile sweep attributes writes by content delta, not tree dirtiness

The T02 spike closed the Bash hole with a `PostToolUse` `Bash|PowerShell` sweep that ran `git status --porcelain` and called `ReconcileFileChange` for **every** dirty file (ADR-0002, SPEC "Bash-hole mechanism"). `git status` reports the whole working tree, not what the just-run command wrote, so the sweep could not tell an actor's own shell write from a file a **human's editor** had dirtied out of band. It reconciled both — advancing the actor's read-hash onto a change it never made, so the actor's next edit of that file was allowed and silently clobbered the human's uncommitted work. That defeats User Story 3, the top-ranked failure the whole design exists to prevent, whenever a shell command runs between the human's edit and the agent's edit. The spike had claimed this case was "covered, not a gap"; it was not.

## Decision

Attribute a shell command's writes by **content delta across the command**, not by tree dirtiness after it. A `PreToolUse` `Bash|PowerShell` hook snapshots the hashes of every currently-dirty file to a per-actor temp file just before the command runs; the `PostToolUse` sweep re-hashes the dirty tree and reconciles only the paths whose hash **appeared or changed** since the snapshot (`hook.ReconcileTargets`). A file already dirty and left untouched by the command has an equal before/after hash and is excluded, so a foreign out-of-band edit is never reconciled to the acting actor.

- The attribution is a pure function (`internal/hook.ReconcileTargets`), unit-tested at the seam; the snapshot filename is a pure, per-actor, filesystem-safe derivation (`internal/hook.SnapshotName`). Only the git call and temp-file I/O live in the thin client.
- **Correctness-first fallback:** with no snapshot (the `PreToolUse` shell hook was not wired, or git failed at snapshot time) the sweep reconciles **nothing**. The cost is a possible false self-block on the actor's own shell write — an efficiency loss the version check already tolerates — never a clobber.

## Consequences

- T05's goal is preserved: an actor editing a file it rewrote via its own shell command is still not falsely blocked (the file's hash changed during the command, so it is reconciled).
- The hook surface gains a `PreToolUse` `Bash|PowerShell` snapshot; the client is now stateful across a command's two hook spawns (one temp file per actor, deleted on the post sweep). This is a deliberate step away from the fully stateless "dumb client," justified by the correctness hole it closes.
- The residual gap shrinks to a human editing a file **during** the agent's shell command's execution window — far narrower than "any dirty file at post time," and still best-effort as the guarantee permits. Non-git directories remain outside the guarantee, unchanged.
- Added latency: two `git status` runs plus hashing the dirty set per shell command. The dirty set is small and shell calls are not the hot path (budgets: spawn dominates), so this stays a negligible fraction of turn time.
