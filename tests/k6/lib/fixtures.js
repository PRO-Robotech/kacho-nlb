// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Pre-seeded fixture loader.
//
// kacho-nlb scenarios depend on resources OWNED by other services
// (kacho-iam Project, kacho-compute Region/Zone/Instance, kacho-vpc
// Subnet/NetworkInterface/Address). Per design §2.6 those refs cross
// service boundaries and are validated by nlb on the request-path.
//
// Rather than create those fixtures during load (which would amplify load
// against unrelated services and confound the SLO measurement), scenarios
// expect them to be pre-seeded via env vars. See `tests/k6/README.md`.
//
// All env vars are read once at module import time and frozen to keep the
// VU code allocation-free in the hot path. Validation is LAZY — required-
// fixture checks happen on first VU iteration (not at init), so `k6 inspect`
// and `k6 run --dry` succeed without env.

function envOrEmpty(name) {
  return __ENV[name] || '';
}

// Required for every scenario (validated lazily — see requireFixture).
export const FIXTURES = Object.freeze({
  projectId: envOrEmpty('EXISTING_PROJECT_ID'),
  regionId: envOrEmpty('EXISTING_REGION_ID'),

  // Optional — required ONLY by lifecycle scenarios that create Listeners
  // / Targets against real VPC resources. Read-only scenarios may omit.
  subnetId: envOrEmpty('EXISTING_SUBNET_ID'),
  addressId: envOrEmpty('EXISTING_ADDRESS_ID'),
  instanceId: envOrEmpty('EXISTING_INSTANCE_ID'),
  nicId: envOrEmpty('EXISTING_NIC_ID'),
  zoneId: envOrEmpty('EXISTING_ZONE_ID'),

  // Optional warm-set ids to feed read-heavy scenarios. Comma-separated.
  readLbIds: envOrEmpty('EXISTING_LB_IDS').split(',').filter(Boolean),
  readTgIds: envOrEmpty('EXISTING_TG_IDS').split(',').filter(Boolean),
});

// requireFixture asserts a single fixture is present, throwing otherwise.
// MUST be called from default-export / setup / handler context (not from
// module init) — k6 throws are caught and surfaced as iteration errors.
const REQUIRED_KEYS = ['projectId', 'regionId'];
const REQUIRED_HINTS = {
  projectId: 'export EXISTING_PROJECT_ID=<kacho-iam project id with editor scope>',
  regionId: 'export EXISTING_REGION_ID=ru-central1 (or your region)',
};

let validatedOnce = false;

// validateRequiredOnce is called from runMixedIteration before the first
// real op runs. Cheap (single boolean), but produces a clear error message
// when env is missing instead of cryptic 400s.
export function validateRequiredOnce() {
  if (validatedOnce) return;
  validatedOnce = true;
  const missing = [];
  for (const k of REQUIRED_KEYS) {
    const v = FIXTURES[k];
    if (!v) missing.push(`${k}: ${REQUIRED_HINTS[k]}`);
  }
  if (missing.length > 0) {
    throw new Error(
      'kacho-nlb k6 fixtures missing:\n  - ' +
        missing.join('\n  - ') +
        '\nSee tests/k6/README.md for the env list.'
    );
  }
}

export function requireFixture(field, hint) {
  const v = FIXTURES[field];
  const empty =
    v === undefined ||
    v === '' ||
    (Array.isArray(v) && v.length === 0);
  if (empty) {
    throw new Error(`Scenario requires fixture '${field}'. ${hint || ''}`);
  }
  return v;
}

// pickOne returns a deterministic-ish element by VU iteration index — avoids
// every VU hitting the same id and lets the result distribute across the
// pre-seeded set. Falls back to Math.random when called from a context
// without VU state (e.g. setup / teardown — neither hits this path).
import * as kexec from 'k6/execution';

export function pickOne(arr, salt) {
  if (!arr || arr.length === 0) return '';
  let idx;
  try {
    idx =
      ((kexec.vu.idInTest * 7919) +
        (kexec.scenario.iterationInTest || 0) +
        (salt || 0)) %
      arr.length;
  } catch (_e) {
    idx = Math.floor(Math.random() * arr.length);
  }
  return arr[idx];
}
