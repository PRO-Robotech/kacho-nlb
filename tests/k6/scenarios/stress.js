// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Scenario: stress
//
// Purpose: find the breakpoint where error rate begins to climb. NOT a
// pass/fail gate — interpretation is manual ("inflection point at ~RPS=N").
//
// Profile per design §7.4: linear ramp 100 -> 2000 RPS over 10min, plateau
// briefly, ramp down. Thresholds are LOOSE (just "system stayed up") so the
// run completes and we can read the latency-vs-RPS curve from the time series.
//
// Interpretation hint:
//   - Plot http_req_duration p95 vs http_reqs/s. The inflection where p95
//     starts growing faster than linear == start of degradation.
//   - Plot http_req_failed rate vs RPS. The point where it crosses 5% is
//     the breakpoint for engineering planning.

import { runMixedIteration, teardownAll } from '../lib/mix.js';

export const options = {
  scenarios: {
    stress_ramp: {
      executor: 'ramping-arrival-rate',
      startRate: 100,
      timeUnit: '1s',
      preAllocatedVUs: 200,
      maxVUs: 1500,
      stages: [
        { duration: '8m', target: 2000 },  // ramp 100 -> 2000
        { duration: '2m', target: 2000 },  // brief plateau
        { duration: '30s', target: 0 },    // ramp down
      ],
      tags: { scenario: 'stress' },
    },
  },
  thresholds: {
    // Loose pass/fail — we want the run to complete and produce data.
    'http_req_failed': ['rate<0.50'],  // anything <50% is "still up"
    'checks': ['rate>0.50'],
    // Hard ceiling for absolute timeouts — distinguishes degradation from total stall.
    'http_req_duration{op:read}': ['p(99)<5000'],
  },
};

export default function () {
  runMixedIteration();
}

export function teardown() {
  teardownAll();
}
