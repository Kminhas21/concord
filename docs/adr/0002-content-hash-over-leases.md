# Content hash for correctness, not leases or a claim registry

The version check compares a file's current content hash against the holder's recorded read-hash at edit time; it holds no leases, TTLs, or fencing tokens. We rejected pessimistic leases and a path-claim registry because both assume every writer opts in, and the top-ranked real failure — an agent editing against a file the human's own editor just changed — involves a writer that structurally cannot call a hook. A content hash is writer-agnostic: the editor calls nothing, but it moves the hash, so the holder is still caught.

## Consequences

- No liveness detection, expiry, or reaper is needed, because nothing is held on behalf of a process that can't be observed.
- The guarantee is scoped to writes that pass through a hooked edit tool; out-of-band writes (shell commands, the human's editor) are reconciled separately — `CheckEdit` compares against the live on-disk hash, and a `PostToolUse` git-status sweep advances the writing actor's own read-hash (see the T02 spike, `docs/spikes/filechanged.md`).
