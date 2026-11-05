# The TeamCoordinator operator

Pieces #2 and #3 made a coordination unit installable with Helm. This ADR adds a namespaced `TeamCoordinator` custom resource and a controller-runtime **operator** that reconciles each one into that unit, so provisioning a team is `kubectl apply` of one small object. No coordination logic changes (ADR-0001, ADR-0002): the operator provisions and heals the unit, it never takes part in a version check or the intent registry.

## Decisions

- **Typed builders plus server-side apply, not an embedded chart.** The operator builds Deployment, Services, StatefulSet, NetworkPolicy and token Secret as Go values and applies them with a single field manager. Rendering Helm in-process would drag a template engine and release state into a controller, and make unit tests depend on a renderer. The cost is two descriptions of one unit.
- **The chart stays, as the no-operator path, locked by a parity test.** `deploy/helm/concord` is the shipped reference for clusters without the operator. A Go test renders it with `helm template` and compares images, ports, env names and secretKeyRef targets, probes, Service ports, selectors and the NetworkPolicy against the builders, so the two cannot drift silently. Known deltas are deliberate: the operator always sets `CONCORD_TOKEN`, never sets NATS env, applies fewer labels, and names the Dragonfly objects `<cr>-dragonfly` (the chart says `<release>-concord-dragonfly`). The test skips when `helm` is absent; the CI Linux gate installs it.
- **Garbage collection, not a finalizer.** Every child carries a controller ownerReference, so deleting the CR deletes the unit. A finalizer is added only when the unit owns something outside its namespace (an ALB or DNS record in piece #5); until then it would only add a way to wedge deletion.
- **A label-filtered Secret cache.** The manager caches only Secrets labelled `app.kubernetes.io/managed-by=concord-operator`, so a cluster-wide operator never holds other teams' Secrets in memory. User-supplied `secretRef` Secrets and any unlabelled pre-existing `<cr>-token` are read through the uncached API reader, on demand.
- **Default images track the operator.** The concord image defaults to the operator's own version (`--concord-image`); `spec.image` overrides it. Dragonfly is pinned by tag, `v2.1.0`, in both the operator flag default and the chart's `dragonfly.tag`, never `latest`; the parity test fails if they diverge.
- **Persistence is immutable.** `dataStore.persistence` and `storage` cannot change after creation (CEL `self == oldSelf`); StatefulSet volumeClaimTemplates cannot be edited in place, so changing them means delete and recreate.
- **The team token is generated once and never overwritten.** `<cr>-token` is created only when absent; its SHA-256 on the pod template rolls the pods when it changes. Rotation is deleting the Secret. Zero-gap rotation stays deferred (ADR-0011).

## Consequences

- Correctness is untouched: the version check never depended on the operator, and the intent registry still never blocks. A stopped operator leaves running units serving.
- The unit's behavior is defined in two places. The parity test catches drift in the fields it compares, not in every field; a change to the chart or builders should extend the test.
- The operator holds cluster-wide RBAC to create workloads and read Secrets; it has no delete verb, relying on GC.
