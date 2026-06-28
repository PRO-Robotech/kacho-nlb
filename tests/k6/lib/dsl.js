// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Resource-lifecycle DSL helpers — wrap raw REST calls with sane defaults so
// scenario files stay readable.
//
// Every helper:
//   1. Tags the http call with `rpc` so per-RPC thresholds work.
//   2. Returns the parsed Operation envelope OR the resource on sync reads.
//   3. Does NOT poll — caller decides if they want to await done=true.
//
// The exception is *Sync wrappers (e.g. createLBSync), which DO poll and
// return the underlying resource. These are convenience for lifecycle
// scenarios that need a ready id for follow-up operations.

import { ROUTES, get, post, patch, del, uniqueName } from './client.js';
import { pollOperation } from './poll-op.js';
import { templates } from './payloads.js';

// ---------- NLB (NetworkLoadBalancer) ----------

export function getLB(id) {
  return get(`${ROUTES.loadBalancers}/${id}`, { rpc: 'NLB.Get', op: 'read' });
}

export function listLB(query) {
  const q = query ? `?${query}` : '';
  return get(`${ROUTES.loadBalancers}${q}`, { rpc: 'NLB.List', op: 'read' });
}

// createLB submits the request and returns { status, opId, json }.
// Does NOT poll.
export function createLB({ projectId, regionId, type = 'EXTERNAL', namePrefix = 'lb' } = {}) {
  const body = templates.createNLB({
    projectId,
    regionId,
    type,
    name: uniqueName(namePrefix),
  });
  const res = post(ROUTES.loadBalancers, body, { rpc: 'NLB.Create', op: 'write' });
  return wrapOpResponse(res);
}

export function deleteLB(id) {
  const res = del(`${ROUTES.loadBalancers}/${id}`, { rpc: 'NLB.Delete', op: 'write' });
  return wrapOpResponse(res);
}

export function attachTG(lbId, tgId) {
  const res = post(
    `${ROUTES.loadBalancers}/${lbId}:attachTargetGroup`,
    templates.attachTG({ tgId }),
    { rpc: 'NLB.AttachTargetGroup', op: 'write' }
  );
  return wrapOpResponse(res);
}

export function detachTG(lbId, tgId) {
  const res = post(
    `${ROUTES.loadBalancers}/${lbId}:detachTargetGroup`,
    { target_group_id: tgId },
    { rpc: 'NLB.DetachTargetGroup', op: 'write' }
  );
  return wrapOpResponse(res);
}

// ---------- Listener ----------

export function createListener({ lbId, addressId, namePrefix = 'lst' } = {}) {
  const body = templates.createListener({
    lbId,
    addressId,
    name: uniqueName(namePrefix),
  });
  const res = post(ROUTES.listeners, body, { rpc: 'Listener.Create', op: 'write' });
  return wrapOpResponse(res);
}

export function deleteListener(id) {
  const res = del(`${ROUTES.listeners}/${id}`, { rpc: 'Listener.Delete', op: 'write' });
  return wrapOpResponse(res);
}

// ---------- TargetGroup ----------

export function getTG(id) {
  return get(`${ROUTES.targetGroups}/${id}`, { rpc: 'TG.Get', op: 'read' });
}

export function listTG(query) {
  const q = query ? `?${query}` : '';
  return get(`${ROUTES.targetGroups}${q}`, { rpc: 'TG.List', op: 'read' });
}

export function createTG({ projectId, regionId, targets = [], namePrefix = 'tg' } = {}) {
  const body = templates.createTG({
    projectId,
    regionId,
    name: uniqueName(namePrefix),
    targets,
  });
  const res = post(ROUTES.targetGroups, body, { rpc: 'TG.Create', op: 'write' });
  return wrapOpResponse(res);
}

export function deleteTG(id) {
  const res = del(`${ROUTES.targetGroups}/${id}`, { rpc: 'TG.Delete', op: 'write' });
  return wrapOpResponse(res);
}

export function addTargets(tgId, targets) {
  const res = post(
    `${ROUTES.targetGroups}/${tgId}:addTargets`,
    { target_group_id: tgId, targets: targets || [] },
    { rpc: 'TG.AddTargets', op: 'write' }
  );
  return wrapOpResponse(res);
}

export function removeTargets(tgId, targets) {
  const res = post(
    `${ROUTES.targetGroups}/${tgId}:removeTargets`,
    { target_group_id: tgId, targets: targets || [] },
    { rpc: 'TG.RemoveTargets', op: 'write' }
  );
  return wrapOpResponse(res);
}

// ---------- Sync convenience wrappers (create + poll-to-done) ----------

export function createLBSync(args, pollOpts) {
  const r = createLB(args);
  if (r.status >= 300 || !r.opId) return { ok: false, http: r };
  const op = pollOperation(r.opId, Object.assign({ tag: 'create-lb-sync' }, pollOpts || {}));
  if (!op.ok) return { ok: false, op };
  return { ok: true, resource: op.response, op };
}

export function createTGSync(args, pollOpts) {
  const r = createTG(args);
  if (r.status >= 300 || !r.opId) return { ok: false, http: r };
  const op = pollOperation(r.opId, Object.assign({ tag: 'create-tg-sync' }, pollOpts || {}));
  if (!op.ok) return { ok: false, op };
  return { ok: true, resource: op.response, op };
}

// ---------- internals ----------

function wrapOpResponse(res) {
  const out = { status: res.status, body: res.body, opId: '', metadata: null };
  if (res.status < 300) {
    try {
      const j = res.json();
      out.opId = j && j.id ? j.id : '';
      out.metadata = j && j.metadata ? j.metadata : null;
      out.done = j && j.done === true;
      out.error = j && j.error ? j.error : null;
    } catch (_e) {
      out.parseError = true;
    }
  }
  return out;
}
