# Platform flagship — roadmap & playbook

The "local → platform" flagship turns concord from a local coordination daemon
into a multi-tenant Kubernetes platform, built in seven pieces. This doc is the
living map of **what's done, what's next, and the repeatable workflow** to take
each remaining piece from idea to merged code. The architecture itself lives in
[`docs/specs/platform.md`](specs/platform.md) and the one-page mental model in
[`docs/specs/platform-architecture-overview.md`](specs/platform-architecture-overview.md);
this doc is the process + status layer over them.

## The repeatable workflow (per piece)

Each piece is one turn of the same crank. Do them in order; don't skip the gate.

1. **Grill** — `/mattpocock-skills:grilling`. Map the design tree; settle every decision in rounds before writing anything. Facts are the agent's job (sub-agents/tools), decisions are yours.
2. **Spec** — `/to-spec`. Synthesize the grilled decisions into `docs/specs/<piece>.md` (problem, solution, a long user-story list, implementation + testing decisions, out-of-scope, the **test seam**). No interview — it just writes what grilling settled.
3. **Tickets** — `/mattpocock-skills:to-tickets`. Break the spec into tracer-bullet vertical slices with blocking edges, grouped into PR-sized checkpoints. Ticket files land in `.scratch/<piece>/issues/` (gitignored, local).
4. **Implement, ticket by ticket** — `/mattpocock-skills:implement` + `/tdd`. For each ticket: write the failing test/gate first, implement minimally, make the **verification gate** pass, update `DECISIONS.md` (and `CONTEXT.md`/ADRs when the model or a hard decision changes), then **spin a subagent for `/superpowers:requesting-code-review`**, apply verified findings (process them with `/superpowers:receiving-code-review` — verify before implementing, push back when wrong), and **checkpoint** with the user before moving on.
5. **PR per checkpoint** — push, open the PR, let the 4 required CI checks (gate ×2, lint, race) go green, merge, sync main. Then the next piece.

**Grounded testing is the through-line:** run the real control plane cheaply, mock nothing you can boot. Each piece adds a `make` acceptance gate analogous to `make obs-smoke` / `make chart-test`.

## Status

| # | Piece | Status | Spec | Acceptance gate |
|---|-------|--------|------|-----------------|
| 1 | **Observability** — OTel metrics + Grafana; NATS JetStream + event-web live SSE feed | ✅ **done** (PRs #5, #6) | [observability.md](specs/observability.md) | `make obs-smoke` |
| 2 | **Containerize + Helm** — canonical image → GHCR; chart for one concord+data unit | 🔨 **in progress** | [containerize-helm.md](specs/containerize-helm.md) | `make chart-test` (kind) |
| 3 | **Team-mode app changes** — tenant scoping, per-team auth, HTTP health/readiness probes | ⬜ next | _to spec_ | _tbd_ |
| 4 | **The operator** — `TeamCoordinator` CRD + controller-runtime, on minikube/kind | ⬜ | _to spec_ | envtest + kind |
| 5 | **IaC + EKS** — Terraform stands up AWS (EKS/VPC/EC2/IRSA/ECR/Route53/ACM) | ⬜ | _to spec_ | `terraform test` + LocalStack; real EKS on demand |
| 6 | **GitOps** — Argo CD app-of-apps, teams-as-code | ⬜ | _to spec_ | argocd CLI + kind |
| 7 | **Resilience** — k6 load + Toxiproxy fault injection | ⬜ | _to spec_ | k6 thresholds + Grafana |

## Remaining pieces — scope seeds (for the next grilling)

Each is a starting frontier, not a finished design — grill it properly when you get there.

### #3 — Team-mode app changes
The app-level changes hosted mode needs, deferred out of pieces #1/#2:
- **Tenant scoping** — how one concord process (or one unit per team) namespaces coordination state so teams don't collide. Likely per-team unit (a Dragonfly per team, already the chart's shape) vs a key-prefix scheme in a shared store — a real decision to grill.
- **Auth** — operator-issued per-team bearer token (K8s Secret), checked by concord on the RPC. A Connect interceptor is the natural seam.
- **HTTP health/readiness** — a real `/healthz` (process up) + `/readyz` (Dragonfly reachable) on the daemon, replacing the chart's TCP probes (ticket boundary noted in piece #2).
- Grounded gate: extend `make chart-test` (auth rejects a bad token, readiness reflects Dragonfly) on kind.

