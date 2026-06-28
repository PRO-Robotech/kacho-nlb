// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Payload-template merge layer.
//
// Loads `data/payload-templates.json` ONCE at init time, then merges each
// scenario's overrides on top. This gives ops the ability to tweak knobs
// (e.g. default health-check interval, ip_version) without changing JS.

import { SharedArray } from 'k6/data';

const TEMPLATES = new SharedArray('nlb-payload-templates', function () {
  // Inline default fallback so the scenario boots even when the JSON file
  // isn't bundled at the path the runner expects. Keep in sync with the
  // JSON's `defaults` block.
  return [open('../data/payload-templates.json')];
});

let parsed = null;
function defaults() {
  if (parsed === null) {
    try {
      parsed = JSON.parse(TEMPLATES[0]);
    } catch (_e) {
      parsed = { defaults: {} };
    }
  }
  return parsed.defaults || {};
}

// fixtureBody merges template defaults under runtime overrides. Override
// wins, but missing scalar fields fall back to template.
export function fixtureBody(name, override) {
  const tmpl = defaults()[name] || {};
  return Object.assign({}, tmpl, override);
}
