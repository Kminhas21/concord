# Spike: can `FileChanged` close the Bash hole? (T02)

**Question.** The version check (Layer 1) only sees writes that pass through a hooked edit tool. Shell commands (`sed -i`, `go fmt`, build scripts) and the human's own editor write files the edit hook never sees. Can the `FileChanged` hook close that gap, and can we depend on it?

**Method.** Read the official Claude Code hooks guide and reference (`code.claude.com/docs/en/hooks-guide.md`, `.../hooks.md`). The reference `#filechanged` schema section was not retrievable (page truncated in conversion); findings below are from the guide plus the reference's lifecycle/matcher tables. No live probe was run — the findings redirect us off `FileChanged`, so its exact schema stopped being load-bearing.

## Findings

1. **`FileChanged` does fire for out-of-band writes.** The guide: *"To run a hook when a specific file changes on disk, whatever wrote it, use a FileChanged hook"* and *"reformat a specific file however it changes, including when a `Bash` command rewrites it."* So Bash-caused and external-editor writes both trigger it. ✅

2. **It is filename-scoped, not "any file".** `FileChanged` takes a `matcher` of literal filenames (`.env|.envrc`) or a dynamic `watchPaths` output. It is designed to watch a known set of files, not every source file an agent might touch. Watching an arbitrary, growing edit surface would mean continuously maintaining `watchPaths`. ⚠️

3. **Timing is unconfirmed and probably asynchronous.** The docs never state that `FileChanged` fires *before the next tool call*, and they explicitly document async hooks as a feature. A disk watcher reacting to an OS event is by nature asynchronous, so we must assume it can **race** the next `CheckEdit`. For a correctness layer, an unbounded race is disqualifying. ❌

4. **Exact input field for the changed path is unconfirmed** (reference `#filechanged` unavailable). Not load-bearing given the redirect below.

## Decision: don't build on `FileChanged`. Reconcile two ways instead.

**(a) Live-hash at check time.** `CheckEdit` compares the holder's stored read-hash against the file's **current on-disk hash, read at check time** — not against a separately stored "canonical hash." This makes *any* out-of-band write (Bash, editor) automatically visible to every *other* holder with zero reconciliation: their stored read-hash simply won't match live disk. This alone protects the victim case.

**(b) Synchronous git-status sweep for the writer's own edits.** The remaining case is an actor that writes out-of-band and then wants to edit *its own* change without a false block. Close it with a `PostToolUse` hook matching `Bash|PowerShell` that runs `git status --porcelain` to list the files the shell just wrote, and calls `ReconcileFileChange(actor_id, path, new_hash)` to advance **that actor's** read-hash. `PostToolUse` fires after the Bash call and before the next tool call (standard synchronous hook behaviour), so there is no race.

## Residual gap (accepted, already permitted by the guarantee)

- **Non-git working directories:** `git status --porcelain` yields nothing, so an actor's own shell writes there are not reconciled — the actor may hit a false block and must re-read. Efficiency loss, not a correctness loss.
- **A human editor change *between* two of an agent's calls** is caught by live-hashing at `CheckEdit` (case a), so it is covered, not a gap.
- Anything neither hook observes remains outside the guarantee, as `SPEC.md` already states.

## Impact on tickets

- **T05** changes from "`ReconcileFileChange` driven by `FileChanged`" to "`ReconcileFileChange` driven by a synchronous `PostToolUse` `Bash|PowerShell` git-status sweep; `CheckEdit` live-hashes on-disk content." Same RPC (`ReconcileFileChange`), different, more reliable driver.
- **T03** gains a decision: `CheckEdit` computes the current hash from disk at check time (see DECISIONS).
- **T09** drops the `FileChanged` hook client; adds the `Bash|PowerShell` `PostToolUse` sweep.
