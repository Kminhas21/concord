# Networked concord for hosted mode (topology-only reversal of ADR-0007)

ADR-0007 established one concord daemon per machine, reachable only over loopback: the hook client makes a single localhost RPC, and concord is never networked. That remains exactly right for solo/local mode. This ADR reverses it **for hosted mode only, and only in topology**: when concord runs as a container on Kubernetes, it binds `0.0.0.0` behind a cluster-internal Service so in-cluster clients (CI agents) and, later, an opt-in external path can reach it. One **coordination unit** — one concord Deployment plus its own single Dragonfly — serves one team namespace.

Nothing about correctness changes. The two independent layers (ADR-0001), the content-hash version check and its guarantee, the no-reaper design (ADR-0002), the fail-open behavior when Dragonfly is unreachable, and Dragonfly-as-shape-not-scale (ADR-0004) are identical in both modes. What changes is the listen address (`CONCORD_ADDR` binds a routable interface instead of loopback) and the process topology (a Deployment of N stateless concord replicas sharing one Dragonfly, instead of a single local process). concord's Go code already supports this via `CONCORD_ADDR` and `CONCORD_DRAGONFLY_ADDR`; hosted mode is a packaging and deployment concern, not a logic change.

We reverse only what hosted mode forces. We do **not** introduce cross-machine or multi-node coordination: a unit still owns a single Dragonfly, and coordination state is never shared across units. Scaling concord horizontally is safe precisely because all replicas are stateless and consult the one Dragonfly; adding a second Dragonfly would be a different, out-of-scope design.

## Consequences

- Solo/local mode is untouched: `docker run` / a local binary still binds loopback, one daemon per machine. The ADR-0007 non-negotiable holds wherever concord is not deployed as a networked unit.
- Hosted mode's guarantee is the same guarantee, with a larger blast radius: it now covers every agent that reaches the unit's Service, not just agents in one local session. The version check still fails open if Dragonfly is down or a file can't be hashed.
- A concord replica is stateless and horizontally scalable within a unit; the unit's identity and data live in its single Dragonfly StatefulSet.
- Authentication, tenant scoping, network isolation, and an HTTP health/readiness endpoint are **not** introduced here — hosted mode initially relies on cluster-internal networking and TCP-socket probes. Those hardening steps are deliberately deferred to the team-mode piece.
- Reversing ADR-0007 for hosted mode is the load-bearing precondition for every later platform piece (the operator, EKS, GitOps): they all deploy this networked unit.
