# concord is a long-lived local daemon reached by RPC

concord runs as a single long-lived process on the developer's machine; each hook invocation spawns a thin compiled client that makes one RPC to that daemon over localhost. We chose a persistent daemon over spawning the full service per hook because the measured cost is dominated by process spawn (~11ms for a compiled binary) rather than the datastore (sub-millisecond local Dragonfly), so keeping the service warm and the per-hook client minimal is where the latency budget is won. RPC (rather than a REST/JSON surface) is the primary interface so the same typed contract serves both the hook clients and the test seam.

## Consequences

- The transport is localhost TCP on a configurable port (chosen over a Unix socket for cross-platform reliability on Windows).
- This is out of scope for multi-node or cross-machine coordination by design; there is exactly one daemon per machine.
