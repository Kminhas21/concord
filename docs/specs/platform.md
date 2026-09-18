# SPEC: concord team/hosted platform (architecture)

**Status:** ready-for-agent (architecture spec — decomposes into per-piece specs). The high-level mental model and diagram live in `docs/specs/platform-architecture-overview.md`; this is the build-from architecture spec. Terms are defined in `CONTEXT.md`. Respects ADR-0001 (two layers), ADR-0002 (no reaper), ADR-0006 (no embeddings); **deliberately reverses ADR-0007** ("one daemon per machine, never networked") for team mode — a new ADR records that.

## Problem Statement

concord solves clobbering *within* one developer's session — its subagents don't overwrite each other. But real teams have many agents on the *same repo* at once: teammate A's Claude Code, teammate B's, and automated CI agents all editing the same codebase. concord's guarantee stops at the session boundary, so cross-session and cross-teammate clobbering is unprotected. There is also no way to run concord *for a team*: it's a single loopback daemon per machine with no multi-tenancy, no shared deployment, no operational surface. A team that wants "no stale edits across everyone touching our repo" has nothing to run.

## Solution

Ship concord in **two modes from one codebase**, without sacrificing the local product:

- **Solo/local mode (unchanged).** A developer runs the single daemon locally (`docker run`) and coordinates their own session's subagents. Fast, simple, offline. This stays first-class.
- **Team/hosted mode (new).** The *same coordination guarantee* scales from "agents in my session" to "every agent touching my team's repo." It is a **multi-tenant Kubernetes platform**: an operator provisions an isolated concord + data stack per team; clients are both in-cluster CI agents and teammates' laptops; it is deployed to AWS EKS via Terraform, delivered by Argo CD, observed with OpenTelemetry/Prometheus/Grafana + a live NATS-JetStream event feed, and load/fault-tested with k6 + Toxiproxy.

Team mode scales the *blast radius* of the existing guarantee; it does not change the version-check or intent-registry logic.

## User Stories

**Tenants & coordination at team scale**
1. As a team lead, I want to provision a coordination instance for my team's repo declaratively, so that all agents on that repo are coordinated without my running anything locally.
2. As a teammate, I want my Claude Code edits coordinated against my colleagues' agents on the same repo, so that we don't silently clobber each other's work.
3. As a CI pipeline, I want my automated coding agents (running as Kubernetes Jobs) coordinated against each other and against humans, so that a nightly migration fleet doesn't corrupt in-flight human work.
4. As a team, I want each team's coordination state fully isolated from other teams, so that one team's activity or failure cannot affect another.
5. As a teammate on a laptop, I want to reach my team's concord securely over the internet, so that I get team-scale protection without being in the cluster.
6. As a CI agent in-cluster, I want to reach concord over a fast internal path, so that coordination stays low-latency on the hot path.

