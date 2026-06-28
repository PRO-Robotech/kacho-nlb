// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Scenario: smoke
//
// Purpose: post-deploy sanity. Confirms the api-gateway -> kacho-nlb path is
// healthy under a *minimal* load. NOT a benchmark — if smoke fails the
// stack is broken, not the SLO.
//
// Profile per design §7.4: 100 RPS / 30s, error rate < 1%.
//
// Implementation: constant-arrival-rate executor, capped at 100 iter/s. Pre-
// allocated VU count covers ~150ms response latency (100rps * 0.15s = 15);
// max VUs scales modestly if responses spike. Mix is the standard 60/20/10/10.

import { runMixedIteration, teardownAll } from '../lib/mix.js';

export const options = {
  scenarios: {
    smoke: {
      executor: 'constant-arrival-rate',
      rate: 100,
      timeUnit: '1s',
      duration: '30s',
      preAllocatedVUs: 20,
      maxVUs: 60,
      tags: { scenario: 'smoke' },
    },
  },
  thresholds: {
    'http_req_failed': ['rate<0.01'],
    'http_req_duration{op:read}': ['p(95)<200'],
    'checks': ['rate>0.99'],
  },
  // Smoke must FAIL FAST — don't wait on a stuck connection.
  noConnectionReuse: false,
  discardResponseBodies: false,
};

export default function () {
  runMixedIteration();
}

export function teardown() {
  teardownAll();
}
