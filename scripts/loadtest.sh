#!/bin/sh
# Phase 7's measurements (ADR 0020), run against the Docker Compose stack:
#
#   1. central:  k6 in its own container, over the Docker network, at fixed rates
#   2. sidecar:  k6 inside RateGuard's network namespace (loopback TCP), same rates
#   3. socket:   the Go benchmark over the Unix socket and over loopback TCP, same rates
#   4. hot key:  k6 at saturation on one API key, then on 100, against one instance
#   5. the same against three instances behind nginx
#   6. Redis alone: redis-benchmark running the bucket script on one key, then on
#      random keys
#
# Every number depends on the machine; the README publishes one run with the
# machine it ran on. Reports go to stdout and to test/load/results/ (gitignored).
#
# Tunables (environment): RATES ("250 1000"), DURATION (60s), WARMUP (10s),
# CLIENTS (100), VUS (100), THROUGHPUT_DURATION (30s), COOLDOWN (20 seconds of idle
# before each measurement, so a laptop's CPU is not still hot from the last one).
set -eu

cd "$(dirname "$0")/.."

K6_IMAGE=grafana/k6:1.4.2
GO_IMAGE=golang:1.27.3
RATES=${RATES:-250 1000}
DURATION=${DURATION:-60s}
WARMUP=${WARMUP:-10s}
CLIENTS=${CLIENTS:-100}
VUS=${VUS:-100}
THROUGHPUT_DURATION=${THROUGHPUT_DURATION:-30s}
COOLDOWN=${COOLDOWN:-20}
OUT=test/load/results
mkdir -p "$OUT"
# Docker needs a host path it understands: D:/... under Git Bash on Windows.
HOST_PWD="$(pwd -W 2>/dev/null || pwd)"

token="$(sed -n 's/^RG_ADMIN_TOKEN=//p' .env)"
if [ -z "$token" ]; then
	echo "RG_ADMIN_TOKEN is missing from .env" >&2
	exit 1
fi

LOAD="docker compose -f docker-compose.yml -f docker-compose.load.yml"
DEMO="docker compose -f docker-compose.yml -f docker-compose.demo.yml"

# k6 runs one script with the given docker network and environment, and
# saves its report as $OUT/<name>.txt. Arguments after the name are passed to
# docker run.
k6() {
	name=$1
	script=$2
	shift 2
	MSYS_NO_PATHCONV=1 docker run --rm \
		-v "$HOST_PWD/test/load:/scripts:ro" \
		-e ADMIN_TOKEN="$token" \
		"$@" "$K6_IMAGE" run --quiet --no-color "/scripts/$script" | tee "$OUT/$name.txt"
}

port="$(sed -n 's/^RATEGUARD_HOST_PORT=//p' .env)"
METRICS_URL="http://127.0.0.1:${port:-8080}/metrics"

# histogram prints the single instance's check-latency histogram as
# "<le> <cumulative count>" lines.
histogram() {
	curl -fsS "$METRICS_URL" |
		sed -n 's/^rateguard_decision_duration_seconds_bucket{operation="check",le="\([^"]*\)"} \(.*\)$/\1 \2/p'
}

# server_side appends to $OUT/<name>.txt what the service itself measured
# between two histogram snapshots: the bucket bound each percentile falls
# under. It counts every check in between, the warm-up included.
server_side() {
	printf '%s\n' "$2" >"$OUT/.before"
	printf '%s\n' "$3" >"$OUT/.after"
	awk 'NR == FNR { before[$1] = $2; next }
		{ n++; le[n] = $1; cum[n] = $2 - before[$1] }
		END {
			total = cum[n]
			if (total <= 0) { print "server side     no checks recorded"; exit }
			split("0.5 0.9 0.99", qs, " ")
			line = "server side     "
			for (j = 1; j <= 3; j++) {
				for (i = 1; i <= n && cum[i] < qs[j] * total; i++) {}
				bound = (le[i] == "+Inf") ? "+Inf" : sprintf("%g", le[i] * 1000)
				line = line sprintf("p%g <= %s ms  ", qs[j] * 100, bound)
			}
			printf "%s(%d checks; histogram bucket bounds)\n\n", line, total
		}' "$OUT/.before" "$OUT/.after" | tee -a "$OUT/$1.txt"
	rm -f "$OUT/.before" "$OUT/.after"
}

{
	echo "commit  $(git rev-parse --short HEAD)$(git diff --quiet HEAD -- . || echo ' (uncommitted changes)')"
	echo "date    $(date -u +%Y-%m-%dT%H:%M:%SZ)"
	docker info --format 'docker  {{.ServerVersion}}, {{.NCPU}} CPUs, {{.MemTotal}} bytes, {{.OperatingSystem}}, kernel {{.KernelVersion}}'
} | tee "$OUT/environment.txt"

