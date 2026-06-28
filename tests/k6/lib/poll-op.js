// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Long-Running Operation polling helper.
//
// All kacho-nlb mutations return `operation.Operation`; the client polls
// `/nlb/v1/operations/{id}` until `done=true`. Worker SLO is sub-second
// for control-plane Create/Update/Delete on a quiet stand, but under load
// it can stretch; the caller picks an acceptable budget.

import { get, ROUTES } from './client.js';
import { check, sleep } from 'k6';

const DEFAULT_INTERVAL_SEC = 0.1; // 100ms
const DEFAULT_MAX_ATTEMPTS = 50;  // 5 s budget

// pollOperation polls until done=true OR attempts exhausted.
// Returns:
//   { ok: true,  response, raw }     — success
//   { ok: false, error,   raw }      — operation completed with error
//   { ok: false, code, ... }         — HTTP error / timeout
export function pollOperation(opId, opts) {
  if (!opId) return { ok: false, code: 'no-op-id' };
  const interval = (opts && opts.intervalSec) || DEFAULT_INTERVAL_SEC;
  const max = (opts && opts.maxAttempts) || DEFAULT_MAX_ATTEMPTS;
  const tag = (opts && opts.tag) || 'poll-op';

  for (let i = 0; i < max; i++) {
    const res = get(`${ROUTES.operations}/${opId}`, { op: tag });
    if (res.status !== 200) {
      return { ok: false, code: res.status, body: res.body };
    }
    let j;
    try {
      j = res.json();
    } catch (_e) {
      return { ok: false, code: 'parse-error', body: res.body };
    }
    if (j.done) {
      if (j.error) return { ok: false, error: j.error, raw: j };
      return { ok: true, response: j.response, raw: j };
    }
    sleep(interval);
  }
  return { ok: false, code: 'timeout', budgetSec: max * interval };
}

export function checkOpDone(opRes, label) {
  return check(opRes, {
    [`${label}: op completed`]: (r) => r && r.ok === true,
  });
}
