# Load tests

The measurements behind [docs/performance.md](../../docs/performance.md) and the
README's performance section
([ADR 0020](../../docs/adr/0020-how-latency-and-the-key-ceiling-are-measured.md)). Run
all of them against the Compose stack with:

```bash
make load            # or ./scripts/loadtest.sh; about 25 minutes
```

The script brings up the stack with RateGuard also serving on a Unix socket
([docker-compose.load.yml](../../docker-compose.load.yml)), runs every measurement, and
writes each report to `test/load/results/`, which is gitignored. It needs only Docker:
k6, Go and nginx run in containers. It creates a policy and clients through the admin
API on each run, so it leaves rows behind in the local database.

| File | What it measures |
|---|---|
| [latency.js](latency.js) | p50/p99 at a fixed arrival rate, spread over many API keys |
| [throughput.js](throughput.js) | Requests a second at saturation, on one key (the hot-key run) or many |
| [socketbench](socketbench/main.go) | The same fixed-rate latency over the Unix socket and loopback TCP. k6 cannot dial a socket |
| [lib.js](lib.js) | Setup through the admin API, and the report format |

Every policy used never refuses, so each request is an allowed take that writes its
bucket: the most expensive decision. The client-side figures leave out the warm-up.
After each single-instance run the script also reports the service's own view, from
the change in `rateguard_decision_duration_seconds` over the run (warm-up included): the
histogram bucket each percentile falls under.

Knobs, all optional: `RATES` (`250 1000`), `DURATION` (60s), `WARMUP` (10s), `CLIENTS` (100),
`VUS` (100), `THROUGHPUT_DURATION` (30s) and `COOLDOWN` (20 seconds idle before each
measurement). For example:

```bash
RATES="500 2000" DURATION=120s make load
```

To run one script by hand, against a stack that is already up:

```bash
docker run --rm --network rateguard_default -v "$PWD/test/load:/scripts:ro" \
  -e BASE_URL=http://rateguard:8080 -e ADMIN_TOKEN="$(sed -n 's/^RG_ADMIN_TOKEN=//p' .env)" \
  grafana/k6:1.4.2 run --quiet /scripts/latency.js
```

Numbers from one machine say little about another. Publish them with the machine
they came from, as `test/load/results/environment.txt` records it.
