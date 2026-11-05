# SPEC: the concord operator (piece #4 of the platform flagship)

**Status:** ready-for-agent. Fourth piece of the "local → platform" flagship. Adds a namespaced `TeamCoordinator` custom resource and a controller-runtime operator that reconciles each one into a running **coordination unit** — the same object set the piece-#2/#3 chart renders. Tracks issue #10. Local-testable (envtest + kind); no cloud. Terms are defined in `CONTEXT.md`; respects ADR-0001/0002 (logic unchanged), ADR-0009 (observability best-effort), ADR-0010 (one unit per team), ADR-0011 (team-token auth). Records **ADR-0012** (the operator).

## Problem Statement

Pieces #2 and #3 made a coordination unit installable and safe to share (`helm install` with a token Secret, HTTP probes, a Dragonfly NetworkPolicy). But provisioning a team is still a human assembling values, creating a token Secret by hand, and running Helm; nothing notices when someone edits a team's Deployment, nothing reports a team's health in one place, and tearing a team down is manual. For GitOps teams-as-code (piece #6) a team must be **one declarative Kubernetes object** that a controller turns into — and keeps as — a working unit.

## Solution

A `TeamCoordinator` CRD (`concord.kminhas21.github.io/v1alpha1`, namespaced, short name `tc`) and an operator built directly on controller-runtime (no kubebuilder layout; `controller-gen` generates the CRD, deepcopy, and RBAC).

For each `TeamCoordinator`, the operator **server-side applies** typed objects into the CR's namespace, each owned by the CR via an ownerReference:

- `<cr>-concord` Deployment (concord, `spec.replicas`, HTTP probes, `CONCORD_TOKEN` from the token Secret) and Service (rpc 8080, metrics 9464)
- `<cr>-dragonfly` StatefulSet (1 replica, optional PVC) and Service (6379)
- `<cr>-dragonfly` NetworkPolicy (Dragonfly ingress only from this unit's concord pods) — always on
- `<cr>-token` Secret — generated once (32 random bytes) unless `spec.auth.secretRef` names an existing one

It reports `Ready`/`Progressing` conditions, the in-cluster endpoint, and ready replicas in `status`. Deleting the CR deletes the stack through Kubernetes garbage collection — **no finalizer**, because the unit owns nothing outside its namespace yet.

No coordination logic changes. The chart remains the install path for clusters without the operator; a parity test keeps the two from drifting.

## User Stories

1. As a platform engineer, I want a `TeamCoordinator` resource that fully describes a team's coordination unit, so that a team is one declarative Kubernetes object.
2. As a platform engineer, I want applying a `TeamCoordinator` to produce a working unit (concord, Dragonfly, Services, NetworkPolicy, token Secret) in its namespace, so that I never hand-assemble per-team deployments.
3. As a platform engineer, I want several `TeamCoordinator`s to coexist in one namespace, so that object names never collide (`<cr>-…` prefixes).
4. As a platform engineer, I want someone's manual edit to a managed field (e.g. Deployment replicas) corrected automatically, so that declared state is the real state.
5. As a platform engineer, I want the operator to react to edits of the objects it owns immediately, not on a timer, so that drift is corrected in seconds.
6. As a platform engineer, I want fields set by other actors left alone, so that the operator only fights over what it owns (server-side apply field ownership).
7. As a platform engineer, I want `kubectl get tc` to show Ready, Replicas, Endpoint, and Age, so that I can see every team's health at a glance.
8. As a platform engineer, I want `Ready=True` only when all concord replicas are available and Dragonfly is ready, so that Ready means "can actually coordinate".
9. As a platform engineer, I want `Progressing` while the unit is converging and `observedGeneration` on status, so that I can tell a stale status from a current one.
10. As a platform engineer, I want `status.endpoint` (`<cr>-concord.<ns>.svc.cluster.local:8080`), so that CI agents know where to point `CONCORD_ADDR`.
11. As a team lead, I want the operator to generate my team token as a Secret when I don't supply one, so that a new team is authenticated by default.
12. As a team lead, I want the generated token never overwritten by later reconciles, so that clients holding it keep working.
13. As a team lead, I want to rotate the token by deleting the Secret, so that the operator regenerates it and concord pods roll onto the new value (token hash on the pod template).
14. As a security-minded lead, I want to supply my own Secret via `spec.auth.secretRef`, so that the credential can come from an external secret manager.
15. As a security-minded lead, I want a missing `secretRef` Secret to block the unit with `Ready=False, reason=SecretNotFound` and a Warning Event, so that a unit is never started with auth silently off.
16. As a security-minded lead, I want the operator's cache to hold only Secrets it generated (label-filtered), and read a `secretRef` Secret with one uncached GET, so that the operator doesn't mirror every Secret in the cluster.
17. As a platform engineer, I want `replicas`, `intentTTL`, `resources`, and `image` settable per team with sane defaults (1, 600s, empty, the operator's own version), so that most teams need only `repo`.
18. As a platform engineer, I want invalid specs (replicas < 1, a non-duration TTL) rejected by the apiserver, so that bad config never reaches the reconcile loop.
19. As a platform engineer, I want changing `dataStore.persistence`/`storage` after creation rejected with a clear message, so that I don't hit an immutable-StatefulSet reconcile loop (delete + recreate instead).
20. As a platform engineer, I want the default concord image to track the operator's version, so that operator and daemon never disagree about probes or env; a team can pin `spec.image`.
21. As a platform engineer, I want Dragonfly pinned to a specific version by default (overridable per CR via `dataStore.image`), so that units don't drift across Dragonfly versions on pod restarts.
22. As a platform engineer, I want deleting a `TeamCoordinator` to remove its whole stack, so that de-provisioning a team leaves nothing behind.
23. As a platform engineer, I want the operator installed from its own small chart (CRD, ClusterRole, ServiceAccount, Deployment with leader election), so that Argo CD can deploy it in piece #6.
24. As a platform engineer, I want the operator to expose controller-runtime's metrics and health/readiness probes, so that it is observable like any controller.
25. As a maintainer, I want the reconcile behavior tested against a real kube-apiserver (envtest), so that tests prove what the operator produces, not its helpers.
26. As a maintainer, I want a parity test comparing the operator's objects with the chart's render for equivalent values, so that the two install paths can't silently drift.
27. As a maintainer, I want `make operator-test` on kind to prove a CR becomes a unit that rejects a bad token, blocks a stale edit, allows a matching one, and disappears on delete, so that the operator is grounded end to end.
28. As a maintainer, I want the operator design recorded as ADR-0012 and *TeamCoordinator*/*operator* in `CONTEXT.md`, so that the trade-offs and vocabulary are durable.

## Implementation Decisions

**Layout (same Go module).** `api/v1alpha1/` (types + `zz_generated.deepcopy.go`), `internal/operator/` (reconciler + pure builders), `cmd/concord-operator/` (manager main). Generated CRD/RBAC land in the operator chart (`deploy/helm/concord-operator/crds/`, `templates/`). `controller-gen` and `setup-envtest` are pinned via `go run`/`go install` in the Makefile; CI's "generated code is current" step is extended to controller-gen output. Pins: controller-runtime v0.25.x with its matching `k8s.io/*`; envtest's Kubernetes minor matches the kind node image `make chart-test` uses (resolved at implementation).

**API (`v1alpha1`).**
```yaml
spec:
  repo: string                    # required; informational label of the team's repo
  replicas: int32                 # min 1, default 1
  intentTTL: duration             # default 600s
  image: string                   # optional; default ghcr.io/kminhas21/concord:<operator version>
  resources: ResourceRequirements # optional
  dataStore:
    image: string                 # optional; default pinned Dragonfly (operator flag)
    persistence: bool             # default false — immutable (CEL self == oldSelf)
    storage: Quantity             # default 1Gi — immutable
  auth:
    mode: token                   # enum [token]; mtls/oidc later
    secretRef: {name, key}        # optional; key default "token"
status:
  conditions: [Ready, Progressing]  # metav1.Condition
  observedGeneration: int64
  endpoint: string
  readyReplicas: int32
```
Printer columns: Ready, Replicas, Endpoint, Age. Validation via kubebuilder markers and CEL. **Deferred fields:** `ingress` (needs the ALB controller — piece #5), ServiceMonitor (needs Prometheus Operator CRDs — piece #6/#7), NATS events URL (observability). Fixed, not fields: ports (rpc 8080, metrics 9464, redis 6379), metrics on, NetworkPolicy on.

**Builders.** Pure functions `TeamCoordinator → typed object` per child, using the chart's labels (`app.kubernetes.io/{name,instance,component,managed-by}`; `instance` = CR name). Selectors use only `instance`+`component` and never change. The concord pod template carries an annotation with a SHA-256 of the token value so a token change rolls the pods.

**Reconcile.** Get CR → resolve token (generated Secret: create if absent, never overwrite; `secretRef`: uncached GET via the manager's APIReader; missing → `Ready=False/SecretNotFound`, Warning Event, **do not apply the Deployment**, return for backoff) → server-side apply each child (one field manager `concord-operator`, `ForceOwnership`, ownerReference controller=true) → read Deployment/StatefulSet status → patch `/status` only when it changed. Watches: `For(TeamCoordinator)`, `Owns(Deployment, StatefulSet, Service, NetworkPolicy, Secret)`. No timed requeue; errors return for exponential backoff. Warning Events only (reconcile failure, missing Secret).

**Cache and RBAC.** Manager cache filters `Secret` by label `app.kubernetes.io/managed-by=concord-operator`. ClusterRole (from controller-gen markers): get/list/watch/create/update/patch on `teamcoordinators`, `teamcoordinators/status`, deployments, statefulsets, services, networkpolicies, secrets; create/patch on events. No delete (GC handles it).

**Deletion.** ownerReferences + Kubernetes GC; no finalizer. ADR-0012 records that a finalizer is added only when an out-of-namespace resource exists (ALB/DNS in piece #5).

**Chart changes.** The concord chart's Dragonfly `tag` default moves from `latest` to the same pinned version as the operator (parity). No other chart changes.

**Operator packaging (PR-2).** Second target in `deploy/docker/Dockerfile` → `ghcr.io/kminhas21/concord-operator`; GoReleaser entry; `deploy/helm/concord-operator` chart (CRD in `crds/`, ClusterRole/Binding, ServiceAccount, Deployment with `--leader-elect`, manager metrics/probes). Cluster-wide watch.

**ADR-0012 — the operator.** Typed builders + SSA (vs embedding the chart); the chart kept as the no-operator path with a parity test; ownerRef GC instead of a finalizer and when that flips; label-filtered Secret cache; default image tracks operator version; persistence immutable.

**Docs.** `CONTEXT.md` gains *TeamCoordinator* and *operator* (mapped to *coordination unit*). `platform.md`'s CRD prototype is corrected (port 8080, `Progressing`, ingress/ServiceMonitor deferred). README gains an operator section. `DECISIONS.md` dated entries per ticket. Roadmap: #4 in progress → done.

## Testing Decisions

**What makes a good test here.** Assert the objects the operator produces and the status it reports, through the Kubernetes API — never reconcile helpers.

**envtest (real kube-apiserver + etcd, Linux CI gate; skipped on the Windows gate):**
- apply a CR → Deployment (replicas, image, probes, `CONCORD_TOKEN` secretKeyRef, token-hash annotation), StatefulSet, both Services, NetworkPolicy, token Secret exist with the expected fields and controller ownerReferences.
- drift: change the Deployment's replicas → reconciled back.
- generated token is stable across reconciles; deleting it → a new one and a changed pod-template hash.
- `secretRef` set → no generated Secret, Deployment references the given Secret; missing → `Ready=False/SecretNotFound` and no Deployment.
- status: with Deployment/StatefulSet status patched to ready (envtest has no kubelet), `Ready=True`, `endpoint`, `readyReplicas`, `observedGeneration`.
- validation: replicas 0 and an edit to `dataStore.persistence` are rejected by the apiserver.
- envtest runs no garbage collector, so deletion is asserted as ownerReferences here and as real removal on kind.

**Parity test (Go, CI):** render the concord chart (`helm template` with values equivalent to a sample CR) and compare key fields with the builders' output — images, ports, env names, probe paths, NetworkPolicy selector/from-labels. Skips if `helm` is absent locally; CI has it.

**kind e2e (`make operator-test`, local/opt-in like `chart-test`):** build + `kind load` both images, install the operator chart, apply a CR, wait `Ready=True`, read the generated token, then reuse `chart-test.sh`'s helpers: no/wrong token → `unauthenticated`; good token → stale edit blocks, matching allows. Delete the CR → all `<cr>-*` objects gone.

**Prior art.** `scripts/chart-test.sh`, the helm-unittest suites, the RPC-seam tests' "assert behavior not internals" rule.

## Out of Scope

- **Ingress/ALB, Route53/ACM, finalizers** — piece #5.
- **ServiceMonitor / per-tenant Prometheus wiring** — piece #6/#7.
- **Argo CD delivery of the operator and CRs** — piece #6.
- **Scheduled/zero-gap token rotation, mTLS, OIDC** — rotation is delete-and-regenerate.
- **Dragonfly HA / replicas > 1, ElastiCache** — one Dragonfly per unit (ADR-0004).
- **Cluster-scoped CRs or operator-created namespaces.**
- **Any change to the version-check or intent-registry logic, or to what the guarantee covers.**

## Further Notes

- Build order: (a) API types + CRD/deepcopy codegen + CI currency; (b) reconciler core — builders, SSA, ownerRefs, generated token, envtest; (c) status, `secretRef`, validation/immutability, drift tests; (d) parity test + Dragonfly pin + ADR/docs; (e) operator image + chart + `make operator-test`. PR-1 = (a)–(d); PR-2 = (e).
- Dragonfly version to pin: the latest release at implementation time (v2.1 when written).