**The operator (control plane)**
7. As a platform engineer, I want a `TeamCoordinator` custom resource that describes a team's coordination stack, so that teams are declarative Kubernetes objects.
8. As a platform engineer, I want an operator that reconciles each `TeamCoordinator` into a running concord + data stack, so that I never hand-assemble per-team deployments.
9. As a platform engineer, I want the operator to self-heal drift (someone edits a managed Deployment; it's corrected), so that the declared state is the real state.
10. As a platform engineer, I want the operator to report status/conditions (Ready, Provisioning, error) on each `TeamCoordinator`, so that I can see tenant health with `kubectl get`.
11. As a platform engineer, I want deleting a `TeamCoordinator` to clean up its whole stack (finalizer), so that de-provisioning a team leaves nothing behind.
12. As a platform engineer, I want the operator to provision a `ServiceMonitor` per team, so that Prometheus scrapes each tenant automatically (ties into observability).

**Networking & access**
13. As a platform engineer, I want in-cluster clients to reach a team's concord via an internal Service, so that no tenant is needlessly exposed.
14. As a platform engineer, I want external laptop access to be opt-in per team via a shared ALB with host-based routing, so that I don't run a load balancer per tenant.
15. As a security-minded lead, I want each team's concord to require a per-team credential, so that only authorized clients coordinate against it.
16. As a platform engineer, I want the operator to issue and rotate a per-team token as a Kubernetes Secret, so that credential lifecycle is automated.

**Delivery, infra, and operations**
17. As a platform engineer, I want the whole platform (operator, CRDs, ingress controller, observability) deployed by Argo CD from git, so that the cluster is a pure function of git.
18. As a team lead, I want to provision my team by opening a PR that adds a `TeamCoordinator` manifest, so that teams-as-code is reviewable and auditable.
19. As a platform engineer, I want the AWS cluster and its dependencies provisioned by Terraform, so that the environment is reproducible and destroyable.
20. As a cost-conscious owner, I want the whole AWS footprint `terraform destroy`-able, so that I can spin it up for a demo and tear it down.
21. As a platform engineer, I want pods to assume scoped IAM roles via IRSA, so that no static AWS credentials live in the cluster.
22. As a platform engineer, I want images built and pushed to a registry by CI, so that deploys pull immutable, versioned images.

**Observability & resilience (integration points)**
23. As an operator, I want each team's concord metrics and events flowing into the platform observability stack, so that I can watch coordination across all tenants.
24. As a platform engineer, I want to load-test a team's concord and see p99 under load, so that I can size it.
25. As a reliability engineer, I want to inject latency/faults on the concord↔data link and confirm coordination degrades gracefully (never blocks/among), so that I can prove the best-effort telemetry and correctness-first design under failure.

**Preserving the core**
26. As a solo developer, I want local mode unchanged and offline-capable, so that the platform work never taxes the simple local product.
27. As a maintainer, I want team mode to reuse the exact version-check and intent-registry logic, so that there is one coordination implementation, not two.

## Implementation Decisions

**Dual-mode from one codebase.** The coordination service (version check + intent registry) is unchanged in logic. Team mode adds three things around it: tenant scoping, authentication, and Kubernetes-native health/readiness — none of which touch the coordination decision path.

**`TeamCoordinator` custom resource (the operator centerpiece).** One CR per team/repo. Its `spec` carries the repo identifier, concord replica count, embedded data-store config, intent TTL, resource requests/limits, an `ingress` block (host + external-enabled), and an `auth` block (mode + secret reference). Its `status` carries conditions (`Ready`, `Provisioning`), the resolved endpoint, replica readiness, and `observedGeneration`. (Prototype of the resource shape, encoding the decision — not a working manifest:)

```yaml
kind: TeamCoordinator
spec:
  repo: "github.com/acme/monorepo"
  replicas: 2
  intentTTL: 600s
  dataStore: { engine: dragonfly, storage: 1Gi }
  ingress:  { external: true, host: acme.concord.example.com }
  auth:     { mode: token }           # token | mtls | oidc(future)
status:
  conditions: [{ type: Ready, status: "True" }]
  endpoint: "concord.team-acme.svc.cluster.local:8973"
```

**Reconcile behavior.** For each `TeamCoordinator`, the operator creates/updates, in that team's namespace: a concord **Deployment** (N replicas), a **Dragonfly StatefulSet** (self-hosted per team), a **Service** (internal), an optional **Ingress** (when `ingress.external`), a **NetworkPolicy** (namespace isolation), a **ServiceMonitor** (Prometheus), and a **Secret** (the per-team token). It corrects drift, sets status conditions, and uses a **finalizer** so deletion tears the stack down.

**Data layer.** Self-hosted Dragonfly StatefulSet per team, provisioned by the operator — uniform locally (minikube) and on EKS. Because concord uses only basic Redis operations (SET/GET/SADD/SMEMBERS/EXPIRE/pipeline — no Dragonfly-specific features), **AWS ElastiCache (Valkey/Redis) is a documented drop-in managed alternative**, not the default (managing per-team ElastiCache from the operator would require ACK/Crossplane and break local↔cloud parity).

**Networking.** In-cluster clients use an internal ClusterIP Service. External laptop access is opt-in per team via the **AWS Load Balancer Controller → a single shared ALB** with **host-based routing** per team, **ACM** TLS, **Route53** DNS. One shared L7 load balancer, not one per tenant.

**Authentication.** Baseline is a **per-team bearer token** the operator generates and stores as a Kubernetes Secret; CI agents mount it, laptop clients send it as a header. mTLS and OIDC-at-the-ALB are documented hardening paths, not day-1.

**AWS primitive map.** EKS (managed control plane); **EC2 managed node group** (workers); VPC (public/private subnets, NAT); AWS Load Balancer Controller → ALB (external) + internal Service (in-cluster); **IRSA** (pods → scoped IAM roles); **ECR** (images); Route53 + ACM (DNS/TLS). ElastiCache is the optional managed data swap.

**Delivery (GitOps).** Argo CD, **app-of-apps**: it deploys the platform components (operator, CRDs, ingress controller, observability stack) and the **`TeamCoordinator` CRs themselves live in git** — provisioning a team is a reviewed PR ("teams-as-code").

**Infrastructure as code.** Terraform provisions EKS + VPC + node group + IAM/IRSA + ECR + Route53/ACM and bootstraps Argo CD; the whole footprint is `terraform destroy`-able.

**Resilience & performance.** k6 load-tests a team's concord (drive `CheckEdit`/`QueryIntent`, measure p99 under concurrency). Toxiproxy sits on the concord↔data link to inject latency/faults, proving graceful degradation (correctness/latency-first, best-effort telemetry) and measuring the cost — reusing the existing stress harness and observability metrics as the measurement surface.

**Decomposition (each is its own `/to-spec` + PR when built).** (1) Observability — *already specced*; (2) Containerize + Helm; (3) Team-mode app changes (tenant scoping, auth, probes); (4) The operator (`TeamCoordinator` + controller-runtime, on minikube); (5) IaC + EKS (Terraform, deploy to AWS); (6) GitOps (Argo CD); (7) Resilience (k6 + Toxiproxy). Dependency-ordered.

## Testing Decisions

**What makes a good test.** Assert observable behavior — the objects the operator produces, the API responses concord returns, the endpoints that answer — never internal wiring.

**The operator — the one new seam: `envtest`.** controller-runtime's `envtest` stands up a real API server + etcd. A reconcile test applies a `TeamCoordinator` CR and asserts the expected objects exist with the correct spec (Deployment replicas, StatefulSet, Service, NetworkPolicy, ServiceMonitor, Secret), that status conditions are set, that drift is corrected on re-reconcile, and that deletion (finalizer) removes the stack. This is the standard, highest seam for operators and keeps tests off internal reconcile helpers.

**Team-mode app changes — the existing RPC seam.** Tenant scoping and auth are tested through the existing Connect-client seam (drive `CheckEdit`/`QueryIntent`; assert a request without a valid team token is rejected, and that two teams' state is isolated). Prior art: the coordination package's existing seam tests + the `StartDragonfly` testcontainers harness.

**Infra — plan/validate, not apply-in-CI.** Terraform is checked with `terraform validate` and a `plan`; a full apply is a manual/gated step (cost). Optionally, `terratest` (Go) exercises a throwaway environment, gated and out of the default CI.

**Resilience — end-to-end.** k6 + Toxiproxy scenarios are integration tests asserting the *properties* (p99 bound under load; correct allow/block verdicts and `events_dropped` increments while the data link is degraded), reusing the observability metrics as assertions.

**GitOps — manifest validity + sync.** Argo CD manifests and the app-of-apps are validated (schema/lint) and, in an integration environment, asserted to sync to Healthy.

## Out of Scope

- **Model serving / AI-inference infrastructure** (KServe, Triton, vLLM, Ray, GPU Operator, DCGM). concord coordinates agents' file edits; it never serves models. That is a *different* project, not a concord bolt-on.
- **Multi-cloud / GCP / Azure.** AWS only for this flagship.
- **Anything beyond the seven decomposed pieces** — service mesh, KEDA autoscaling, Kyverno/OPA policy, Crossplane, Backstage are possible *later* additions, explicitly deferred (each would be its own piece).
- **Changing the coordination guarantees, the version-check logic, or the intent-registry logic.** Team mode scales the guarantee's blast radius; it does not alter the logic (ADR-0001/0002/0006 stand).
- **Solo/local mode changes.** Local mode stays as-is; the platform work must not regress it.
- The **observability internals** — specced separately (`docs/specs/observability.md`); this spec only consumes its metric/event surface.

## Further Notes

- **New ADRs to record during implementation:** (a) reversing ADR-0007 (networked, multi-tenant team mode) with the latency justification (in-cluster co-location; coordination gaps are seconds); (b) the operator/`TeamCoordinator` design; (c) per-team token auth (with mTLS/OIDC as documented upgrades).
- **This is an architecture spec, not a single implementation plan.** Each of the seven pieces gets its own `/to-spec` and PR when built, in the dependency order above. Observability (piece 1) is already specced and should be built first.
- **The interview narrative** (dual-mode; same guarantee at two scales; operator-provisioned per-tenant stacks; GitOps teams-as-code; best-effort observability) and the tension→answer pairs are captured in `docs/specs/platform-architecture-overview.md` — keep the two in sync.
- **Cost control is a first-class requirement:** everything AWS is `terraform destroy`-able and intended for short-lived demo/dev use, not always-on hosting.
