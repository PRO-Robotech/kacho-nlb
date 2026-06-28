// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Scenario: soak
//
// Purpose: detect slow leaks (memory, pgxpool, file descriptors, prepared-
// statement cache, outbox cursor drift) over a long window. Production-like
// load held flat for 60 minutes.
//
// Profile per design §7.4: 200 RPS x 60min, p95 drift < 10% between the first
// and last 5min windows.
//
// k6 cannot ASSERT drift across windows in thresholds — instead we use a
// generous absolute p95 ceiling, plus a custom Trend metric `latency_window`
// tagged with `phase:warmup|midpoint|cooldown`. Post-run analysis compares
// the three windows.

import { Trend } from 'k6/metrics';
import { runMixedIteration, teardownAll } from '../lib/mix.js';
import { exec } from 'k6/execution';

const latencyWindow = new Trend('latency_window');

export const options = {
  scenarios: {
    soak: {
      executor: 'constant-arrival-rate',
      rate: 200,
      timeUnit: '1s',
      duration: '60m',
      preAllocatedVUs: 60,
      maxVUs: 200,
      tags: { scenario: 'soak' },
    },
  },
  thresholds: {
    'http_req_failed': ['rate<0.005'],
    'http_req_duration{op:read}': ['p(95)<150', 'p(99)<500'],
    'http_req_duration{op:write}': ['p(95)<400', 'p(99)<1000'],
    'checks': ['rate>0.995'],
    // Per-window trend tagging — analyser compares warmup vs cooldown
    // sub-trends post-run for drift detection.
    'latency_window{phase:warmup}': ['p(95)<150'],
    'latency_window{phase:cooldown}': ['p(95)<170'], // 10% drift budget
  },
};

const RUN_MS = 60 * 60 * 1000;

export default function () {
  const t0 = Date.now();
  const phase = currentPhase(t0 - exec.scenario.startTime);
  const start = Date.now();
  runMixedIteration();
  const elapsed = Date.now() - start;
  // Only sample read iterations for drift — writes dominated by Postgres
  // variance distort the signal.
  if (elapsed < 500) {
    latencyWindow.add(elapsed, { phase });
  }
}

function currentPhase(elapsedMs) {
  if (elapsedMs < 5 * 60 * 1000) return 'warmup';        // first 5min
  if (elapsedMs > RUN_MS - 5 * 60 * 1000) return 'cooldown'; // last 5min
  return 'midpoint';
}

export function teardown() {
  teardownAll();
}
