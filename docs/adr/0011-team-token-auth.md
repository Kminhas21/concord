# Team-token auth for hosted mode

ADR-0010 put concord on a cluster network with no authentication, deferring it to the team-mode piece. This ADR adds the minimum: a **team token**, a static bearer secret shared by every client of one **coordination unit**. When the daemon is started with `CONCORD_TOKEN`, every RPC must carry `Authorization: Bearer <token>`; a missing or mismatched token is answered `CodeUnauthenticated`. When `CONCORD_TOKEN` is unset, no interceptor is installed and solo/local mode is byte-for-byte unchanged.

The check is a Connect interceptor installed **outermost**, so a rejected call never reaches coordination or the RPC-duration histogram. The comparison is constant-time. The health probes (`/healthz`, `/readyz`) are plain HTTP handlers outside the Connect chain, so the kubelet needs no credential.

## Decisions

- **Static per-unit bearer.** One token per coordination unit, delivered as a Kubernetes Secret. The chart never generates it.
- **Unset = off.** Auth is opt-in so the local daemon and existing deployments keep working; the chart warns when it is off.
- **The hook client fails open, loudly, on rejection.** A rejected `CheckEdit` prints `concord: auth rejected — edits UNPROTECTED, check CONCORD_TOKEN` and exits 0. This is the same stance as an unreachable daemon: the version check is enforced whenever concord is reachable and authorized, and is never a reason to wedge an agent's session. A misconfigured token therefore degrades protection (visibly) rather than halting all work; failing closed would turn a config typo into an outage for every agent in the unit.
- **Probes exempt.** Liveness and readiness must work without secrets, and they expose nothing but up/down.
- **A metric, not a domain event.** Rejections increment `concord_auth_rejected` and emit a rate-limited log line. Domain events record coordination decisions (ADR-0009), and an unauthenticated request's actor id is untrusted, so it must not enter the event feed. The presented token is never logged.
- **Single token; rotation is a Secret update plus a rollout restart.** Zero-gap rotation (several accepted tokens, or a hot-reloaded file) is deferred to the operator piece, which owns credential lifecycle.

## Consequences

- This is a shared-secret perimeter, not identity: any holder of the token can act as any actor. It stops accidental and drive-by cross-talk inside a cluster, not a hostile insider.
- Traffic is plain HTTP in-cluster; TLS terminates at the ALB (a later piece). The token is only as private as the network path until then.
- Upgrades, not part of this ADR: **mTLS** between client and daemon for per-client identity, or **OIDC at the ALB** for external callers. Either layers on top without changing the version check.
- Correctness is untouched: the version check never depended on auth, and the intent registry still never blocks (ADR-0001, ADR-0002).
