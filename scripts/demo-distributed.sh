#!/bin/sh
# Phase 5's headline demo, against the stack from docker-compose.demo.yml
# (three RateGuard instances behind nginx). Run it with `make demo`.
#
#   1. A client limited to 100 requests an hour sends 300 at once through the
#      load balancer. Exactly 100 are allowed, although every instance answers
#      about a third of them.
#   2. Redis is stopped. Every request is still decided, by each instance's
#      local bucket of 100 / 3, and the degradation shows in /metrics.
#   3. Redis is started again, and the circuit breakers close.
#
# It exits non-zero if any of that does not happen.
set -eu

LB="${DEMO_LB_URL:-http://127.0.0.1:8090}"
TOKEN="${RG_ADMIN_TOKEN:-$(sed -n 's/^RG_ADMIN_TOKEN=//p' .env)}"
COMPOSE="docker compose -f docker-compose.yml -f docker-compose.demo.yml"
COOLDOWN="${RG_REDIS_BREAKER_COOLDOWN_SECONDS:-5}"
OUT="$(mktemp -d)"
trap 'rm -rf "$OUT"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

admin() { # method path [body]
	if [ $# -ge 3 ]; then
		curl -fsS -X "$1" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$3" "$LB$2"
	else
		curl -fsS -X "$1" -H "Authorization: Bearer $TOKEN" "$LB$2"
	fi
}

# check prints "<status> <instance>" for one decision.
check() {
	curl -s -o /dev/null -w '%{http_code} %header{x-upstream}\n' \
		-H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
		-d '{"method":"GET","path":"/api/products"}' "$LB/v1/check"
}

# count prints how many lines of a results file have the given status.
count() { awk -v s="$2" '$1 == s { n++ } END { print n + 0 }' "$1"; }

# metric prints "<instance> <value>" for one series, once per instance. The
# load balancer goes round the instances, so nine scrapes reach all three.
metric() {
	for _ in 1 2 3 4 5 6 7 8 9; do
		# Only the sample line starts with the series; # HELP and # TYPE do not.
		curl -s -D "$OUT/headers" "$LB/metrics" | awk -v s="$1 " 'index($0, s) == 1 { print $NF }' >"$OUT/value"
		printf '%s %s\n' "$(sed -n 's/^[Xx]-[Uu]pstream: *//p' "$OUT/headers" | tr -d '\r')" "$(cat "$OUT/value")"
	done | sort -u
}

curl -fsS --retry 30 --retry-connrefused --retry-all-errors --retry-delay 1 -o /dev/null "$LB/readyz" ||
	fail "the load balancer at $LB is not ready; start the demo stack first"

echo "== Setting up a client limited to 100 requests an hour"
policy="$(admin POST /v1/policies "{\"name\":\"Demo $(date +%s)\",\"capacity\":100,\"window_seconds\":3600}")"
policy_id="$(echo "$policy" | sed -n 's/^{"id":"\([^"]*\)".*/\1/p')"
client="$(admin POST /v1/clients '{"name":"Demo App"}')"
client_id="$(echo "$client" | sed -n 's/^{"client":{"id":"\([^"]*\)".*/\1/p')"
KEY="$(echo "$client" | sed -n 's/.*"key":"\([^"]*\)".*/\1/p')"
[ -n "$policy_id" ] && [ -n "$client_id" ] && [ -n "$KEY" ] || fail "could not create the policy and client"
admin PUT "/v1/clients/$client_id/policy" "{\"policy_id\":\"$policy_id\"}" >/dev/null
# The concurrent burst runs curl from xargs, which needs these exported.
export KEY LB

echo "== 1. 300 concurrent requests through the load balancer"
seq 300 | xargs -P 30 -I{} sh -c 'curl -s -o /dev/null -w "%{http_code} %header{x-upstream}\n" -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" -d "{\"method\":\"GET\",\"path\":\"/api/products\"}" "$LB/v1/check"' >"$OUT/burst"
allowed="$(count "$OUT/burst" 200)"
refused="$(count "$OUT/burst" 429)"
echo "allowed $allowed, refused $refused"
echo "requests per instance:"
awk '{ n[$2]++ } END { for (i in n) printf "  %s %d\n", i, n[i] }' "$OUT/burst" | sort
instances="$(awk '{ print $2 }' "$OUT/burst" | sort -u | wc -l | tr -d ' ')"
[ "$instances" = 3 ] || fail "requests reached $instances instances, want 3"
[ "$allowed" = 100 ] && [ "$refused" = 200 ] || fail "want exactly 100 allowed and 200 refused: one shared limit"

echo "== 2. Stopping Redis, then 150 more requests"
$COMPOSE stop redis >/dev/null 2>&1
: >"$OUT/outage"
for _ in $(seq 150); do check >>"$OUT/outage"; done
allowed="$(count "$OUT/outage" 200)"
refused="$(count "$OUT/outage" 429)"
echo "allowed $allowed, refused $refused (each instance's local bucket holds 100 / 3 = 33)"
[ $((allowed + refused)) = 150 ] || fail "some requests were not decided: $(awk '$1 != 200 && $1 != 429' "$OUT/outage" | head -3)"
[ "$allowed" -ge 1 ] && [ "$allowed" -le 99 ] || fail "want between 1 and 99 allowed: local enforcement, never unlimited"
echo "rateguard_degraded_decisions_total{mode=\"local\"} per instance:"
metric 'rateguard_degraded_decisions_total{mode="local"}' | sed 's/^/  /'
metric rateguard_redis_breaker_state >"$OUT/state"
echo "rateguard_redis_breaker_state per instance (2 = open):"
sed 's/^/  /' "$OUT/state"
[ "$(awk '$2 != 2' "$OUT/state" | wc -l | tr -d ' ')" = 0 ] || fail "every breaker should be open"

echo "== 3. Starting Redis again"
$COMPOSE up -d --wait redis >/dev/null 2>&1
sleep $((COOLDOWN + 1))
: >"$OUT/recovered"
for _ in 1 2 3 4 5 6; do check >>"$OUT/recovered"; done
metric rateguard_redis_breaker_state >"$OUT/state"
echo "rateguard_redis_breaker_state per instance (0 = closed):"
sed 's/^/  /' "$OUT/state"
[ "$(awk '$2 != 0' "$OUT/state" | wc -l | tr -d ' ')" = 0 ] || fail "every breaker should have closed"
# The stopped Redis kept no data (compose runs it without persistence), so
# the client's bucket is full again.
[ "$(count "$OUT/recovered" 200)" = 6 ] || fail "after recovery want Redis's fresh bucket to allow all 6"

echo "== The demo passed: one shared limit across three instances, local enforcement while Redis was down, and recovery."
