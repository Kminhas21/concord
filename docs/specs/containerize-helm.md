# SPEC: concord containerize + Helm (piece #2 of the platform flagship)

**Status:** ready-for-agent. Second piece of the "local → platform" flagship. Packages the coordination unit as a container image and a Helm chart — the building block the `TeamCoordinator` operator (piece #4) will later template. Local-mode-testable with kind + kubeconform + helm; no cloud. Terms are defined in `CONTEXT.md`; respects ADR-0001 (two layers), ADR-0002 (no reaper), ADR-0004 (Dragonfly = shape, not scale), and **reverses ADR-0007 for hosted mode only** (new ADR, below).

## Problem Statement

concord ships today as local binaries (a daemon + hook client) and a docker-compose observability stack. There is no way to run a concord coordination unit on Kubernetes: no published container image, no packaging that a cluster can deploy, and no reproducible "one concord + its data store" unit. Without that, none of the later platform pieces — the operator, EKS, GitOps — have a deployable artifact to stand on, and a platform engineer can't demonstrate the core cloud-native packaging skills (a real image, a hand-written Helm chart, schema-validated manifests, a cluster-grounded test).

## Solution

Produce two artifacts and a grounded test gate:

1. **A canonical container image.** Promote the existing multi-stage distroless build into a first-class `deploy/docker/Dockerfile` that produces one image carrying both binaries (`/concord` default entrypoint, `/event-web` override). It is built (no push) on every PR, and published multi-arch (amd64+arm64) to **GHCR** on version tags via GoReleaser.

2. **A Helm chart — `deploy/helm/concord/`** — deploying one **coordination unit**: a stateless `concord` Deployment behind a ClusterIP Service, and a single-instance Dragonfly StatefulSet behind a headless Service, wired together through config. Hand-written and minimal, with a tight `values.yaml` surface and the standard `app.kubernetes.io/*` labels. This is the exact unit the operator will later stamp out per team.

3. **A grounded acceptance gate.** `helm lint` + `kubeconform` (validate rendered manifests against the real K8s API schema) + `helm-unittest` (assert template logic) run fast in CI; a `make chart-test` boots a **kind** cluster, `kind load`s the image, `helm install`s the chart, and drives a real `RecordRead` + stale `CheckEdit` against the in-cluster concord+Dragonfly — proving the packaged unit actually coordinates, not just renders. This is the K8s analog of `make obs-smoke`.

Crucially, this piece **only packages** — it changes no coordination logic. The one code-adjacent change is that concord now binds a network address inside a pod (it already can, via `CONCORD_ADDR`), which requires reversing ADR-0007 for hosted mode; solo/local mode is untouched.

## User Stories

1. As a platform engineer, I want a canonical `Dockerfile` that builds one concord image, so that there is a single, reproducible artifact to deploy and publish.
2. As a platform engineer, I want the image built on distroless with static binaries, so that it is small, rootless, and has minimal attack surface.
3. As a platform engineer, I want the image to carry both `concord` and `event-web` with `concord` as the default entrypoint, so that one published image serves both the coordination unit and the observability feed (entrypoint-overridden).
4. As a platform engineer, I want the image published to GHCR on version tags, so that a cluster can pull a versioned, immutable artifact.
5. As a platform engineer, I want the published image to be multi-arch (amd64 + arm64), so that it runs on both Intel and Graviton nodes without a rebuild when EKS arrives.
6. As a contributor, I want the image built (without pushing) on every PR, so that a change that breaks the build is caught before merge.
7. As a developer, I want a Helm chart that deploys one concord coordination unit (concord + Dragonfly), so that I can run concord on any Kubernetes cluster with a single `helm install`.
8. As a developer, I want the chart hand-written and minimal, so that every template is meaningful and there is no dead scaffolding to reason about.
9. As a developer, I want concord deployed as a stateless Deployment, so that it can be scaled to N replicas that all share the one Dragonfly.
10. As a developer, I want Dragonfly deployed as a single-instance StatefulSet with a headless Service, so that the data workload matches the identity/shape the operator will provision per team.
11. As a developer, I want Dragonfly storage ephemeral by default with a persistence (PVC) toggle, so that the in-flight coordination state needs no volume, but persistence is available when wanted.
12. As a developer, I want concord to reach Dragonfly by its in-cluster Service DNS, so that the two components find each other without hardcoded addresses.
13. As a developer, I want a tight `values.yaml` surface (image, concord, dragonfly, service knobs), so that I can configure the unit without editing templates.
14. As a developer, I want the image tag to default to the chart's `AppVersion`, so that the chart and the image it deploys version together.
15. As a developer, I want the intent-registry TTL configurable through values, so that I can tune expire-on-silence per deployment.
16. As a developer, I want concord's `/metrics` endpoint enabled by default in the chart (a `metrics.enabled` toggle) with a named metrics port and a small metrics Service, so that a future Prometheus/ServiceMonitor can scrape each unit.
17. As a developer, I want an optional `CONCORD_NATS_URL` values hook (off by default, pointing at an external stream), so that a unit can emit domain events without this chart having to deploy NATS.
18. As a cluster operator, I want liveness and readiness probes on concord (TCP-socket on the RPC port), so that Kubernetes restarts a wedged pod and only routes to accepting ones.
19. As a cluster operator, I want the concord container to run as a non-root user with a read-only root filesystem and dropped capabilities, so that the pod meets a restricted security posture.
20. As a cluster operator, I want standard `app.kubernetes.io/*` recommended labels and Helm-templated resource names, so that the unit's objects are consistently labeled and selectable.
21. As a cluster operator, I want resource requests/limits configurable through values for both concord and Dragonfly, so that the unit schedules predictably.
22. As a platform engineer, I want the rendered manifests validated against the Kubernetes API schema (kubeconform), so that a chart that renders invalid manifests fails before it ever reaches a cluster.
23. As a platform engineer, I want template-logic tests (helm-unittest) asserting the values toggles render correctly, so that the persistence, metrics, and events conditionals are proven to include/omit the right objects.
24. As a platform engineer, I want `helm lint` in CI, so that chart structural problems are caught on every PR.
25. As a platform engineer, I want a `make chart-test` that stands up a kind cluster, loads the local image, installs the chart, and drives a real stale `CheckEdit` that blocks, so that I have end-to-end proof the packaged unit coordinates — not just that YAML renders.
26. As a platform engineer, I want the kind e2e to run fully offline (local image via `kind load`, `pullPolicy: IfNotPresent`), so that the acceptance test needs no registry, no auth, and no network.
27. As a contributor, I want the fast chart checks (lint + kubeconform + helm-unittest) to run in CI on every PR, with the heavy kind e2e local/opt-in, so that required checks stay fast (mirroring the obs-smoke decision).
28. As a maintainer, I want the ADR-0007 reversal recorded as a new ADR scoped to topology only, so that "concord now listens on a network Service in hosted mode" is an honest, documented decision that preserves every correctness property.
29. As a solo developer, I want local/loopback mode completely unchanged, so that containerization adds a deployment option without altering how concord runs on my laptop.
30. As a hiring reviewer, I want to see a real image + a hand-written chart + schema-validated manifests + a cluster-grounded test, so that the project demonstrates authentic cloud-native packaging practice.
31. As the future TeamCoordinator operator, I want this chart to be the exact per-team unit I template, so that provisioning a team reuses a proven, tested building block rather than bespoke manifests.

## Implementation Decisions

**Canonical image.** Move the container build to `deploy/docker/Dockerfile` (out of the observability corner) as the one canonical multi-stage build: a Go build stage producing static `CGO_ENABLED=0` binaries, and a distroless `nonroot` final stage carrying `/concord` (default entrypoint) and `/event-web`. Digest-pinned bases, as today. The observability compose and the Helm chart both reference this one image. The old `deploy/observability/Dockerfile` is replaced by/redirected to it (compose `build.dockerfile` updated).

**Registry + publish.** The image publishes to **GHCR** (`ghcr.io/kminhas21/concord`). GoReleaser is extended with its docker (buildx) support to build+push a **multi-arch (amd64+arm64)** image on `v*` tags, alongside the binaries it already ships; the release workflow already has `contents: write` and gains `packages: write`. On PRs, a CI job **builds** the image (buildx, single-arch is fine) **without pushing** to catch build breaks. Local development and the kind e2e use `kind load docker-image` with a fixed local tag (`ghcr.io/kminhas21/concord:dev`) — no registry involved.

**Helm chart — `deploy/helm/concord/`.** Hand-written (not `helm create`), chart `apiVersion: v2`. It renders, for one coordination unit:

- a **concord Deployment** — stateless, `replicaCount` (default 1), image from values (`tag` defaulting to `.Chart.AppVersion`, `pullPolicy` default `IfNotPresent`), env wiring (`CONCORD_ADDR=:<port>` binding `0.0.0.0`, `CONCORD_DRAGONFLY_ADDR=<dragonfly-headless-svc>:6379`, `CONCORD_INTENT_TTL`, and — when `metrics.enabled` — `CONCORD_METRICS_ADDR=:<metricsPort>`, and when `events.natsURL` is set, `CONCORD_NATS_URL`), TCP-socket liveness/readiness probes on the RPC port, a restricted `securityContext` (runAsNonRoot, readOnlyRootFilesystem, drop ALL caps, no privilege escalation), and configurable resources;
- a **concord ClusterIP Service** exposing the RPC port (and the metrics port when enabled);
- an optional **metrics Service** (or a named metrics port on the main Service) when `metrics.enabled`, so a future ServiceMonitor can select it;
- a **Dragonfly StatefulSet** — single replica, headless Service for stable DNS, storage `emptyDir` by default with a `persistence.enabled`→PVC (`volumeClaimTemplates`) toggle and `persistence.size`, configurable resources;
- a **Dragonfly headless Service**.

No ServiceAccount/HPA/Ingress/NetworkPolicy/ServiceMonitor/Secret/auth templates — those belong to later pieces. Standard `app.kubernetes.io/{name,instance,version,component,managed-by,part-of}` labels on every object; names templated from the release + a `concord.fullname` helper.

**`values.yaml` surface (the knob set the operator will later drive):**

```yaml
image:
  repository: ghcr.io/kminhas21/concord
  tag: ""            # defaults to .Chart.AppVersion
  pullPolicy: IfNotPresent
concord:
  replicaCount: 1
  rpcPort: 8080
  intentTTL: 600s
  metrics:
    enabled: true
    port: 9464
  events:
    natsURL: ""      # off by default; external stream when set
  resources: {}
dragonfly:
  image: docker.dragonflydb.io/dragonflydb/dragonfly
  tag: latest        # pinned by digest in practice
  port: 6379
  persistence:
    enabled: false
    size: 1Gi
  resources: {}
```
(Shape from the grilling decisions; the running chart is the source of truth for exact keys.)

**ADR-0007 reversal (new ADR, recorded in this piece).** Scoped to *topology only*: solo/local mode is unchanged (one daemon per machine, loopback-only); **hosted mode** runs concord as a containerized Deployment bound to `0.0.0.0` behind a cluster-internal Service, one coordination unit per team namespace, one Dragonfly per unit. The two coordination layers (ADR-0001), the content-hash guarantee, the no-reaper design (ADR-0002), and the fail-open behavior are all identical; only the listen address and process topology change. Cross-machine / multi-node coordination remains out of scope — a unit still owns a single Dragonfly. `CLAUDE.md`'s "one daemon per machine" line is qualified accordingly, and `DECISIONS.md` links the new ADR.

**No coordination-logic change.** concord's Go code is untouched except (if needed) documentation; it already binds `CONCORD_ADDR` and reaches Dragonfly via `CONCORD_DRAGONFLY_ADDR`. Health endpoints, tenant scoping, and auth are explicitly deferred to piece #3.

**CI / make wiring.** A CI job builds the image on PRs (no push) and runs `helm lint` + `kubeconform` (over `helm template`) + `helm-unittest` — fast, no cluster. The release workflow pushes the multi-arch image to GHCR on tags. `make` gains: `image` (build the local `:dev` image), `chart-lint` (lint + kubeconform + unittest), and `chart-test` (the kind e2e). `chart-test` is local/opt-in, not on the required matrix.

## Testing Decisions

**What makes a good test here.** The chart is packaging, so tests assert *observable packaging behavior*, not template internals: (1) the manifests the chart **renders** are valid and contain the right objects under the right values, and (2) the packaged unit, once **running in a real cluster**, actually coordinates. No test inspects Helm's internal templating mechanics; they assert rendered output and live behavior.

**The seam is the rendered chart + the running cluster — the coordination assertion reuses the existing RPC seam.** Two layers, one conceptual seam:

- **Rendered-manifest layer (fast, CI).** `kubeconform` validates every object from `helm template` against the Kubernetes API schema — a chart that renders an invalid Deployment fails here. `helm-unittest` asserts template *logic* as external output: `persistence.enabled=true` renders a `volumeClaimTemplate` (and none when false); `metrics.enabled=false` omits the metrics port/Service and the `CONCORD_METRICS_ADDR` env; `events.natsURL` set renders the `CONCORD_NATS_URL` env; `image.tag=""` resolves to `AppVersion`; the `app.kubernetes.io/*` labels are present. `helm lint` catches structural problems.

- **kind e2e layer (grounded, `make chart-test`).** Boot a kind cluster, `kind load` the local image, `helm install` the chart, wait for the concord + Dragonfly rollout, `kubectl port-forward` the concord Service, then drive the coordination guarantee **through the existing Connect RPC seam** exactly as every other concord test does: `RecordRead(actor, path, v1)` then `CheckEdit(actor, path, v2)` must **block** (stale), and a matching-hash `CheckEdit` must allow. Tear the cluster down. This proves the packaged image + chart + in-cluster Dragonfly wiring genuinely coordinate.

**Prior art.** `make obs-smoke` (boots a real stack, drives real traffic, asserts, tears down) is the direct model for `make chart-test`. The existing coordination tests (drive the Connect client → assert the verdict) are the model for the e2e's assertion. The testcontainers Dragonfly harness is prior art for "test against the real data store, not a fake."

## Out of Scope

- **Everything in later pieces:** tenant scoping, per-team auth (bearer tokens/Secrets), and an HTTP health/readiness endpoint (`/healthz`/`/readyz`) — those are **piece #3** (team-mode app changes). Ingress/ALB, NetworkPolicy, ServiceMonitor CRD, the `TeamCoordinator` CRD + operator — **piece #4**. EKS/VPC/IRSA/ECR/Terraform — **#5**. Argo CD/GitOps — **#6**. k6/Toxiproxy — **#7**.
- **Publishing the chart itself** (OCI to GHCR or a gh-pages Helm repo) — the chart lives in-repo and is installed from its path; Argo CD (#6) consumes charts from git.
- **Bundling the observability stack** (NATS/Prometheus/Grafana/event-web) into this chart — the chart is the coordination unit only; observability stays piece #1's compose stack. `metrics.enabled` only exposes the endpoint for an external scraper; `events.natsURL` only points at an external stream.
- **Multi-node / cross-machine coordination** — a unit still owns a single Dragonfly (ADR-0004); horizontal Dragonfly scaling is not a concern.
- **Any change to the coordination guarantees, the version-check logic, or the intent-registry logic** — this piece only packages them.

## Further Notes

- This is the natural second piece: it is the smallest deployable increment, fully testable locally with kind (no cloud, no spend), and it produces the exact artifact every later piece builds on — the operator templates this chart, EKS runs this image, Argo CD deploys this chart.
- The new ADR (next number: **0010**) records the ADR-0007 reversal; it should be written during implementation, before the chart's networked Deployment lands, so the decision precedes the code that embodies it.
- Build order within the piece (for the implementation plan): (2a) canonical `deploy/docker/Dockerfile` + GHCR publish (GoReleaser) + PR build job + ADR-0010; then (2b) the `deploy/helm/concord/` chart + `helm lint`/`kubeconform`/`helm-unittest` + `make chart-test` kind e2e.
- The chart's `values.yaml` surface is deliberately the knob set the `TeamCoordinator` CRD will expose per team, so the operator piece becomes "translate a CR into these values," not a redesign.
- Helm 4 and kind are the local toolchain; kubeconform validates against upstream K8s schemas. Nothing here requires a cloud account.