### #4 — The operator (the controller-runtime centerpiece)
- **`TeamCoordinator` CRD** — `spec: {repo, replicas, intentTTL, dataStore, ingress, auth}`, `status: {conditions, endpoint}` (prototype in `platform.md`).
- **Reconcile** — a CR → the piece-#2 Helm chart's object set (Deployment + Dragonfly StatefulSet + Services + Secret + probes), drift correction, finalizer-driven deletion, status/conditions. The values surface from piece #2 is deliberately the knob set the CR drives — so this is "translate a CR into those values," not a redesign.
- Grounded gate: **envtest** (real kube-apiserver+etcd, no cluster) for the reconcile loop; **kind** for end-to-end (apply a CR → a working unit).

### #5 — IaC + EKS
- **Terraform** provisions EKS + VPC + EC2 node group + IAM/IRSA + ECR + Route53/ACM and bootstraps Argo CD; fully `terraform destroy`-able.
- Grounded gate: `terraform validate` + `terraform test` (`.tftest.hcl`) + `tflint`/`checkov`; **LocalStack** for VPC/IAM/ECR locally; a real short-lived EKS only when validating the whole thing (cost-gated).
- Add the AWS + Terraform MCP servers here (see below).

### #6 — GitOps
- **Argo CD** app-of-apps; `TeamCoordinator` CRs live in git so provisioning a team is a reviewable PR.
- Grounded gate: argocd CLI + kind (the cluster converges to git); Playwright for the Argo UI.

### #7 — Resilience
- **k6** load-tests the team service to measured thresholds; **Toxiproxy** injects latency/faults on the concord↔Dragonfly link to prove graceful degradation (fail-open) and measure its cost, observed on the piece-#1 Grafana dashboard.

## Grounded-testing tools (add just-in-time, per piece)

Already set up: **Go + testcontainers** (real Dragonfly/NATS), **Docker + docker-compose**, **kind + helm + kubeconform + kubectl** (piece #2), **context7 MCP** (up-to-date library docs — controller-runtime, client-go, Terraform provider, Argo CD, NATS), **Playwright MCP** (UIs). Add when the piece needs it, not upfront:

- **#3/#4:** `setup-envtest` (controller-runtime's real apiserver+etcd), `helm-unittest`; optionally a **Kubernetes MCP** to read cluster state structurally.
- **#5:** `terraform` + `tflint`/`checkov`/`trivy`, **LocalStack** (+ its MCP), the **HashiCorp Terraform MCP** (schema/registry grounding) and **AWS MCP servers** (`aws-api`, `aws-documentation`, `eks`, `cost-analysis` — the cost one avoids surprise EKS bills).
- **#6:** `argocd` CLI, `helm/kind-action` for opt-in CI.
- **#7:** `k6`, `toxiproxy`.

MCPs add context cost — add each only when its piece needs it, and prefer the CLIs (envtest, kind, terraform, k6) as the real grounding; `claude mcp add <name>` is the user's to run.

## Where things live

- **Specs:** `docs/specs/<piece>.md` (source of truth per piece).
- **Tickets:** `.scratch/<piece>/issues/NN-*.md` (local, gitignored; TDD slices + verification gates).
- **Decisions:** `DECISIONS.md` (dated, append-only); **ADRs:** `docs/adr/NNNN-*.md` (hard, surprising, real-trade-off decisions — next number after 0010).
- **Glossary:** `CONTEXT.md` (use the exact vocabulary; update it when the domain model changes).
- **Acceptance gates:** `make` targets + `scripts/` (obs-smoke, chart-test, …); CI in `.github/workflows/`.

## Non-negotiables carried through every piece

Solo/local mode stays a single local binary (loopback). The two-layer separation (ADR-0001), content-hash correctness with no reaper (ADR-0002), Dragonfly-as-shape-not-scale (ADR-0004), and strictly-best-effort observability (ADR-0009) are unchanged — the platform scales the *blast radius* of the same guarantee, it never alters the logic. Hosted mode's topology reversal of ADR-0007 is recorded in ADR-0010; any further reversal gets its own ADR.
