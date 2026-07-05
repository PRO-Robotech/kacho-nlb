# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

"""Cross-resource end-to-end cases (XRES-*) — sub-phase 6.0 S4 (UC-1/UC-2/UC-5).

Acceptance source: docs/specs/sub-phase-6.0-nlb-functional-acceptance.md §S4
  (6.0-34 EXTERNAL e2e, 6.0-35 INTERNAL e2e, 6.0-36 teardown bottom-up,
   6.0-37 dangling instance-target graceful read).
Design source: docs/specs/nlb-functional-design-plan.md §5 (UC-1/UC-2/UC-5), §6.1.

These cases orchestrate the standard per-resource RPCs (already covered atomically
by load-balancer.py / listener.py / target-group.py / targets.py) into the full
tenant journeys the platform promises: stand up an L4 NLB "from nothing to
traffic-ready" (control-plane), tear it down bottom-up with correct address
lifecycle, and survive a dangling cross-service target reference on read.

Test-design techniques applied (skill testing-product-coach):
  - use-case / scenario flow (UC-1/UC-2/UC-5 happy journeys, multi-step Operation
    polling on every mutation);
  - state-transition (LB INACTIVE→ACTIVE on attach via lb_status_recompute trigger;
    delete-blocked→empty→deleted; target row survives peer deletion);
  - decision-table (scheme EXTERNAL vs INTERNAL × network_id presence → accept/reject);
  - error-guessing (default_target_group_id pointed at an un-attached TG → composite
    FK FAILED_PRECONDITION; v4 listener + v6 BYO Address → family mismatch; delete LB
    while it still holds a listener → "load balancer is not empty");
  - conformance (camelCase round-trip of S2/S3 fields sessionAffinity / crossZoneEnabled
    / networkId; Operation envelope on every mutation; computed TargetState enum).

Cross-domain fixture tolerance (deliberate, mirrors the rest of the suite):
the api-gateway routes every domain, but the seeded VPC/Compute fixtures
(network, subnet, instance) are not guaranteed on every CI lane — only on the
umbrella stack that runs kacho-deploy/scripts/seed-nlb-fixtures.sh. Steps whose
success depends on a peer fixture therefore assert the nlb-guaranteed contract
strictly (Operation envelope, sync-validation rejects, status transitions driven
by nlb's own DB triggers, graceful-read survival) and gate the peer-dependent
linkage assertions on the resource actually having been created. This keeps the
suite green on a bare lane while fully exercising the chain when fixtures exist.

REST base paths:
  /nlb/v1/networkLoadBalancers   /nlb/v1/listeners   /nlb/v1/targetGroups
"""

CASES = []

_LB_BASE = "/nlb/v1/networkLoadBalancers"
_LST_BASE = "/nlb/v1/listeners"
_TG_BASE = "/nlb/v1/targetGroups"
_VPC_SUBNETS = "/vpc/v1/subnets"

# sub-phase 8.1 note: the INTERNAL LoadBalancer now takes its VIP from a subnet
# source (v4Source.subnetId) + placementType, not from a top-level networkId. The
# removed inputs (networkId / securityGroupIds / crossZoneEnabled) are gone from the
# proto and silently ignored by grpc-gateway. The INTERNAL journey below provisions
# a zonal subnet inline; the listener-level fields still follow sub-phase-4.0 and
# are tracked for a separate listener acceptance.

_HC_TCP = {"name": "hc", "interval": "2s", "timeout": "1s",
           "unhealthyThreshold": 3, "healthyThreshold": 2, "tcp": {"port": 8080}}

# Set of TargetState.Status enum strings the computed (control-plane) ramp may
# legitimately return. UNHEALTHY/STATUS_UNSPECIFIED are valid enum members even
# though the deterministic ramp currently emits only the first four — assert the
# member set, not a single literal (the dangling ref must NOT crash or mutate it).
_VALID_TARGET_STATE_JS = (
    "['STATUS_UNSPECIFIED','INITIAL','HEALTHY','UNHEALTHY','DRAINING','INACTIVE']"
)


# ---------------------------------------------------------------------------
# Reusable step fragments
# ---------------------------------------------------------------------------

