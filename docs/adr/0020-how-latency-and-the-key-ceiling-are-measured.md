# ADR 0020: How latency and the per-key ceiling are measured

- **Status:** Accepted
- **Date:** 2026-11-05

## Context

ADR 0004 (overview § 8.4) promises measured p50 and p99 for the central and sidecar
topologies, and ADR 0003 (§ 8.3) a single-hot-key run that finds the per-key
throughput ceiling. The locked stack names k6 for load testing. k6 cannot dial a Unix
socket, though, and the sidecar topology's point is the socket (ADR 0017).

## Options

For the sidecar:

1. **k6 on loopback only:** k6 shares RateGuard's network namespace and calls
   `127.0.0.1`. This is co-located, with no network hop, but it is TCP, not the
   socket.
2. **k6 on loopback, plus a small Go benchmark** that drives the Unix socket and
   loopback TCP with the same client, so the socket's own figure is measured too.
3. **A proxy from TCP to the socket:** this adds a hop and measures the proxy.

## Decision

Option 2, chosen by the project owner on 2026-11-05. `scripts/loadtest.sh`
(`make load`) runs everything against the Compose stack:

- **Load model.** Latency runs use an open model: requests arrive at a fixed rate
  whatever the latency, by default 250 a second (light load) and then 1,000 (moderate
  load on the reference laptop), spread over 100 API keys on a policy that
  never refuses. Every request is therefore an allowed take that writes its bucket in
  Redis, the most expensive decision. The first 10 seconds warm the caches and
  connection pools and are not measured. The next 60 seconds are. Every measurement
  starts after 20 idle seconds, because a laptop CPU that is still hot from the last
  run throttles the next one. Even so, the same run repeated can differ by tens of
  percent on a laptop.
- **Central.** k6 runs in its own container and calls RateGuard over the Docker network.
- **Sidecar.** k6 runs in RateGuard's network namespace and calls `127.0.0.1`.
- **Socket.** `test/load/socketbench`, using only the standard library, drives the Unix
  socket and loopback TCP at the same rate, with the same keep-alive client. The
  measured time is split into rounds that alternate which target goes first. Its
  percentiles are nearest-rank; k6 interpolates. Over tens of thousands of samples the
  difference is far below the noise.
- **Per-key ceiling.** k6 runs 100 virtual users back to back, with no pause, for 30
  seconds, first on one API key and then on 100, against one instance and against
  three behind nginx. `redis-benchmark` then runs the bucket script itself in Redis on
  one key and on random keys, which isolates Redis from the HTTP path.
- **Latency is measured by the caller**, from the request's start to the end of its
  body, so it includes the HTTP stack on both sides. The service's own view is the
  `rateguard_decision_duration_seconds` histogram (ADR 0019). The script reports, for
  each single-instance run, the bucket each of its percentiles falls under.

## Consequences

- Every component, k6 included, runs on one machine. "Central" here crosses a virtual
  bridge, not a physical network, so a real deployment adds its own network round
  trip. The load generator and the service also compete for the same CPUs: k6 alone
  used more of them than RateGuard in a saturation run, so throughput figures are a
  floor for the service, not a ceiling. The README publishes the machine with the
  numbers, and they are not general benchmarks.
- With a single Redis, every key's script runs on the same thread, so one hot key and
  many keys have the same Redis ceiling. A per-key ceiling separate from Redis's whole
  capacity only appears once keys are spread across several Redis nodes, which is what
  sharding (ADR 0003) would be for.
- The figures must be measured again when the hot path, the Lua script or the
  deployment changes. `make load` reproduces them.
