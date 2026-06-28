// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Scenario: baseline
//
// Purpose: production-like sustained load with FULL SLO assertions. This is
// the gate scenario for "is the build releaseable".
//
// Profile per design §7.4: 500 RPS / 5min, p95 <= 100ms, p99 <= 300ms.
//
// Notes on threshold semantics:
//   - http_req_duration p95/p99 apply across ALL ops; the 60% read mix
//     dominates, which is what the SLO measures.
//   - Write-path tail (p99 of mutations) is allowed up to 500ms — controlled
//     under `http_req_duration{op:write}`; tighter than smoke, looser than
//     reads since LRO submit hits writer + outbox.
//   - http_req_failed < 1% is a hard fail (any release-blocker error).

import { runMixedIteration, teardownAll } from '../lib/mix.js';

export const options = {
  scenarios: {
    baseline: {
      executor: 'constant-arrival-rate',
      rate: 500,
      timeUnit: '1s',
      duration: '5m',
      preAllocatedVUs: 100,
      maxVUs: 300,
      tags: { scenario: 'baseline' },
    },
  },
  thresholds: {
    'http_req_failed': ['rate<0.01'],
    'http_req_duration': ['p(95)<100', 'p(99)<300'],
    'http_req_duration{op:read}': ['p(95)<80', 'p(99)<200'],
    'http_req_duration{op:write}': ['p(95)<200', 'p(99)<500'],
    'checks': ['rate>0.99'],
    // Throughput floor — confirms we actually drove the configured rate.
    'http_reqs': ['count>=120000'], // 500 rps * 300s * ~0.8 headroom
  },
};

export default function () {
  runMixedIteration();
}

export function teardown() {
  teardownAll();
}
