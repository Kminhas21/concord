# SPEC: concord team-mode app changes (piece #3 of the platform flagship)

**Status:** ready-for-agent. Third piece of the "local → platform" flagship. Adds the app-level changes hosted mode needs — per-team bearer-token auth, HTTP health/readiness endpoints, and Dragonfly network isolation in the chart — so a coordination unit can be safely shared by a team. Tracks issue #9. Local-mode-testable (testcontainers + kind); no cloud. Terms are defined in `CONTEXT.md`; respects ADR-0001 (two layers), ADR-0002 (no reaper), ADR-0009 (observability best-effort), ADR-0010 (networked unit, hosted mode only). Records **ADR-0011** (team-token auth).

## Problem Statement

Piece #2 packaged concord as a Helm-deployed **coordination unit**, but the unit is wide open: any client that can reach its Service can read and write a team's coordination state, Dragonfly itself accepts connections from anywhere in the cluster, and Kubernetes can only probe it with a TCP socket — so a pod whose Dragonfly is unreachable still looks Ready and receives traffic it can only answer with errors. Before an operator can stamp out one unit per team (piece #4), a unit has to authenticate its clients, isolate its data store, and report real health.

## Solution

Three small changes around the unchanged coordination service:

1. **Team-token auth.** A Connect interceptor checks `Authorization: Bearer <token>` against the daemon's `CONCORD_TOKEN` with a constant-time compare and rejects mismatches with `CodeUnauthenticated`. Unset `CONCORD_TOKEN` = auth off, so solo/local mode is byte-for-byte unchanged. The hook client sends `CONCORD_TOKEN` when set; on an auth rejection it **fails open loudly** (edit proceeds, distinct stderr warning), consistent with the existing fail-open-when-unreachable stance.
2. **HTTP health/readiness.** `/healthz` (process serving — never touches Dragonfly) and `/readyz` (Dragonfly `PING`, ~1s timeout, 503 on failure) on the RPC listener's mux, exempt from auth. The chart swaps concord's TCP probes for these.
3. **Isolation by deployment, enforced.** Tenant scoping is *one coordination unit per team* (ADR-0010) — no key prefixing, no tenant field in the protocol; the team token identifies the team implicitly. Because Dragonfly has no auth of its own, the chart adds a **NetworkPolicy** admitting Dragonfly ingress only from this release's concord pods.

No coordination logic changes. The version check, intent registry, and their guarantees are identical.

### What team mode does and does not add (honest scope)

The version check compares a holder's read-hash against the **current hash of that holder's own disk**. It therefore stops stale edits across teammates only where agents **share a working tree** (e.g. CI agents on a shared volume). For teammates on **separate clones**, the cross-teammate value is the **intent registry** — which already works across machines, because paths are canonicalized repo-relative. `docs/specs/platform.md` user story 2 is corrected to say so.

## User Stories

