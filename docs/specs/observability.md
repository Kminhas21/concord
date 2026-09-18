# SPEC: concord observability (piece #1 of the platform flagship)

**Status:** ready-for-agent. First piece of the "local → platform" flagship; ships in solo/local mode, no cloud or Kubernetes required. Terms are defined in `CONTEXT.md`; respects ADR-0001 (two independent layers), ADR-0002 (no reaper/TTL on correctness), ADR-0007 (daemon shape).

## Problem Statement

Today concord is a silent black box. The daemon logs only its lifecycle (`listening…`, `shutting down`); the RPC handlers log nothing, expose no metrics, and emit no events. A developer running concord locally has no way to *see* coordination happening — the only signals are a hook's exit code (2 = blocked) and watching Dragonfly with `redis-cli monitor`. There is no way to answer "how often are edits being blocked?", "which actors overlap?", or "is divergence rising?", and no way to watch events unfold in real time. This makes concord hard to trust, hard to debug, and impossible to demo.

## Solution

Give concord two complementary observability planes, both runnable locally with one `docker compose up`, built on OpenTelemetry:

1. **A metrics plane → Grafana.** The daemon is instrumented with OpenTelemetry and exposes a Prometheus `/metrics` endpoint. Prometheus scrapes it; a provisioned-as-code Grafana dashboard shows aggregate trends: block rate over time, overlaps and divergences per minute, RPC latency (p50/p99), and a live-intents gauge. This is the "how is coordination behaving overall?" view.

2. **An event plane → a live event table.** On every coordination decision the daemon emits a structured **domain event** to NATS JetStream (a durable stream). A small `event-web` service subscribes and streams events to a browser over Server-Sent Events, rendering a real-time, filterable **tabular event feed** — the play-by-play: "Agent A blocked on `login.go`", "Agent B overlap on `src/auth`". This is the "watch it happen right now" view and makes concord genuinely event-driven.

Crucially, observability never degrades coordination: event emission is asynchronous and best-effort, so a slow or absent NATS never slows or blocks a `CheckEdit`.

## User Stories

