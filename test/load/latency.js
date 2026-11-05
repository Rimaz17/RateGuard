// Decision latency at a fixed request rate, as the caller sees it.
//
// Requests arrive at RATE per second whatever the latency (an open model),
// spread over CLIENTS API keys whose buckets never run dry, so every
// decision is a full refill-and-take in Redis. The first WARMUP fills the
// caches and connection pools and is not measured.
//
//   BASE_URL     RateGuard, e.g. http://rateguard:8080 (central) or
//                http://127.0.0.1:8080 from inside its network namespace
//                (sidecar)
//   ADMIN_TOKEN  RG_ADMIN_TOKEN, to create the policy and clients
//   RATE         requests per second (default 1000)
//   DURATION     measured time (default 60s)
//   WARMUP       unmeasured time before it (default 10s)
//   CLIENTS      API keys to spread requests over (default 100)
//   TITLE        heading for the report

import http from 'k6/http';
import { check } from 'k6';
import { env, seconds, setUpClients, checkParams, checkBody, report, measuredThresholds } from './lib.js';

const rate = Number(__ENV.RATE || 1000);
const duration = __ENV.DURATION || '60s';
const warmup = __ENV.WARMUP || '10s';

const arrivals = {
  executor: 'constant-arrival-rate',
  rate: rate,
  timeUnit: '1s',
  preAllocatedVUs: 100,
  maxVUs: 1000,
};

export const options = {
  scenarios: {
    warmup: Object.assign({}, arrivals, { duration: warmup }),
    measure: Object.assign({}, arrivals, { duration: duration, startTime: warmup }),
  },
  thresholds: measuredThresholds,
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  setupTimeout: '120s',
};

export function setup() {
  // 10^9 tokens a minute: no request is ever refused, so every one writes.
  return { keys: setUpClients(env('BASE_URL'), env('ADMIN_TOKEN'), Number(__ENV.CLIENTS || 100), 1000000000, 60) };
}

export default function (data) {
  const key = data.keys[Math.floor(Math.random() * data.keys.length)];
  const res = http.post(`${__ENV.BASE_URL}/v1/check`, checkBody, checkParams(key));
  check(res, { 'status is 200': (r) => r.status === 200 });
}

export function handleSummary(data) {
  return report(__ENV.TITLE || `latency at ${rate}/s`, data, seconds(duration));
}
