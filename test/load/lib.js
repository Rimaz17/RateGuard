// Shared setup for the k6 load tests: a policy and its clients, created
// through the admin API so that every request in the measurement is a real
// decision for a real API key.

import http from 'k6/http';
import { fail } from 'k6';

// env reads a required environment variable.
export function env(name) {
  const v = __ENV[name];
  if (!v) {
    fail(`${name} must be set`);
  }
  return v;
}

// setUpClients creates a policy of the given size and n clients on it, and
// returns their API keys. Names carry a timestamp, so runs never collide.
export function setUpClients(base, adminToken, n, capacity, windowSeconds) {
  const params = {
    headers: { Authorization: `Bearer ${adminToken}`, 'Content-Type': 'application/json' },
    tags: { name: 'setup' },
  };
  const stamp = Date.now();
  const policy = http.post(`${base}/v1/policies`,
    JSON.stringify({ name: `Load ${stamp}`, capacity: capacity, window_seconds: windowSeconds }), params);
  if (policy.status !== 201) {
    fail(`creating the policy: ${policy.status} ${policy.body}`);
  }
  const policyID = policy.json('id');

  const keys = [];
  for (let i = 0; i < n; i++) {
    const reg = http.post(`${base}/v1/clients`, JSON.stringify({ name: `Load ${stamp} ${i}` }), params);
    if (reg.status !== 201) {
      fail(`registering client ${i}: ${reg.status} ${reg.body}`);
    }
    const assign = http.put(`${base}/v1/clients/${reg.json('client.id')}/policy`,
      JSON.stringify({ policy_id: policyID }), params);
    if (assign.status !== 200) {
      fail(`assigning the policy to client ${i}: ${assign.status} ${assign.body}`);
    }
    keys.push(reg.json('api_key.key'));
  }
  return keys;
}

// checkParams are the request parameters for one decision with key.
export function checkParams(key) {
  return {
    headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' },
    tags: { name: 'check' },
  };
}

// The body of every decision request: a path no rule matches, so each
// decision uses the policy's own bucket.
export const checkBody = JSON.stringify({ method: 'GET', path: '/api/products' });

// seconds converts a k6 duration such as 30s, 2m or 1m30s to seconds.
export function seconds(d) {
  let total = 0;
  const re = /(\d+(?:\.\d+)?)(ms|s|m|h)/g;
  let m;
  while ((m = re.exec(d)) !== null) {
    total += Number(m[1]) * { ms: 0.001, s: 1, m: 60, h: 3600 }[m[2]];
  }
  if (total <= 0) {
    fail(`cannot read the duration ${d}`);
  }
  return total;
}

// report formats the measured scenario's numbers. k6 computes trend
// percentiles from every sample, not from buckets. The rate is over the
// measured duration only: k6's own rate counts the warm-up and setup too.
export function report(title, data, measuredSeconds) {
  const d = data.metrics['http_req_duration{scenario:measure}'].values;
  const reqs = data.metrics['http_reqs{scenario:measure}'].values;
  const ok = data.metrics['checks{scenario:measure}'].values;
  const dropped = data.metrics.dropped_iterations ? data.metrics.dropped_iterations.values.count : 0;
  const ms = (v) => v.toFixed(3);
  const lines = [
    `== ${title}`,
    `requests        ${reqs.count} (${(reqs.count / measuredSeconds).toFixed(1)}/s over ${measuredSeconds}s)`,
    `status 200      ${(ok.rate * 100).toFixed(2)}%`,
    `dropped         ${dropped}`,
    `latency ms      p50 ${ms(d.med)}  p90 ${ms(d['p(90)'])}  p95 ${ms(d['p(95)'])}  p99 ${ms(d['p(99)'])}  max ${ms(d.max)}  avg ${ms(d.avg)}`,
    '',
  ];
  return { stdout: lines.join('\n') };
}

// measuredThresholds make k6 keep the measured scenario's own metrics. The
// limits are loose on purpose: the runs measure, they do not gate.
export const measuredThresholds = {
  'http_req_duration{scenario:measure}': ['max>=0'],
  'http_reqs{scenario:measure}': ['count>0'],
  'checks{scenario:measure}': ['rate>0.99'],
};