1. As a developer running concord locally, I want a single `docker compose up` to start concord plus its full observability stack, so that I can see the system working without wiring anything by hand.
2. As a developer, I want a Grafana dashboard of concord's behavior, so that I can see coordination trends at a glance.
3. As a developer, I want the dashboard to show the rate of blocked vs allowed edits over time, so that I can tell how much contention agents are hitting.
4. As a developer, I want the dashboard to show overlaps reported and divergences detected per minute, so that I can gauge how well work is being partitioned.
5. As a developer, I want the dashboard to show RPC latency percentiles (p50/p99) per method, so that I can confirm coordination stays fast.
6. As a developer, I want a live-intents gauge, so that I can see how many actors are actively registered at any moment.
7. As a developer, I want a live event page that fills in as coordination happens, so that I can watch the system react in real time rather than infer from exit codes.
8. As a developer, I want each event row to show timestamp, actor, event type, path(s), and (for blocks) the reason, so that I can read the story of a coordination episode.
9. As a developer, I want blocked edits shown distinctly (e.g. red) and overlaps/divergences color-coded, so that the important events stand out at a glance.
10. As a developer, I want to filter the live event table by actor, event type, or path, so that I can isolate one agent's activity or one file's history.
11. As a developer, I want the event page to show the most recent N events when I open it (not just events from the moment I connected), so that I don't miss what just happened.
12. As a developer, I want events to survive an `event-web` restart, so that the feed is durable rather than purely ephemeral.
13. As a developer, I want a blocked-edit decision to produce an `edit_blocked` event carrying the stale-vs-no-read reason, so that I can distinguish the two block causes.
14. As a developer, I want an allowed edit to produce an `edit_allowed` event, so that the feed shows the full flow, not only failures.
15. As a developer, I want a read to produce a `read_recorded` event, so that I can see when an actor establishes its read-hash.
16. As a developer, I want intent registration and actual-footprint appends to produce events, so that I can watch the intent registry fill in.
17. As a developer, I want an overlap reported by a query to produce an `overlap_reported` event naming both actors and the shared paths, so that I can see dedup signals as they occur.
18. As a developer, I want divergence between predicted and actual footprint to produce a `divergence_detected` event listing the divergent paths, so that I can watch coupling the partition missed.
19. As a developer, I want a reconciliation (a writer's own out-of-band change) to produce a `reconciled` event, so that I can see the shell-sweep working.
20. As a developer, I want an intent record expiring to produce an `intent_expired` event, so that I can see the registry self-cleaning.
21. As an operator, I want observability to be strictly best-effort, so that a down or slow telemetry backend can never slow or block a coordination decision.
22. As an operator, I want the daemon to keep coordinating correctly even if NATS is unreachable, so that telemetry is never a correctness dependency.
23. As an operator, I want metrics exposed on a standard Prometheus `/metrics` endpoint, so that any Prometheus-compatible scraper can consume them.
24. As an operator, I want the Grafana dashboards and Prometheus/NATS configuration provisioned from files in the repo, so that the whole stack is reproducible and version-controlled.
25. As a hiring reviewer, I want the observability to use current, in-demand tooling (OpenTelemetry, Prometheus, Grafana, NATS JetStream, SSE), so that the project demonstrates modern platform practice.
26. As a contributor, I want the event and metric emission tested at a clean seam, so that I can change internals without breaking the observability contract.
27. As a developer, I want each event to carry the decision latency where relevant, so that I can correlate the live feed with the latency graphs.

## Implementation Decisions

**Instrumentation standard.** OpenTelemetry is the single instrumentation foundation in the daemon. Metrics and domain events are emitted from the coordination service's RPC handlers, at the point each decision is made. Only the daemon is instrumented in this piece; end-to-end tracing (which would also instrument the `concord-hook` client) is deferred (see Out of Scope).

**Observability must never degrade coordination (load-bearing).** Emitting a domain event is asynchronous and non-blocking: the handler hands the event to a buffered channel and returns; a background publisher drains the channel to NATS JetStream. If the channel is full or NATS is unavailable, events are dropped and a drop counter is incremented — the handler never waits on, and never fails because of, telemetry. This preserves the version check's latency and correctness guarantees (ADR-0002, ADR-0007).

**New module — an `Emitter` seam.** The coordination service gains a dependency on a narrow `Emitter` interface that accepts domain events. Production wiring supplies a NATS-JetStream-backed emitter; tests supply a fake. This is the single new seam; the metrics increments are made through the OTel meter alongside the emit call. (Prototype shape of a domain event, encoding the decision:)

```json
{
  "ts": "2026-09-18T12:34:56.789Z",
  "type": "edit_blocked",
  "actor_id": "agent-7",
  "paths": ["src/auth/login.go"],
  "reason": "stale",            // stale | no-read | "" (n/a)
  "latency_ms": 3.1,
  "detail": {}                    // event-type-specific extras (e.g. matched_actor for overlaps)
}
```

**Event types** (one per coordination decision): `read_recorded`, `edit_allowed`, `edit_blocked`, `intent_registered`, `actual_appended`, `overlap_reported`, `divergence_detected`, `reconciled`, `intent_expired`.

**Metrics** (OTel → Prometheus exporter on `/metrics`):

- `concord_checkedit_total{decision}` — counter; decision ∈ allowed|blocked_stale|blocked_no_read|allowed_new_file.
- `concord_rpc_duration_seconds{method}` — histogram of handler latency.
- `concord_overlaps_total`, `concord_divergences_total`, `concord_intent_expired_total` — counters.
- `concord_events_dropped_total` — counter (telemetry backpressure/NATS-down visibility).
- `concord_live_intents` — gauge (active intent records).

**Event transport — NATS JetStream.** Events are published to a durable JetStream stream (subject namespaced, e.g. `concord.events`). Durability gives replay: a freshly opened event page can request the last N events, and events survive an `event-web` restart. The daemon is a producer only.

**New service — `event-web`.** A separate small Go service (not part of the daemon) that: subscribes to the JetStream stream, keeps a bounded in-memory ring buffer of the most recent N events for instant page load, serves a single static HTML/JS page, and streams events to the browser over **Server-Sent Events**. The SSE contract: a client connects to an events endpoint; on connect it receives the buffered recent events, then a continuous stream of new events as JSON, one per SSE message. Filtering (by actor/type/path) is applied client-side in the page.

**Web UI.** A single static page (vanilla JS + SSE, no build toolchain) rendering a live, append-on-top, color-coded, filterable table. It is deliberately a backend/platform artifact, not a front-end project.

**Local topology — one compose file.** `docker compose up` starts: concord (daemon), Dragonfly, NATS (JetStream enabled), Prometheus, Grafana, and `event-web`. Grafana dashboards, the Grafana datasource, and the Prometheus scrape config are provisioned from files committed in the repo (dashboards-as-code). No manual clicking to reach a working dashboard.

**Configuration.** New settings follow the existing `CONCORD_*` env-var convention (e.g. a NATS URL, a metrics listen address, an events-enabled toggle). Absent/empty telemetry config degrades to "coordination only" with emission disabled.

**Layer separation preserved (ADR-0001).** Observability is a cross-cutting concern layered onto both the version check and the intent registry; it never lets one layer block or deny on behalf of the other. It only observes.

## Testing Decisions

**What makes a good test here.** Tests assert *observable behavior* — the events produced and the metrics exposed — never internal wiring. The existing RPC surface remains the driver: a test drives a real `CheckEdit`/`QueryIntent`/etc. through the Connect client (as today) and asserts what came out.

**The one new seam: the `Emitter`.** A fake `Emitter` injected into the service captures emitted events. Tests drive an RPC decision and assert the correct event type, actor, paths, and reason were emitted (e.g. a stale edit emits `edit_blocked{reason:"stale"}`; an overlap query emits `overlap_reported` naming both actors). This tests the observability contract at its boundary, not the NATS client.

**Metrics as external behavior.** After driving RPCs, a test scrapes the `/metrics` endpoint and asserts the expected counters/histograms moved (e.g. `concord_checkedit_total{decision="blocked_stale"}` incremented). This asserts the public metrics surface, not internal counters.

**event-web SSE as external behavior.** An integration test connects to the SSE endpoint, publishes an event (through the emitter/JetStream or a seam), and asserts the SSE stream delivers it, and that a late-connecting client receives the buffered recent events.

**Best-effort guarantee under failure.** A test asserts that with the emitter failing or NATS unavailable, `CheckEdit` still returns its correct allow/block verdict and `concord_events_dropped_total` increments — proving telemetry is never a correctness or availability dependency.

**Prior art.** The existing seam tests in the coordination package (drive Connect client → assert response) and the testcontainers harness (`StartDragonfly`) are the model; a NATS testcontainer follows the same pattern for the event-plane integration tests. The stress harness (`-tags stress`) is prior art for the best-effort/failure probes.

## Out of Scope

- **Distributed tracing (Tempo) and log aggregation (Loki),** and instrumenting the `concord-hook` client for end-to-end traces — the deliberate metrics-first cut; these are the immediate follow-up piece.
- **Everything cloud/platform:** Kubernetes, the operator/CRD, multi-tenant team mode, AWS/EKS, Terraform, Argo CD, k6/Toxiproxy. Those are later flagship pieces; this piece is local-mode only.
- **OTel Collector.** Not needed while metrics are scraped directly from the daemon's `/metrics`; it enters when traces/logs are added.
- **Alerting / SLOs** on the metrics (defined later, once the metrics exist and have baselines).
- **Authentication** on the event page or `/metrics` (local-only for now).
- Any change to the coordination guarantees, the version-check logic, or the intent-registry logic. This piece only observes them.

## Further Notes

- This is the natural first flagship piece because it is immediately useful (you can finally *see* concord work), it is pure platform-eng skill (OpenTelemetry, Prometheus, Grafana, NATS JetStream, SSE), it runs entirely in local mode, and it makes every later piece debuggable.
- An ADR (next number: 0009) should be recorded during implementation capturing the observability architecture and, in particular, the "telemetry is strictly best-effort, never on the correctness/latency path" decision — it is a real, surprising trade worth documenting.
- The event model here is designed to carry forward unchanged into team/hosted mode: the same domain events, published to the same kind of stream, are what a future multi-tenant dashboard and Grafana would consume — so this piece is also the foundation of the platform observability story, not a throwaway.
- Build order within the piece (for the implementation plan): (1a) OTel metrics + `/metrics` + Prometheus + Grafana dashboard-as-code + compose; then (1b) the `Emitter` seam + domain events + NATS JetStream + `event-web` + the live SSE table.
