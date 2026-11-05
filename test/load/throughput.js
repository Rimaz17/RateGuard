// Throughput at saturation: VUS virtual users send decision requests back
// to back, with no pause, for DURATION after an unmeasured WARMUP.
//
// With CLIENTS=1 every request draws from one bucket, one Redis key: the
// single-hot-key run that finds the per-key ceiling (ADR 0003). Running it
// again with many clients shows whether that key, or something else, is
// the limit.
//
//   BASE_URL     RateGuard, or a load balancer in front of several
//   ADMIN_TOKEN  RG_ADMIN_TOKEN, to create the policy and clients
//   CLIENTS      API keys to spread requests over (default 1)
//   VUS          concurrent virtual users (default 100)
//   DURATION     measured time (default 30s)
//   WARMUP       unmeasured time before it (default 5s)
//   TITLE        heading for the report

import http from 'k6/http';
import { check } from 'k6';
import { env, seconds, setUpClients, checkParams, checkBody, report, measuredThresholds } from './lib.js';

const vus = Number(__ENV.VUS || 100);
const duration = __ENV.DURATION || '30s';
const warmup = __ENV.WARMUP || '5s';

export const options = {
  scenarios: {
    warmup: { executor: 'constant-vus', vus: vus, duration: warmup },
    measure: { executor: 'constant-vus', vus: vus, duration: duration, startTime: warmup },
  },
  thresholds: measuredThresholds,
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  setupTimeout: '120s',
};

export function setup() {
  // 10^9 tokens a second: the bucket never runs dry, so every request is
  // an allowed take that writes the key.
  return { keys: setUpClients(env('BASE_URL'), env('ADMIN_TOKEN'), Number(__ENV.CLIENTS || 1), 1000000000, 1) };
}

export default function (data) {
  const key = data.keys[Math.floor(Math.random() * data.keys.length)];
  const res = http.post(`${__ENV.BASE_URL}/v1/check`, checkBody, checkParams(key));
  check(res, { 'status is 200': (r) => r.status === 200 });
}

export function handleSummary(data) {
  return report(__ENV.TITLE || `throughput with ${vus} VUs`, data, seconds(duration));
}
