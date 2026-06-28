// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Workload mix per design §7.4.
//
//   60% reads          (NLB.Get / NLB.List / TG.Get)
//   20% Create+Delete  (short LB / Listener / TG lifecycle)
//   10% AddTargets / RemoveTargets
//   10% AttachTG / DetachTG
//
// Each VU iteration picks ONE op weighted by these probabilities. The
// caller (scenario) controls VU count and iteration rate; we only decide
// "which op runs this iteration".

import {
  getLB, listLB, getTG, listTG,
  createLB, deleteLB,
  createListener, deleteListener,
  createTG, deleteTG,
  addTargets, removeTargets,
  attachTG, detachTG,
} from './dsl.js';
import { pollOperation } from './poll-op.js';
import { FIXTURES, pickOne, validateRequiredOnce } from './fixtures.js';
import { templates } from './payloads.js';
import { check } from 'k6';

// Lifecycle resources created within a scenario are tracked PER-VU so we
// can tear down in teardown(). Cross-VU sharing of mutable arrays is not
// supported by k6 — that's intentional, it keeps cleanup local.
const created = {
  lbs: [],
  listeners: [],
  tgs: [],
};

export function exportCreated() {
  return created;
}

// runMixedIteration executes ONE op picked by the design weights.
// Returns the high-level op label so the scenario can tag custom metrics.
export function runMixedIteration() {
  validateRequiredOnce();
  const r = Math.random();
  if (r < 0.60) return doRead(r);
  if (r < 0.80) return doShortLifecycle();
  if (r < 0.90) return doTargetsOp();
  return doAttachOp();
}

// --- READ (60%) ----------------------------------------------------------

function doRead(rRaw) {
  // Sub-pick within reads to ensure all three read RPCs see traffic.
  const sub = (rRaw * 100) % 3; // 0,1,2
  if (sub < 1) {
    // NLB.List with a tight page_size — exercises pagination + filter path.
    const res = listLB(`page_size=20`);
    check(res, { 'read NLB.List 2xx': (r) => r.status >= 200 && r.status < 300 });
    return 'NLB.List';
  }
  if (sub < 2) {
    // NLB.Get on a warm-set id (avoids the predictable 404 hot path).
    const id = pickOne(FIXTURES.readLbIds) || pickOne(created.lbs);
    if (!id) {
      // Fall back to a List so the read budget isn't wasted.
      listLB(`page_size=10`);
      return 'NLB.List';
    }
    const res = getLB(id);
    check(res, { 'read NLB.Get 2xx-or-404': (r) => r.status < 500 });
    return 'NLB.Get';
  }
  const id = pickOne(FIXTURES.readTgIds) || pickOne(created.tgs);
  if (!id) {
    listTG(`page_size=10`);
    return 'TG.List';
  }
  const res = getTG(id);
  check(res, { 'read TG.Get 2xx-or-404': (r) => r.status < 500 });
  return 'TG.Get';
}

// --- SHORT LIFECYCLE: Create -> Delete (20%) -----------------------------

function doShortLifecycle() {
  // Round-robin Create across LB / Listener / TG to spread load fairly.
  const pick = Math.floor(Math.random() * 3);
  if (pick === 0) return shortLBCycle();
  if (pick === 1) return shortListenerCycle();
  return shortTGCycle();
}

function shortLBCycle() {
  const c = createLB({ projectId: FIXTURES.projectId, regionId: FIXTURES.regionId });
  check(c, { 'shortLB Create 2xx': (r) => r.status >= 200 && r.status < 300 });
  if (c.status >= 300 || !c.opId) return 'NLB.Create';
  const op = pollOperation(c.opId, { tag: 'mix-create-lb', maxAttempts: 30 });
  const lbId = op.ok && op.response ? op.response.id : '';
  if (lbId) {
    created.lbs.push(lbId);
    // Immediate teardown — short cycle.
    const d = deleteLB(lbId);
    check(d, { 'shortLB Delete 2xx': (r) => r.status >= 200 && r.status < 300 });
  }
  return 'NLB.Create+Delete';
}

function shortListenerCycle() {
  // Requires a parent LB; reuse a created one if any, else fall through to
  // an LB cycle (which still exercises Create+Delete).
  const lbId = pickOne(created.lbs);
  if (!lbId || !FIXTURES.addressId) return shortLBCycle();
  const c = createListener({ lbId, addressId: FIXTURES.addressId });
  check(c, { 'shortListener Create 2xx-or-pre': (r) => r.status < 500 });
  if (c.status >= 300 || !c.opId) return 'Listener.Create';
  const op = pollOperation(c.opId, { tag: 'mix-create-listener', maxAttempts: 30 });
  const lid = op.ok && op.response ? op.response.id : '';
  if (lid) {
    created.listeners.push(lid);
    const d = deleteListener(lid);
    check(d, { 'shortListener Delete 2xx': (r) => r.status >= 200 && r.status < 300 });
  }
  return 'Listener.Create+Delete';
}

function shortTGCycle() {
  const c = createTG({ projectId: FIXTURES.projectId, regionId: FIXTURES.regionId });
  check(c, { 'shortTG Create 2xx': (r) => r.status >= 200 && r.status < 300 });
  if (c.status >= 300 || !c.opId) return 'TG.Create';
  const op = pollOperation(c.opId, { tag: 'mix-create-tg', maxAttempts: 30 });
  const tgId = op.ok && op.response ? op.response.id : '';
  if (tgId) {
    created.tgs.push(tgId);
    const d = deleteTG(tgId);
    check(d, { 'shortTG Delete 2xx': (r) => r.status >= 200 && r.status < 300 });
  }
  return 'TG.Create+Delete';
}

// --- TARGETS (10%) -------------------------------------------------------

function doTargetsOp() {
  const tgId = pickOne(created.tgs) || pickOne(FIXTURES.readTgIds);
  if (!tgId) return shortTGCycle();
  const t1 = templates.externalTarget((Date.now() % 200) + 1);
  const t2 = templates.externalTarget((Date.now() % 200) + 50);
  const add = addTargets(tgId, [t1, t2]);
  check(add, { 'AddTargets 2xx-or-pre': (r) => r.status < 500 });
  // RemoveTargets in the same iteration — 2-phase drain (Phase A is fast).
  const rem = removeTargets(tgId, [t1]);
  check(rem, { 'RemoveTargets 2xx-or-pre': (r) => r.status < 500 });
  return 'TG.Add+RemoveTargets';
}

// --- ATTACH (10%) --------------------------------------------------------

function doAttachOp() {
  const lbId = pickOne(created.lbs) || pickOne(FIXTURES.readLbIds);
  const tgId = pickOne(created.tgs) || pickOne(FIXTURES.readTgIds);
  if (!lbId || !tgId) return shortLBCycle();
  const a = attachTG(lbId, tgId);
  check(a, { 'AttachTG 2xx-or-pre': (r) => r.status < 500 });
  const d = detachTG(lbId, tgId);
  check(d, { 'DetachTG 2xx-or-pre': (r) => r.status < 500 });
  return 'NLB.Attach+DetachTG';
}

// teardownAll — best-effort cleanup of resources this VU created.
// Called from scenario teardown(). Errors are swallowed; we don't want a
// flaky cleanup to mask scenario results.
export function teardownAll() {
  for (const id of created.listeners.splice(0)) {
    deleteListener(id);
  }
  for (const id of created.lbs.splice(0)) {
    deleteLB(id);
  }
  for (const id of created.tgs.splice(0)) {
    deleteTG(id);
  }
}
