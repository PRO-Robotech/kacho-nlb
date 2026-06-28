// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// HTTP client + auth header injection for kacho-nlb k6 load tests.
//
// All scenarios route through api-gateway REST mux (default
// `http://localhost:18080`). The path prefix `/nlb/v1/` is fixed by proto
// `google.api.http` mappings (see kacho-proto/.../loadbalancer/v1/*.proto).
//
// Auth header layout follows the workspace api-gateway contract:
//   - `Authorization: Bearer <token>` — IAM JWT (env IAM_TOKEN)
//   - `x-kacho-actor` — opaque actor id (audit trail, env ACTOR)
//   - `x-kacho-project-id` — default project id (env EXISTING_PROJECT_ID)
//
// The k6 process re-reads __ENV for every VU, so any of these can be
// overridden per-run on the command line:
//   k6 run --env BASE_URL=... --env IAM_TOKEN=... scenarios/baseline.js

import http from 'k6/http';
import { check } from 'k6';

export const BASE_URL =
  __ENV.K6_BASE_URL || __ENV.BASE_URL || 'http://localhost:18080';

const NLB_PREFIX = '/nlb/v1';

// REST roots (single source of truth — scenarios must NOT hardcode paths).
export const ROUTES = {
  loadBalancers: `${NLB_PREFIX}/networkLoadBalancers`,
  listeners: `${NLB_PREFIX}/listeners`,
  targetGroups: `${NLB_PREFIX}/targetGroups`,
  operations: `${NLB_PREFIX}/operations`,
};

const ACTOR = __ENV.ACTOR || 'k6-load-test@kacho';
const TOKEN = __ENV.IAM_TOKEN || '';
const PROJECT_ID = __ENV.EXISTING_PROJECT_ID || '';

function baseHeaders() {
  const h = {
    'Content-Type': 'application/json',
    Accept: 'application/json',
    'x-kacho-actor': ACTOR,
  };
  if (TOKEN !== '') {
    h['Authorization'] = `Bearer ${TOKEN}`;
  }
  if (PROJECT_ID !== '') {
    h['x-kacho-project-id'] = PROJECT_ID;
  }
  return h;
}

function paramsWithTags(extraTags) {
  return {
    headers: baseHeaders(),
    // `op` and `rpc` tags are consumed by scenario thresholds (see scenarios/*.js).
    tags: Object.assign({ service: 'nlb' }, extraTags || {}),
    // Keep timeout below k6 default (60s) — surfaces deadlocks faster.
    timeout: '30s',
  };
}

export function get(path, tags) {
  return http.get(`${BASE_URL}${path}`, paramsWithTags(tags));
}

export function post(path, body, tags) {
  return http.post(`${BASE_URL}${path}`, JSON.stringify(body || {}), paramsWithTags(tags));
}

export function patch(path, body, tags) {
  return http.patch(`${BASE_URL}${path}`, JSON.stringify(body || {}), paramsWithTags(tags));
}

export function del(path, tags) {
  return http.del(`${BASE_URL}${path}`, null, paramsWithTags(tags));
}

// uniqueName returns an RFC-1123 / NLB-spec-compatible name:
//   ^[a-z][-a-z0-9]{1,61}[a-z0-9]$
// Total length <= 63.
export function uniqueName(prefix) {
  const safe = (prefix || 'lt').toLowerCase().replace(/[^a-z0-9]/g, '');
  const ts = Date.now().toString(36);
  const rnd = Math.floor(Math.random() * 1e9).toString(36);
  let n = `${safe}-${ts}${rnd}`.toLowerCase().replace(/[^a-z0-9-]/g, '');
  if (n.length > 63) n = n.slice(0, 63);
  // Trim trailing hyphen to satisfy the regex tail anchor.
  while (n.endsWith('-')) n = n.slice(0, -1);
  return n;
}

export function expectStatus(res, allowed, label) {
  return check(res, {
    [`${label}: status ok`]: (r) => allowed.includes(r.status),
  });
}

export function expect2xx(res, label) {
  return expectStatus(res, [200, 201, 202, 204], label);
}
