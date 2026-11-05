# Performance

The full results and what they mean. The [README](../README.md#performance) has the
headline figures.

Measured on 2026-11-05 at commit `312c006` with `make load`
([ADR 0020](adr/0020-how-latency-and-the-key-ceiling-are-measured.md),
[test/load](../test/load/README.md)) on one laptop: an Intel Core i5-10210U (4 cores, 8
threads, 1.6 GHz base), 16 GB of RAM, Windows 11 and Docker Desktop 29.2.1 on WSL2 (8
CPUs and 7.7 GB for Docker). k6, RateGuard, Redis and Postgres all ran on it at once.
Treat the figures as the cost of the decision path on modest hardware, not as a server
benchmark, and re-measure on your own.

Every request below was an allowed take that wrote its bucket in Redis (the most
expensive decision), every one was answered 200, and warm-up was not measured.

## Latency: central and sidecar

One instance, 100 API keys, requests arriving at a fixed rate for 60 seconds:

| Topology | Client | p50 at 250/s | p99 at 250/s | p50 at 1,000/s | p99 at 1,000/s |
|---|---|---|---|---|---|
| Central, over the Docker network | k6 | 1.44 ms | 26.4 ms | 0.89 ms | 41.8 ms |
| Sidecar, loopback TCP | k6 | 1.40 ms | 23.9 ms | 1.59 ms | 89.9 ms |
| Sidecar, Unix socket | Go ([socketbench](../test/load/socketbench/main.go)) | 1.08 ms | 15.4 ms | 1.11 ms | 92.3 ms |
| Sidecar, loopback TCP | Go ([socketbench](../test/load/socketbench/main.go)) | 0.86 ms | 9.1 ms | 1.20 ms | 80.1 ms |

RateGuard's own view of the same runs, from `rateguard_decision_duration_seconds`, put
the median at or under 1 ms in every run (the histogram resolves bucket bounds, not
exact values). Its p99 was at most 10 to 25 ms at 250 a second, and at most 25 to
100 ms at 1,000.

What the numbers say, and what they do not:

- **A decision costs about a millisecond at the median,** as the caller sees it, and
  less than that inside RateGuard.
- **On one machine the topologies cannot be told apart.** The difference between
  crossing Docker's bridge and staying on loopback, or between TCP and the Unix socket,
  is smaller than the noise between runs; the socket was not consistently faster than
  loopback TCP here. What a sidecar saves in a real deployment is the network round trip between
  hosts, which a single machine cannot show. Add your own network's round trip to the
  central figures.
- **The tail comes from sharing the CPU, not from the network.** The service's own p99
  is close to the caller's, and it grows with load: the load generator, RateGuard and
  Redis compete for the same 8 logical CPUs, and in a saturation run k6 alone used more
  CPU than RateGuard. On this laptop the overview's single-digit-millisecond budget
  holds at the median, and at p90 in every run but one (k6 on the sidecar at 1,000 a
  second, 15.3 ms); it does not hold at p99.

## Throughput and the per-key ceiling

k6 with 100 virtual users sending back to back for 30 seconds
([ADR 0003](adr/0003-no-hot-key-sharding-in-v1.md)):

| Run | Requests a second | p50 | p99 |
|---|---|---|---|
| One instance, one hot key | 4,921 | 14.5 ms | 93.7 ms |
| One instance, 100 keys | 4,757 | 15.2 ms | 99.0 ms |
| Three instances behind nginx, one hot key | 2,503 | 28.5 ms | 170.0 ms |
| Three instances behind nginx, 100 keys | 2,386 | 30.1 ms | 179.2 ms |
| Redis alone: the bucket script on one key (`redis-benchmark`, 50 connections) | 19,680 | | |
| Redis alone: the bucket script on random keys | 20,740 | | |

- **The per-key ceiling is about 19,700 decisions a second on this machine,** the rate
  at which Redis runs the bucket script on a single key. It is the same as on random
  keys, because one Redis runs every script on one thread: with a single Redis, one
  key's ceiling is Redis's ceiling.
- **End to end, the hot key was never the limit.** One key and 100 keys ran at the same
  rate, about 4,900 decisions a second through one instance, bound by CPU. Three
  instances on the same four cores were slower than one, since nginx and two more
  processes compete for those cores: scaling out needs more machines, not more
  processes on one.
- **Runs vary.** Repeats of the same single-instance run on this laptop ranged from about
  1,750 to 4,900 requests a second, depending on how hot the CPU was. The figures above
  come from one run, with 20 idle seconds before each measurement.
- **Sharding.** A client that needs more than the ceiling needs its bucket split into
  `K` sub-buckets of `capacity / K`, picked at random, as ADR 0003 plans. Because a
  single Redis caps every key together, the sub-buckets only raise the ceiling when they
  live on different Redis nodes. `keyFor` is still the one place to change.