1. As a team lead, I want my team's coordination unit to require a team token, so that only our agents can read or write our coordination state.
2. As a CI agent, I want to send the team token on every RPC via `CONCORD_TOKEN`, so that I authenticate without any per-call configuration.
3. As a teammate, I want the hook client to read `CONCORD_TOKEN` from my environment, so that pointing my laptop at the team unit is one env var.
4. As a solo developer, I want concord with no `CONCORD_TOKEN` set to behave exactly as today, so that team mode adds nothing to my local setup.
5. As a teammate with a wrong or missing token, I want a loud, distinct warning that my edits are UNPROTECTED, so that I notice the misconfiguration instead of silently losing coordination.
6. As a teammate with a wrong token, I want my edits to still proceed, so that a misconfigured credential never blocks a whole team's work (fail open — the guarantee is already conditional on reachability).
7. As an operator, I want a `concord_auth_rejected` counter, so that I can see on the dashboard when clients are presenting bad tokens.
8. As an operator, I want rejected requests logged at a bounded rate, so that a misconfigured fleet can't flood the logs.
9. As a security-minded lead, I want the token compared in constant time, so that the check doesn't leak the token through timing.
10. As a cluster operator, I want `/healthz` to report the process is serving without touching Dragonfly, so that a Dragonfly blip never triggers a concord restart storm.
11. As a cluster operator, I want `/readyz` to fail when Dragonfly is unreachable, so that Kubernetes stops routing to a replica that can only return errors.
12. As a cluster operator, I want the probe endpoints exempt from auth, so that kubelet can probe without a token.
13. As a cluster operator, I want the probes on the always-on RPC port, so that they work even when metrics are disabled.
14. As a cluster operator, I want the chart to use HTTP liveness/readiness probes on `/healthz`/`/readyz`, so that pod Ready means "can actually coordinate".
15. As a team lead, I want the chart to read the team token from an existing Kubernetes Secret, so that the credential never lives in values files or git.
16. As a developer, I want auth off by default in the chart with a NOTES warning, so that a dev/kind install still works but nobody mistakes it for a protected unit.
17. As a platform engineer, I want a NetworkPolicy admitting Dragonfly ingress only from this unit's concord pods, so that another namespace can't bypass the token by talking to Dragonfly directly.
18. As a platform engineer, I want the NetworkPolicy behind a `networkPolicy.enabled` toggle (default on), so that it can be disabled where a CNI or setup conflicts.
19. As a teammate on a laptop, I want `CONCORD_ADDR` to accept a full URL (`https://…`), so that I can reach a TLS-terminated external endpoint when piece #5 adds one.
20. As a platform engineer, I want auth, health, and isolation tested through the RPC seam against a real Dragonfly, so that the tests prove behavior, not internals.
21. As a platform engineer, I want `make chart-test` to prove in a real cluster that a bad token is rejected, a good token's stale edit is blocked, and readiness tracks Dragonfly, so that the unit's hardening is grounded end to end.
22. As a maintainer, I want the auth design recorded as ADR-0011 (fail-open on rejection, probes exempt, mTLS/OIDC as upgrades), so that the trade-offs are durable.
23. As a maintainer, I want *coordination unit*, *hosted mode*, and *team token* in `CONTEXT.md`, so that the vocabulary stays exact.
24. As the future `TeamCoordinator` operator (piece #4), I want the auth Secret reference and NetworkPolicy to be chart values, so that provisioning a team is "set these values", not new templates.

## Implementation Decisions

**Auth interceptor.** A new Connect unary interceptor (in a small internal package, e.g. `internal/auth`) constructed only when `CONCORD_TOKEN` is non-empty. It reads `Authorization`, requires the `Bearer ` prefix, compares with `crypto/subtle.ConstantTimeCompare`, and returns `connect.NewError(connect.CodeUnauthenticated, …)` on mismatch. It is installed **outermost**, so rejected calls never reach coordination or the RPC-duration histogram; rejections increment `concord_auth_rejected` (when metrics are on) and emit a rate-limited log line. No domain event — events record coordination decisions, and an unauthenticated request's actor is untrusted. Single token; zero-gap rotation (multiple accepted tokens or a hot-reloaded file) is deferred to the operator piece, which owns credential lifecycle. Rotation today = update the Secret + `rollout restart`.

**Daemon config.** `CONCORD_TOKEN` added to `configFromEnv`. Empty = no interceptor.

**Health endpoints.** Registered on the RPC `http.ServeMux` alongside the Connect handler, outside the interceptor chain (plain `http.Handler`s, so auth never applies). `/healthz` → `200 ok`. `/readyz` → the store's `Ping` (new `RedisStore.Ping(ctx)`, a Redis `PING`) under a ~1s timeout → `200 ok` or `503` with the error text. No caching; probe cadence makes a PING per probe trivial.

**Hook client.** `newClient()` adds a request header `Authorization: Bearer $CONCORD_TOKEN` when set (a Connect client interceptor or a wrapped `http.Client` transport — whichever is smaller). `daemonURL()` uses `CONCORD_ADDR` as-is when it contains `://`, else prefixes `http://`. In `preToolUse`, an error whose `connect.CodeOf(err) == connect.CodeUnauthenticated` prints `concord: auth rejected — edits UNPROTECTED, check CONCORD_TOKEN` and exits 0; all other paths unchanged. Advisory `postToolUse` calls stay silent-fail as today.

**No TLS in the daemon.** In-cluster traffic is plain HTTP behind cluster networking; external TLS terminates at the ALB (piece #5).

**Chart.**
- `values.yaml` gains `concord.auth.existingSecret: ""` and `concord.auth.secretKey: token`. When `existingSecret` is set, the Deployment sets `CONCORD_TOKEN` via `secretKeyRef`; when empty, no env and `NOTES.txt` warns auth is OFF. The chart never generates a token.
- concord liveness → `httpGet /healthz`, readiness → `httpGet /readyz`, both on the rpc port. Dragonfly's TCP probes unchanged.
- `networkPolicy.enabled: true` renders a NetworkPolicy selecting the Dragonfly pods (datastore component label) with ingress allowed only from pods carrying this release's concord (coordination component) labels on the redis port. Harmless where the CNI doesn't enforce it.

**ADR-0011 — team-token auth.** Records: static per-unit bearer token; unset = off; fail open (loudly) on rejection and why; probes exempt; metric not event; single token with rotation deferred to the operator; mTLS and OIDC-at-the-ALB as documented upgrades.

**Docs.** `CONTEXT.md` gains *coordination unit*, *hosted mode* (vs *solo/local mode*), *team token* (avoid: lease token, API key). `platform.md` user story 2 is corrected (see "honest scope"). README's Kubernetes section documents `concord.auth.existingSecret` and `CONCORD_TOKEN`. `DECISIONS.md` gets dated entries per ticket. Roadmap: #2 → done, #3 → in progress.

## Testing Decisions

**What makes a good test here.** Same rule as the rest of concord: drive external behavior at the RPC seam (plus, for the probes, the daemon's HTTP surface) against a real Dragonfly; never assert on key layout or interceptor internals.

**Go seam tests (testcontainers Dragonfly, via `run()` like the existing `cmd/concord` tests):**
- token configured: no header → `Unauthenticated`; wrong token → `Unauthenticated`; right token → the normal stale-blocks / matching-allows verdicts.
- no token configured: calls with and without a header behave exactly as today.
- `/healthz` and `/readyz` return 200 with no auth header even when a token is configured.
- `/readyz` → 503 after Dragonfly is stopped; `/healthz` stays 200.
- auth rejections increment `concord_auth_rejected` on `/metrics`.

**Hook-client tests (`cmd/concord-hook`):** sends the bearer header when `CONCORD_TOKEN` is set and none when unset; an `Unauthenticated` response on `PreToolUse` exits 0 with the UNPROTECTED warning; `CONCORD_ADDR` with a scheme is used verbatim.

**Chart tests:** helm-unittest — auth Secret set ⇒ `CONCORD_TOKEN` `secretKeyRef` rendered, unset ⇒ absent; probes are `httpGet` `/healthz`/`/readyz`; NetworkPolicy rendered when enabled (selector + from-labels + port) and absent when disabled. kubeconform covers the NetworkPolicy schema.

**kind e2e (`make chart-test`, extended):** create a token Secret, install with `concord.auth.existingSecret`; a call with no/bad token is rejected (`unauthenticated`); with the good token, stale `CheckEdit` blocks and matching allows; scale the Dragonfly StatefulSet to 0 → the concord pod goes NotReady; scale back → Ready again. NetworkPolicy enforcement is **not** gated (depends on the CNI).

**Prior art.** `cmd/concord/main_test.go` (`run()` against a listener, metrics endpoint tests), the testcontainers Dragonfly harness, `scripts/chart-test.sh`, and the ticket-04 helm-unittest suites.

## Out of Scope

- **Key-prefix / shared-store multi-tenancy** — tenancy is one unit per team.
- **TLS in the daemon, mTLS, OIDC** — ALB TLS is piece #5; mTLS/OIDC are documented upgrades in ADR-0011.
- **Token issuance and zero-gap rotation** — the operator (piece #4).
- **Ingress/ALB, ServiceMonitor, the `TeamCoordinator` CRD** — pieces #4/#5.
- **Any change to the version-check or intent-registry logic, or to what the guarantee covers** — this piece hardens the unit; it does not widen the guarantee.

## Further Notes

- Build order: (a) daemon auth + health + ADR-0011 + glossary; (b) hook-client token/URL/fail-open; (c) chart auth + HTTP probes + NetworkPolicy + unittests; (d) kind e2e extension. (a)–(b) are Go-only and CI-gated; (c)–(d) are the chart plane.
- The interceptor-outermost ordering means `concord_rpc_duration` counts only authenticated calls; `concord_auth_rejected` counts the rest.
