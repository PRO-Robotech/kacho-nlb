// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Scenario: spike
//
// Purpose: burst tolerance + recovery characterisation. Step from baseline
// load up to ~3x for a short burst, then drop back to baseline and measure
// how fast latency recovers.
//
// Profile per design §7.4: 100 -> 1500 -> 100 RPS, 30s burst.
//
// Threshold strategy:
//   - During burst, latency CAN spike — but error rate must stay < 5%.
//   - After cooldown, p95 should return to within 2x baseline (custom
//     metric `recovery_latency` tagged with phase=recovery measured in
//     the post-burst window).

import { Trend } from 'k6/metrics';
import { runMixedIteration, teardownAll } from '../lib/mix.js';
import { exec } from 'k6/execution';

const recoveryLatency = new Trend('recovery_latency');

export const options = {
  scenarios: {
    spike: {
      executor: 'ramping-arrival-rate',
      startRate: 100,
      timeUnit: '1s',
      preAllocatedVUs: 60,
      maxVUs: 800,
      stages: [
        { duration: '30s', target: 100 },   // warmup baseline
        { duration: '5s',  target: 1500 },  // sharp ramp into spike
        { duration: '30s', target: 1500 },  // BURST: 30s sustained 1500 rps
        { duration: '5s',  target: 100 },   // sharp drop
        { duration: '60s', target: 100 },   // recovery window
      ],
      tags: { scenario: 'spike' },
    },
  },
  thresholds: {
    // Burst tolerance: allow some failure during the spike itself.
    'http_req_failed': ['rate<0.05'],
    'checks': ['rate>0.95'],
    // Hard absolute ceiling — surface dead/queued requests.
    'http_req_duration{op:read}': ['p(99)<3000'],
    // Recovery: after the burst, latency MUST drop back below 2x baseline.
    'recovery_latency': ['p(95)<300'], // baseline read p95<80 -> 2x+padding
  },
};

// Cached phase boundaries (ms from scenario start).
const T_BURST_START = 35 * 1000;
const T_BURST_END   = 70 * 1000;

export default function () {
  const elapsed = Date.now() - exec.scenario.startTime;
  const t0 = Date.now();
  runMixedIteration();
  const dur = Date.now() - t0;
  // Only sample the recovery window (post-burst) for the recovery metric.
  if (elapsed >= T_BURST_END + 5000) {
    recoveryLatency.add(dur);
  }
}

export function teardown() {
  teardownAll();
}
