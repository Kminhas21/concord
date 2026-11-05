# concord

The coordination service for concurrent coding agents. It runs two independent layers: a mandatory, blocking **version check** that stops stale edits, and an advisory **intent registry** that reduces duplicated work. It is a glossary of the domain, not a spec — implementation lives in `SPEC.md` and the ADRs.

## Language

**concord**:
The standalone service that coordinates concurrent coding agents through the version check and the intent registry. Backed by Dragonfly.
_Avoid_: lease manager, lock service, coordinator daemon

**Actor**:
Any writer that can modify a file: a subagent, a top-level session, a human's editor, or a shell process. The version check is deliberately blind to which kind an actor is.
_Avoid_: client, participant, process

**Actor id**:
The identity a holder's state is keyed on: `agent_id` for a subagent, `session_id` for a top-level session. Human editors and shell processes have no actor id — they record nothing but still move file hashes.
_Avoid_: session, client id

**Holder**:
An actor that has recorded a read-hash for a path, and is therefore subject to the version check on that path. Only actors that read through a hooked tool become holders; editors and shells never do.
_Avoid_: owner, locker, lease-holder

**Read-hash**:
The content hash of a file recorded at the moment a holder last read it. The version check compares this against the file's current hash.
_Avoid_: version, checksum, lease token

**Version check**:
Layer 1. The mandatory, blocking comparison of a file's current hash against the holder's read-hash, performed at edit time. A mismatch means the holder is working from stale content and the edit is refused.
_Avoid_: lock, lease, lease check, claim check

**Stale edit**:
An edit-tool call whose holder read-hash no longer equals the file's current hash. Always blocked; the holder is told to re-read and retry.
_Avoid_: conflict, dirty write

**Intent registry**:
Layer 2. The advisory store of what each actor intends and is doing. It answers "is anyone already doing this?" and reports who and what. It never blocks a tool call.
_Avoid_: claim registry, lock table, work queue

**Predicted footprint**:
The intent record written once at subagent start from the delegation prompt. Requires no cooperation from the model.
_Avoid_: claim, reservation

**Actual footprint**:
The intent record appended as an actor touches paths. Divergence from the predicted footprint is the primary signal.
_Avoid_: claim, usage log

**Divergence**:
The gap between an actor's predicted and actual footprint. When real work leaves predicted scope, that gap names the coupling the partition missed.

**Overlap**:
Two actors' intents or path tokens intersecting. Reported to the asker, never denied.
_Avoid_: conflict, collision

**Touch**:
An actor accessing a path — every write, and reads only in exploration mode. A touch refreshes the actor's intent record TTL.

**Expire on silence**:
The intent-registry TTL policy: a record expires after its configured silence window (default 10 minutes) elapses with no touch. Expiry degrades dedup efficiency only, never correctness.
_Avoid_: lease expiry, timeout

**Orchestrator**:
The parent agent that partitions work and delegates to subagents. It queries the intent registry before delegating, and it owns the verification pass. concord informs the orchestrator; it does not act for it.
_Avoid_: coordinator, master

**Verification pass**:
The compiler-and-test run the orchestrator performs on the merged result, which makes an omitted change loud instead of silent. It is a precondition concord's safe-scope guarantee depends on, not a concord feature.
_Avoid_: validation, CI (too general)

**Domain event**:
A structured record concord emits on each coordination decision (`edit_blocked`, `overlap_reported`, `intent_expired`, …), carrying the actor, paths, and — where relevant — a reason and the decision latency. Domain events are observation only: they feed the live event feed and metrics, and never influence a verdict (ADR-0009).
_Avoid_: log line (too general), message (ambiguous with RPC)

**Emitter**:
The narrow seam the coordination service publishes domain events through. Every Emitter is non-blocking and best-effort: production uses an asynchronous, NATS-backed emitter that drops events (and counts the drops) under backpressure rather than ever slowing or failing a decision (ADR-0009).
_Avoid_: publisher (reserve for the internal drain), bus

**Coordination unit**:
One concord Deployment plus its own single Dragonfly, serving one team namespace. Coordination state is never shared across units (ADR-0010).
_Avoid_: cluster, tenant, instance

**TeamCoordinator**:
The namespaced custom resource (`concord.kminhas21.github.io/v1alpha1`, short name `tc`) that declares one team's coordination unit. The operator reconciles each one into that unit's Deployment, Dragonfly, Services, NetworkPolicy, and team token Secret (ADR-0012).
_Avoid_: tenant, team CRD

**Operator**:
The controller-runtime controller that reconciles every TeamCoordinator into its coordination unit and reports its status; it provisions and heals the unit but never takes part in coordination. Distinct from the *orchestrator* (the agent that dispatches work).
_Avoid_: controller (alone), provisioner

**Hosted mode**:
concord run as a networked container behind a cluster-internal Service, serving every agent that can reach its coordination unit. Same guarantee as solo/local mode with a larger blast radius (ADR-0010).
_Avoid_: cloud mode, server mode

**Solo/local mode**:
One daemon per machine, reachable only over loopback, no authentication (ADR-0007).
_Avoid_: standalone mode

**Team token**:
The static bearer secret a coordination unit's clients present as `Authorization: Bearer <token>` when the daemon has `CONCORD_TOKEN` set. A shared perimeter, not an identity: it does not distinguish actors (ADR-0011).
_Avoid_: lease token, API key
