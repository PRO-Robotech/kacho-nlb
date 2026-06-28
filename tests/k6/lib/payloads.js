// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Request-body factories. Centralised so payload shape lives in ONE place
// matching kacho-proto loadbalancer/v1 messages.
//
// Wire-name policy: grpc-gateway accepts snake_case OR lowerCamelCase. We
// emit snake_case throughout — that matches the proto definitions and is
// easier to grep against the proto source.

import { fixtureBody } from './payload-templates.js';

export const templates = {
  // CreateNetworkLoadBalancerRequest
  // proto: kacho.cloud.loadbalancer.v1.CreateNetworkLoadBalancerRequest
  createNLB({ projectId, regionId, type, name, description, labels } = {}) {
    return fixtureBody('createNLB', {
      project_id: projectId,
      region_id: regionId || '',
      type,
      name,
      description: description || 'k6-load-test',
      labels: labels || { env: 'loadtest' },
      deletion_protection: false,
      allow_zonal_shift: false,
    });
  },

  // CreateListenerRequest — uses auto-allocate VIP unless BYO addressId given.
  // proto: kacho.cloud.loadbalancer.v1.CreateListenerRequest
  createListener({ lbId, addressId, name, protocol = 'TCP', port = 80, targetPort = 8080 } = {}) {
    // address_spec is a oneof: either { address_id: "..." } or { auto_allocate: {...} }.
    const addressSpec = addressId
      ? { address_id: addressId }
      : { auto_allocate: {} };
    return fixtureBody('createListener', {
      load_balancer_id: lbId,
      name,
      description: 'k6-load-test',
      labels: { env: 'loadtest' },
      protocol,
      port,
      target_port: targetPort,
      ip_version: 'IPV4',
      address_spec: addressSpec,
      proxy_protocol_v2: false,
    });
  },

  // CreateTargetGroupRequest
  // proto: kacho.cloud.loadbalancer.v1.CreateTargetGroupRequest
  createTG({ projectId, regionId, name, targets = [] } = {}) {
    return fixtureBody('createTG', {
      project_id: projectId,
      region_id: regionId || '',
      name,
      description: 'k6-load-test',
      labels: { env: 'loadtest' },
      targets,
      health_check: defaultHealthCheck(name),
      deregistration_delay_seconds: 30,
      slow_start_seconds: 0,
    });
  },

  // AttachNetworkLoadBalancerTargetGroupRequest
  attachTG({ tgId }) {
    return fixtureBody('attachTG', {
      attached_target_group: {
        target_group_id: tgId,
        health_checks: [defaultHealthCheck('attach')],
      },
    });
  },

  // Target — variant (4) externalIP. Bogon-safe TEST-NET-3 range (RFC 5737).
  externalTarget(addressOctet, weight = 100) {
    const last = ((addressOctet || 1) % 254) + 1;
    return {
      external_ip: { address: `203.0.113.${last}`, zone_id: '' },
      weight,
    };
  },

  // Target — variant (1) instance_id.
  instanceTarget(instanceId, weight = 100) {
    return { instance_id: instanceId, weight };
  },

  // Target — variant (3) ip_ref (in-cloud raw IP scoped to subnet).
  ipRefTarget(subnetId, address, weight = 100) {
    return { ip_ref: { subnet_id: subnetId, address }, weight };
  },
};

function defaultHealthCheck(suffix) {
  // Names must satisfy ^[a-z][-a-z0-9]{1,61}[a-z0-9]$ — derive from suffix.
  const cleaned = (suffix || 'hc').toLowerCase().replace(/[^a-z0-9-]/g, '');
  const trimmed = cleaned.length < 3 ? `hc-${cleaned}-0` : cleaned.slice(0, 60);
  return {
    name: ensureValidName(trimmed),
    interval: '2s',
    timeout: '1s',
    unhealthy_threshold: 2,
    healthy_threshold: 2,
    tcp_options: { port: 80 },
  };
}

function ensureValidName(s) {
  let n = s;
  if (n.length < 3) n = `${n}-hc`;
  if (n.length > 63) n = n.slice(0, 63);
  while (n.endsWith('-')) n = n.slice(0, -1);
  if (!/^[a-z]/.test(n)) n = `h${n}`;
  return n;
}