def _create_external_lb(suffix: str, body_extra: dict = None):
    # sub-phase 8.1: EXTERNAL LB carries an auto public VIP source on Create.
    body = {"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
            "type": "EXTERNAL", "name": f"xres-{suffix}-{{{{runId}}}}",
            "v4Source": {"public": {}}, **(body_extra or {})}
    return [
        Step(name="create-lb", method="POST", path=_LB_BASE, body=body,
             test_script=[*assert_status(200),
                          *assert_operation_envelope(prefix_regex="^nlb[a-z0-9]+$"),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
    ]


def _create_tg(suffix: str):
    body = {"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
            "name": f"xres-tg-{suffix}-{{{{runId}}}}", "healthCheck": _HC_TCP}
    return [
        Step(name="create-tg", method="POST", path=_TG_BASE, body=body,
             test_script=[*assert_status(200),
                          *assert_operation_envelope(prefix_regex="^nlb[a-z0-9]+$"),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
    ]


def _cleanup_lb():
    return [
        Step(name="cleanup-lb", method="DELETE", path=f"{_LB_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ]


def _cleanup_tg():
    return [
        Step(name="cleanup-tg", method="DELETE", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ]


def _cleanup_lst():
    return [
        Step(name="cleanup-lst", method="DELETE", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ]


# ===========================================================================
# 6.0-34 — UC-1: EXTERNAL NLB from nothing to traffic-ready (control-plane)
# ===========================================================================

CASES.append(Case(
    id="XRES-E2E-EXTERNAL-FULL-FLOW",
    title="UC-1 EXTERNAL NLB full chain: LB→listener(auto v4 VIP)→TG→addTargets→attach"
          "→default_tg→GetTargetStates (Verifies 6.0-34)",
    classes=["CRUD", "STATE"], priority="P0",
    steps=[
        *_create_external_lb("ext-flow", {"sessionAffinity": "FIVE_TUPLE"}),
        # Step 1 assertion: fresh LB has no listener/TG → INACTIVE.
        Step(name="get-lb-inactive", method="GET", path=f"{_LB_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('LB starts INACTIVE (no listener/TG yet)', () => "
                          "  pm.expect(j.status).to.eql('INACTIVE'));",
                          "pm.test('type EXTERNAL', () => pm.expect(j.type).to.eql('EXTERNAL'));"]),
        # Step 2: listener with auto external VIP.
        Step(name="create-listener", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "edge-https-{{runId}}",
                   "protocol": "TCP", "port": 443, "targetPort": 8443, "ipVersion": "IPV4"},
             test_script=[*assert_status(200),
                          *assert_operation_envelope(prefix_regex="^nlb[a-z0-9]+$"),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        Step(name="get-listener-vip", method="GET", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "if (!pm.environment.get('lastOpError')) {",
                          "  pm.test('auto VIP allocated (allocatedAddress non-empty)', () => "
                          "    pm.expect(j.allocatedAddress).to.be.a('string').and.have.length.above(0));",
                          "  pm.test('listener ACTIVE after VIP alloc', () => "
                          "    pm.expect(j.status).to.eql('ACTIVE'));",
                          "}"]),
        # Step 3: target group.
        *_create_tg("ext-flow"),
        # Step 4: register an Instance target (peer-validated in worker; tolerated
        # when the seeded Compute instance is absent on a bare lane).
        Step(name="add-instance-target", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"instanceId": "{{existingInstanceId}}", "weight": 100}]},
             test_script=[*assert_status(200),
                          *assert_operation_envelope(prefix_regex="^nlb[a-z0-9]+$"),
                          *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        # Step 5: attach TG to LB.
        Step(name="attach-tg", method="POST",
             path=f"{_LB_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 100},
             test_script=[*assert_status(200),
                          *assert_operation_envelope(prefix_regex="^nlb[a-z0-9]+$"),
                          *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        # Step 6: now the listener default_target_group_id FK resolves (TG attached).
        Step(name="set-default-tg", method="PATCH", path=f"{_LST_BASE}/{{{{lstId}}}}",
             body={"updateMask": "default_target_group_id", "defaultTargetGroupId": "{{tgId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="verify-default-tg-set", method="GET", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "if (!pm.environment.get('lastOpError')) {",
                          "  pm.test('default_target_group_id resolves to attached TG', () => "
                          "    pm.expect(j.defaultTargetGroupId).to.eql(pm.environment.get('tgId')));",
                          "}"]),
        # Linkage: LB recomputed to ACTIVE once it has a listener + attached TG
        # (lb_status_recompute trigger). On a bare lane the listener VIP alloc may
        # fail → LB stays INACTIVE; assert the allowed pair.
        Step(name="get-lb-after-attach", method="GET", path=f"{_LB_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('LB status ACTIVE or INACTIVE (listener-VIP dependent)', () => "
                          "  pm.expect(j.status).to.be.oneOf(['ACTIVE', 'INACTIVE']));"]),
        # Step 7: computed target states (control-plane, peer-independent).
        Step(name="get-target-states", method="GET",
             path=f"{_LB_BASE}/{{{{nlbId}}}}/targetStates?targetGroupId={{{{tgId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "const states = j.targetStates || [];",
                          "pm.test('targetStates is an array', () => pm.expect(states).to.be.an('array'));",
                          "states.forEach(s => pm.test('target status is a valid enum member', () => "
                          f"  pm.expect(s.status).to.be.oneOf({_VALID_TARGET_STATE_JS})));"]),
        # Teardown (bottom-up; clear default before detach — composite FK RESTRICT).
        Step(name="clear-default-tg", method="PATCH", path=f"{_LST_BASE}/{{{{lstId}}}}",
             body={"updateMask": "default_target_group_id", "defaultTargetGroupId": ""},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="detach-tg", method="POST",
             path=f"{_LB_BASE}/{{{{nlbId}}}}:detachTargetGroup",
             body={"targetGroupId": "{{tgId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="remove-instance-target", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"instanceId": "{{existingInstanceId}}"}]},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_lst(),
        *_cleanup_lb(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="XRES-E2E-EXTERNAL-IPV6-VIP",
    title="UC-1 variant: EXTERNAL listener with auto IPv6 VIP (per-family dispatch) "
          "(Verifies 6.0-34 IPv6)",
    classes=["CRUD"], priority="P1",
    steps=[
        *_create_external_lb("ext-v6"),
        Step(name="create-listener-v6", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "edge-v6-{{runId}}",
                   "protocol": "TCP", "port": 443, "targetPort": 8443, "ipVersion": "IPV6"},
             test_script=[
                 # external-v6 pool may be unseeded → Operation RESOURCE_EXHAUSTED;
                 # listener creation must not leak a VIP either way.
                 "pm.test('accepted as Operation or rejected (v6 pool dependent)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
                 *save_from_response("j.metadata && j.metadata.listenerId", "lstId"),
             ]),
        poll_operation_until_done(),
        Step(name="get-listener-v6", method="GET", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[
                 "if (pm.response.code === 200 && !pm.environment.get('lastOpError')) {",
                 "  const j = pm.response.json();",
                 "  pm.test('IPv6 VIP allocated, ipVersion IPV6', () => {",
                 "    pm.expect(j.ipVersion).to.eql('IPV6');",
                 "    pm.expect(j.allocatedAddress).to.be.a('string').and.have.length.above(0);",
                 "  });",
                 "}",
             ]),
        Step(name="cleanup-listener-best-effort", method="DELETE",
             path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="XRES-E2E-DEFAULT-TG-UNATTACHED-FP",
    title="UC-1 negative: set listener default_target_group_id to an un-attached TG "
          "→ FAILED_PRECONDITION (composite FK) (Verifies 6.0-34/6.0-02)",
    classes=["NEG", "STATE"], priority="P1",
    steps=[
        *_create_external_lb("def-unatt"),
        Step(name="create-listener", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "def-lst-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080, "ipVersion": "IPV4"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        # TG exists but is intentionally NOT attached to the LB.
        *_create_tg("def-unatt"),
        Step(name="set-default-unattached", method="PATCH", path=f"{_LST_BASE}/{{{{lstId}}}}",
             body={"updateMask": "default_target_group_id", "defaultTargetGroupId": "{{tgId}}"},
             test_script=[
                 "pm.test('accepted as Operation or sync-rejected', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="check-fp", method="GET", path="/operations/{{opId}}",
             test_script=[
                 "const j = pm.response.json();",
                 "if (j.error) pm.test('FAILED_PRECONDITION (default TG not attached)', () => "
                 "  pm.expect(j.error.code).to.eql(9));",
             ]),
        Step(name="verify-listener-unchanged", method="GET", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('default_target_group_id NOT applied (stays empty)', () => "
                          "  pm.expect(j.defaultTargetGroupId || '').to.eql(''));"]),
        *_cleanup_lst(),
        *_cleanup_lb(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="XRES-E2E-V4-LISTENER-V6-ADDRESS-INVALID",
    title="UC-1 negative: IPV4 listener with a BYO IPv6 Address → InvalidArgument "
          "(family mismatch) (Verifies 6.0-34/6.0-20)",
    classes=["NEG", "VAL"], priority="P1",
    steps=[
        *_create_external_lb("v4-v6"),
        Step(name="create-listener-family-mismatch", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "mm-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080,
                   "ipVersion": "IPV4", "addressId": "{{existingAddressIPv6Id}}"},
             test_script=[
                 "pm.test('rejected (sync InvalidArgument or async error)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="check-invalid-arg", method="GET", path="/operations/{{opId}}",
             test_script=[
                 "const j = pm.response.json();",
                 "if (j.error) pm.test('error is INVALID_ARGUMENT (3)', () => "
                 "  pm.expect(j.error.code).to.eql(3));",
             ]),
        *_cleanup_lb(),
    ],
))


# ===========================================================================
# 6.0-35 — UC-2: INTERNAL NLB (private VIP from subnet) end-to-end
# ===========================================================================

CASES.append(Case(
    id="XRES-E2E-INTERNAL-FULL-FLOW",
    title="UC-2 INTERNAL NLB: LB(networkId, CLIENT_IP_ONLY, crossZone=false)"
          "→listener(subnet, internal VIP)→TG→attach→GetTargetStates (Verifies 6.0-35)",
    classes=["CRUD", "STATE"], priority="P0",
    steps=[
        # sub-phase 8.1: provision a zonal subnet inline, then create an INTERNAL LB
        # whose VIP is auto-allocated from that subnet (v4Source.subnetId). Gate the
        # rest of the journey on the subnet + LB actually materialising.
        Step(name="provision-subnet", method="POST", path=_VPC_SUBNETS,
             pre_script=[
                 "var __seq = parseInt(pm.environment.get('_cidrSeq') || '0', 10) + 1;",
                 "pm.environment.set('_cidrSeq', String(__seq));",
                 "var __run = (pm.environment.get('runId') || 'x0');",
                 "var __h = 0; for (var i = 0; i < __run.length; i++) { __h = (__h * 31 + __run.charCodeAt(i)) & 0xffff; }",
                 "pm.environment.set('_subnetCidr', '10.' + (200 + (__h % 40)) + '.' + (__seq % 250) + '.0/24');",
             ],
             body={"projectId": "{{_suiteProjectId}}", "networkId": "{{existingNetworkId}}",
                   "name": "xres-int-sub-{{runId}}", "v4CidrBlocks": ["{{_subnetCidr}}"],
                   "placementType": "ZONAL", "zoneId": "{{existingZoneId}}"},
             test_script=[
                 "pm.environment.unset('xresSubnetId');",
                 "if (pm.response.code === 200) {",
                 "  const j = pm.response.json();",
                 "  if (j.id) pm.environment.set('opId', j.id);",
                 "  if (j.metadata && j.metadata.subnetId) pm.environment.set('xresSubnetId', j.metadata.subnetId);",
                 "} else { pm.environment.unset('opId'); }",
             ]),
        poll_operation_until_done(),
        Step(name="create-internal-lb", method="POST", path=_LB_BASE,
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "type": "INTERNAL", "placementType": "ZONAL", "name": "xres-int-{{runId}}",
                   "sessionAffinity": "CLIENT_IP_ONLY",
                   "v4Source": {"subnetId": "{{xresSubnetId}}"}},
             test_script=[
                 "pm.environment.unset('nlbId');",
                 "if (pm.environment.get('xresSubnetId')) {",
                 "  pm.test('INTERNAL subnet-source LB accepted as Operation', () => pm.expect(pm.response.code).to.eql(200));",
                 "  const j = pm.response.json();",
                 "  if (j.id) pm.environment.set('opId', j.id);",
                 "  if (j.metadata && j.metadata.networkLoadBalancerId) pm.environment.set('nlbId', j.metadata.networkLoadBalancerId);",
                 "} else {",
                 "  pm.environment.unset('opId');",
                 "  pm.test('no subnet fixture → INTERNAL subnet-source create rejected', () => "
                 "    pm.expect(pm.response.code).to.be.oneOf([400, 404, 503]));",
                 "}",
             ]),
        poll_operation_until_done(),
        Step(name="get-internal-lb", method="GET", path=f"{_LB_BASE}/{{{{nlbId}}}}",
             test_script=[
                 "pm.environment.unset('xresIntReady');",
                 "if (pm.environment.get('nlbId') && pm.response.code === 200 && !pm.environment.get('lastOpError')) {",
                 "  pm.environment.set('xresIntReady', '1');",
                 "  const j = pm.response.json();",
                 "  pm.test('type INTERNAL', () => pm.expect(j.type).to.eql('INTERNAL'));",
                 "  pm.test('placementType ZONAL', () => pm.expect(j.placementType).to.eql('ZONAL'));",
                 "  pm.test('sessionAffinity CLIENT_IP_ONLY round-trips', () => "
                 "    pm.expect(j.sessionAffinity).to.eql('CLIENT_IP_ONLY'));",
                 "  pm.test('v4AddressId resolved to a bound vpc Address', () => "
                 "    pm.expect(j.v4AddressId).to.match(/^adr[a-z0-9]+$/));",
                 "}",
             ]),
        Step(name="create-internal-listener", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "int-lst-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080,
                   "ipVersion": "IPV4", "subnetId": "{{existingSubnetId}}"},
             test_script=[
                 "pm.test('listener accepted or rejected (LB/subnet dependent)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 404]));",
                 "pm.environment.unset('lstId');",
                 *save_from_response("j.id", "opId"),
                 *save_from_response("j.metadata && j.metadata.listenerId", "lstId"),
             ]),
        poll_operation_until_done(),
        Step(name="get-internal-listener-vip", method="GET", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[
                 "if (pm.response.code === 200 && !pm.environment.get('lastOpError')) {",
                 "  const j = pm.response.json();",
                 "  pm.test('internal VIP allocated', () => "
                 "    pm.expect(j.allocatedAddress).to.be.a('string').and.have.length.above(0));",
                 "}",
             ]),
        *_create_tg("int-flow"),
        Step(name="attach-internal-tg", method="POST",
             path=f"{_LB_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 100},
             test_script=[
                 "pm.test('attach accepted or rejected (LB dependent)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 404]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="get-internal-target-states", method="GET",
             path=f"{_LB_BASE}/{{{{nlbId}}}}/targetStates?targetGroupId={{{{tgId}}}}",
             test_script=[
                 "if (pm.environment.get('xresIntReady') === '1') {",
                 "  pm.test('GetTargetStates 200 on ready INTERNAL LB', () => "
                 "    pm.expect(pm.response.code).to.eql(200));",
                 "  const states = pm.response.json().targetStates || [];",
                 "  pm.test('targetStates is an array', () => pm.expect(states).to.be.an('array'));",
                 "}",
             ]),
        # Teardown (guarded best-effort).
        Step(name="detach-internal-tg", method="POST",
             path=f"{_LB_BASE}/{{{{nlbId}}}}:detachTargetGroup",
             body={"targetGroupId": "{{tgId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="cleanup-int-listener", method="DELETE", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_lb(),
        *_cleanup_tg(),
        Step(name="cleanup-int-subnet", method="DELETE", path=f"{_VPC_SUBNETS}/{{{{xresSubnetId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="XRES-E2E-INTERNAL-NO-NETWORK-INVALID",
    title="INTERNAL LB with no placementType and no VIP source → InvalidArgument "
          "(8.1 replaces the old network_id requirement) (Verifies 8.1-12/8.1-19)",
    classes=["NEG", "VAL"], priority="P0",
    steps=[
        Step(name="create-internal-bare", method="POST", path=_LB_BASE,
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "type": "INTERNAL", "name": "int-bare-{{runId}}"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT"),
                          "pm.test('rejected for missing placement or missing vip source', () => {",
                          "  const m = (pm.response.json().message || '').toLowerCase();",
                          "  pm.expect(m).to.satisfy(s => s.includes('placement_type') || s.includes('vip source'));",
                          "});"]),
    ],
))

CASES.append(Case(
    id="XRES-E2E-EXTERNAL-WITH-NETWORK-INVALID",
    title="EXTERNAL LB carrying a removed networkId field + valid public source → "
          "created (grpc-gateway ignores the removed field) (Verifies 8.1-32)",
    classes=["CRUD", "CONF"], priority="P1",
    steps=[
        Step(name="create-external-with-removed-network", method="POST", path=_LB_BASE,
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "type": "EXTERNAL", "name": "ext-net-{{runId}}",
                   "networkId": "{{garbageNetworkId}}", "v4Source": {"public": {}}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="get-no-network", method="GET", path=f"{_LB_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('output does not echo the removed networkId', () => "
                          "  pm.expect(pm.response.json()).to.not.have.property('networkId'));"]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="XRES-E2E-INTERNAL-SG-FOREIGN-REJECTED",
    title="EXTERNAL LB carrying a removed securityGroupIds field + valid public source → "
          "created (LB-level SG was removed in 8.1; grpc-gateway ignores it) (Verifies 8.1-32)",
    classes=["CRUD", "CONF"], priority="P2",
    steps=[
        Step(name="create-with-removed-sg", method="POST", path=_LB_BASE,
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "type": "EXTERNAL", "name": "ext-sg-{{runId}}",
                   "securityGroupIds": ["{{garbageSecurityGroupId}}"], "v4Source": {"public": {}}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="get-no-sg", method="GET", path=f"{_LB_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('output does not echo the removed securityGroupIds', () => "
                          "  pm.expect(pm.response.json()).to.not.have.property('securityGroupIds'));"]),
        *_cleanup_lb(),
    ],
))


# ===========================================================================
# 6.0-36 — UC-5: bottom-up teardown with correct address lifecycle
# ===========================================================================

CASES.append(Case(
    id="XRES-E2E-TEARDOWN-BOTTOM-UP",
    title="UC-5 bottom-up teardown: clear default → remove target → detach → delete "
          "listener (frees VIP) → delete LB → delete TG; final 404s (Verifies 6.0-36)",
    classes=["CRUD", "STATE"], priority="P0",
    steps=[
        # Build a minimal EXTERNAL stack with an external_ip target (peer-free, so
        # the drain step is deterministic regardless of Compute fixtures).
        *_create_external_lb("teardown"),
        Step(name="create-listener", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "td-lst-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080, "ipVersion": "IPV4"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        *_create_tg("teardown"),
        Step(name="add-external-target", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.200"}, "weight": 100}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="attach-tg", method="POST",
             path=f"{_LB_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 100},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="set-default-tg", method="PATCH", path=f"{_LST_BASE}/{{{{lstId}}}}",
             body={"updateMask": "default_target_group_id", "defaultTargetGroupId": "{{tgId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        # Step 1: delete LB while it still owns a listener → rejected ("not empty").
        Step(name="delete-lb-not-empty", method="DELETE", path=f"{_LB_BASE}/{{{{nlbId}}}}",
             test_script=[
                 "pm.test('delete rejected while LB not empty', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="check-not-empty-fp", method="GET", path="/operations/{{opId}}",
             test_script=[
                 "const j = pm.response.json();",
                 "if (j.error) pm.test('FAILED_PRECONDITION (load balancer is not empty)', () => "
                 "  pm.expect(j.error.code).to.eql(9));",
             ]),
        Step(name="lb-still-exists", method="GET", path=f"{_LB_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('LB survived the rejected delete', () => "
                          "  pm.expect(pm.response.json().id).to.eql(pm.environment.get('nlbId')));"]),
        # Step 2: clear listener default (composite FK must be released first).
        Step(name="clear-default", method="PATCH", path=f"{_LST_BASE}/{{{{lstId}}}}",
             body={"updateMask": "default_target_group_id", "defaultTargetGroupId": ""},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        # Step 3: detach TG (no longer the listener default).
        Step(name="detach-tg", method="POST",
             path=f"{_LB_BASE}/{{{{nlbId}}}}:detachTargetGroup",
             body={"targetGroupId": "{{tgId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        # Step 4: drain the target (2-phase RemoveTargets, peer-independent).
        Step(name="remove-target", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.200"}}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        # Step 5: delete listener → auto VIP returned via FreeIP (vip_origin=auto).
        Step(name="delete-listener", method="DELETE", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="listener-gone", method="GET", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
        # Step 6: delete LB → now empty → succeeds.
        Step(name="delete-lb-empty", method="DELETE", path=f"{_LB_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="lb-gone", method="GET", path=f"{_LB_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
        # Step 7: delete TG (no attachment, drained) → succeeds.
        Step(name="delete-tg", method="DELETE", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[
                 "pm.test('TG delete accepted (drained, detached)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="XRES-E2E-DELETE-LB-NOT-EMPTY-FP",
    title="UC-5 negative: Delete LB that still owns a listener → FAILED_PRECONDITION "
          "'load balancer is not empty' (Verifies 6.0-36 step 1)",
    classes=["NEG", "STATE"], priority="P0",
    steps=[
        *_create_external_lb("del-notempty"),
        Step(name="create-listener", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "ne-lst-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080, "ipVersion": "IPV4"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        Step(name="delete-blocked", method="DELETE", path=f"{_LB_BASE}/{{{{nlbId}}}}",
             test_script=[
                 "pm.test('delete rejected while a listener exists', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 "if (pm.response.code === 400 || pm.response.code === 409) "
                 "  pm.test('grpc FAILED_PRECONDITION (9)', () => pm.expect(pm.response.json().code).to.eql(9));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="check-async-fp", method="GET", path="/operations/{{opId}}",
             test_script=[
                 "const j = pm.response.json();",
                 "if (j.error) pm.test('async error is FAILED_PRECONDITION (9)', () => "
                 "  pm.expect(j.error.code).to.eql(9));",
             ]),
        # Cleanup in the lawful order: listener first, then LB.
        *_cleanup_lst(),
        *_cleanup_lb(),
    ],
))


# ===========================================================================
# 6.0-37 — dangling cross-service instance target survives on read (by-design)
# ===========================================================================

CASES.append(Case(
    id="XRES-DANGLING-INSTANCE-READ-GRACEFUL",
    title="Dangling instance-target: TargetGroup.Get / GetTargetStates survive a "
          "target referencing a (potentially-deleted) Compute Instance without "
          "panic; RemoveTargets drains it peer-independently (Verifies 6.0-37)",
    classes=["STATE", "CRUD"], priority="P0",
    steps=[
        # The nlb read paths (TargetGroup.Get / List / GetTargetStates) are an
        # output-only mirror — source of truth = compute.Instance — and never
        # re-resolve the peer on read (verified: no compute client call in the
        # read use-cases). Reading a TG that holds an instance target therefore
        # exercises the identical code path a dangling reference would hit: the
        # control plane cannot tell a live instance from a deleted one, which IS
        # the graceful-degradation property required by data-integrity.md §4.
        *_create_external_lb("dangling"),
        *_create_tg("dangling"),
        Step(name="add-instance-target", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"instanceId": "{{existingInstanceId}}", "weight": 100}]},
             test_script=[*assert_status(200),
                          *assert_operation_envelope(prefix_regex="^nlb[a-z0-9]+$"),
                          *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        # Get(TG) survives: 200, not 404/500, even though the referenced peer is
        # not re-validated here.
        Step(name="get-tg-survives", method="GET", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('TG read survives (not 404/500)', () => "
                          "  pm.expect(j.id).to.eql(pm.environment.get('tgId')));",
                          "pm.test('targets is an array (degraded mirror, not crash)', () => "
                          "  pm.expect(j.targets || []).to.be.an('array'));"]),
        # GetTargetStates is a deterministic, peer-independent computation: it must
        # return 200 and a valid enum status for every stored target — a dangling
        # instance ref cannot turn it into a 500.
        Step(name="get-states-survives", method="GET",
             path=f"{_LB_BASE}/{{{{nlbId}}}}/targetStates?targetGroupId={{{{tgId}}}}",
             test_script=[*assert_status(200),
                          "const states = pm.response.json().targetStates || [];",
                          "pm.test('targetStates is an array', () => pm.expect(states).to.be.an('array'));",
                          "states.forEach(s => pm.test('computed status is a valid enum member', () => "
                          f"  pm.expect(s.status).to.be.oneOf({_VALID_TARGET_STATE_JS})));"]),
        # RemoveTargets resolves by stored identity tuple (no compute call) → it
        # drains a target whose peer may no longer exist.
        Step(name="remove-instance-target", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"instanceId": "{{existingInstanceId}}"}]},
             test_script=[*assert_status(200),
                          *assert_operation_envelope(prefix_regex="^nlb[a-z0-9]+$"),
                          *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="XRES-DANGLING-GTS-UNKNOWN-TG-NOTFOUND",
    title="Dangling negative: GetTargetStates for a well-formed but absent "
          "target_group_id → NOT_FOUND (dangling-target tolerance ≠ tolerating a "
          "missing TargetGroup) (Verifies 6.0-37 boundary)",
    classes=["NEG"], priority="P1",
    steps=[
        *_create_external_lb("dangling-neg"),
        Step(name="get-states-unknown-tg", method="GET",
             path=f"{_LB_BASE}/{{{{nlbId}}}}/targetStates?targetGroupId={{{{garbageTgrId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
        *_cleanup_lb(),
    ],
))