echo "--- one instance, also on a unix socket"
$LOAD up -d --build --wait
rg="$($LOAD ps -q rateguard)"
net="$(docker inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{end}}' "$rg")"

# measure runs one measurement command after the cool-down, then appends what
# the service itself measured. The report's name is the command's first
# argument after the function name: measure k6 <name> ... or
# measure socketbench <name> ...
measure() {
	sleep "$COOLDOWN"
	before="$(histogram)"
	"$@"
	server_side "$2" "$before" "$(histogram)"
}

# socketbench runs the Go benchmark at a rate inside RateGuard's network
# namespace, with the socket's volume mounted.
socketbench() {
	MSYS_NO_PATHCONV=1 docker run --rm --network "container:$rg" \
		-v "$HOST_PWD:/src:ro" -v rateguard-load-socket:/run/rateguard \
		-v rateguard-gomod:/go/pkg/mod -v rateguard-gocache:/root/.cache/go-build \
		-e RG_ADMIN_TOKEN="$token" -e GOFLAGS=-buildvcs=false -w /src "$GO_IMAGE" \
		go run ./test/load/socketbench -rate "$2" -duration "$DURATION" -warmup "$WARMUP" -clients "$CLIENTS" |
		tee "$OUT/$1.txt"
}

for rate in $RATES; do
	measure k6 "central-$rate" latency.js --network "$net" \
		-e BASE_URL=http://rateguard:8080 -e RATE="$rate" -e DURATION="$DURATION" -e WARMUP="$WARMUP" -e CLIENTS="$CLIENTS" \
		-e TITLE="central: k6 over the Docker network, $rate/s"
	measure k6 "sidecar-$rate" latency.js --network "container:$rg" \
		-e BASE_URL=http://127.0.0.1:8080 -e RATE="$rate" -e DURATION="$DURATION" -e WARMUP="$WARMUP" -e CLIENTS="$CLIENTS" \
		-e TITLE="sidecar: k6 on loopback in RateGuard's network namespace, $rate/s"
	measure socketbench "socket-$rate" "$rate"
done

measure k6 hotkey-1 throughput.js --network "$net" \
	-e BASE_URL=http://rateguard:8080 -e CLIENTS=1 -e VUS="$VUS" -e DURATION="$THROUGHPUT_DURATION" \
	-e TITLE="one instance, one hot key, $VUS VUs"
measure k6 spread-1 throughput.js --network "$net" \
	-e BASE_URL=http://rateguard:8080 -e CLIENTS=100 -e VUS="$VUS" -e DURATION="$THROUGHPUT_DURATION" \
	-e TITLE="one instance, 100 keys, $VUS VUs"

echo "--- redis alone: the bucket script, one key and random keys"
sleep "$COOLDOWN"
sha="$($LOAD exec -T redis redis-cli -x SCRIPT LOAD < internal/limiter/scripts/bucket.lua | tr -d '\r')"
{
	echo "== redis-benchmark, EVALSHA of the bucket script, 50 connections, no pipelining"
	$LOAD exec -T redis redis-benchmark -q -n 1000000 -c 50 \
		EVALSHA "$sha" 1 rg:bench:hot 1000000000 1000000000 take | tr '\r' '\n' | grep -v '^$' | tail -1 | sed 's/^/one key:      /'
	$LOAD exec -T redis redis-benchmark -q -n 1000000 -c 50 -r 1000000 \
		EVALSHA "$sha" 1 rg:bench:__rand_int__ 1000000000 1000000000 take | tr '\r' '\n' | grep -v '^$' | tail -1 | sed 's/^/random keys:  /'
} | tee "$OUT/redis.txt"

echo "--- three instances behind nginx"
$LOAD down
$DEMO up -d --build --wait
demo_net="$(docker inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{end}}' "$($DEMO ps -q lb)")"
sleep "$COOLDOWN"
k6 hotkey-3 throughput.js --network "$demo_net" \
	-e BASE_URL=http://lb:80 -e CLIENTS=1 -e VUS="$VUS" -e DURATION="$THROUGHPUT_DURATION" \
	-e TITLE="three instances behind nginx, one hot key, $VUS VUs"
sleep "$COOLDOWN"
k6 spread-3 throughput.js --network "$demo_net" \
	-e BASE_URL=http://lb:80 -e CLIENTS=100 -e VUS="$VUS" -e DURATION="$THROUGHPUT_DURATION" \
	-e TITLE="three instances behind nginx, 100 keys, $VUS VUs"
$DEMO down

echo "--- done; reports are in $OUT/"
