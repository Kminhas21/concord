# Shared RPC helpers for the kind e2e scripts (chart-test.sh, operator-test.sh).
# Source it after setting RPC_URL (the port-forwarded concord base URL, e.g.
# http://localhost:8080). Assertions drive concord's RPC seam only.

RPC=${RPC_URL:?set RPC_URL before sourcing rpc.sh}/concord.v1.CoordinationService

# call METHOD BODY [TOKEN]: POST a Connect JSON call, with a bearer token if given.
call() {
  local auth=()
  [ -n "${3:-}" ] && auth=(-H "Authorization: Bearer $3")
  curl -s -X POST "$RPC/$1" -H 'Content-Type: application/json' ${auth[@]+"${auth[@]}"} -d "$2"
}
fail() { echo "FAIL"; shift; for l in "$@"; do echo "  $l"; done; exit 1; }

# assert_auth_rejects TOKEN: a missing and a wrong token are both rejected.
# Retries to absorb port-forward + rollout readiness (the obs-smoke pattern).
assert_auth_rejects() {
  local token=$1 none="" wrong="" ok=""
  echo -n "==> auth rejects a missing and a wrong token: "
  for _ in $(seq 1 30); do
    none=$(call RecordRead '{"actorId":"e2e","path":"x.go","hash":"v1"}' || true)
    wrong=$(call RecordRead '{"actorId":"e2e","path":"x.go","hash":"v1"}' "not-$token" || true)
    if echo "$none" | grep -q unauthenticated && echo "$wrong" | grep -q unauthenticated; then
      ok=1
      break
    fi
    sleep 1
  done
  [ -n "$ok" ] || fail x "no-token response: $none" "wrong-token response: $wrong"
  echo "ok"
}

# assert_version_check TOKEN: record a read at v1; a stale edit (v2) must BLOCK
# and a matching edit (v1) must ALLOW.
assert_version_check() {
  local token=$1 stale="" allow="" ok=""
  echo -n "==> drive version check with the good token (stale blocks, matching allows): "
  for _ in $(seq 1 30); do
    if call RecordRead '{"actorId":"e2e","path":"x.go","hash":"v1"}' "$token" | grep -vq unauthenticated; then
      # `|| true` so a port-forward flap between calls retries instead of
      # aborting under `set -e`.
      stale=$(call CheckEdit '{"actorId":"e2e","path":"x.go","currentHash":"v2"}' "$token" || true)
      allow=$(call CheckEdit '{"actorId":"e2e","path":"x.go","currentHash":"v1"}' "$token" || true)
      if echo "$stale" | grep -q 'changed since you last read it' && echo "$allow" | grep -q '"allowed":true'; then
        ok=1
        break
      fi
    fi
    sleep 1
  done
  [ -n "$ok" ] || fail x "stale response: $stale" "allow response: $allow"
  echo "ok"
}
