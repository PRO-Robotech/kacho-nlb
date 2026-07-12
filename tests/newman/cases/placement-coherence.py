# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

"""Placement-coherence cases (ZC-NLB-*) — track B GAP-1 / GAP-2 (RED → GREEN).

Acceptance: docs/specs/sub-phase-nlb-vpc-zone-coherence-acceptance.md
  * GAP-1 ZC-NLB-ZONE-01/02 — ZONAL dualstack: обе VIP-семьи в ОДНОЙ зоне.
  * GAP-2 ZC-NLB-REGION-01/03 — INTERNAL LB VIP subnet/address ∈ region lb.region_id.
Norm: .claude/rules/data-integrity.md §Placement-coherence.

Behaviour-level (skill testing-product-coach): negative-кейсы ассертят ТОЧНУЮ строку
placement-coherence ошибки (не только grpc-код). RED до фикса rpc-implementer'а:
разнозональный dualstack и чужерегиональный subnet-source сейчас проходят
(create.go сверяет только same-network + placement TYPE), поэтому Create отдаёт 200
Operation вместо sync 400 → negative-кейсы красные до GREEN.

Fixture-модель (умбрелла-стенд, environments/*.postman_environment.json):
  * existingZoneId (ru-central1-a) и existingZoneAltId (ru-central1-b) — ДВЕ зоны
    ОДНОГО региона existingRegionId (ru-central1) → cross-zone same-region для GAP-1.
  * existingRegionAltId (ru-central2) — чужой регион для GAP-2.
Cross-domain fixture tolerance (зеркалит load-balancer.py): vpc Subnet provisioning
идёт inline через api-gateway; если фикстура не материализовалась — кейс ассертит
lawful fixture-absent rejection (suite остаётся зелёным), строгий контракт — когда
subnet id сохранён.

REST base: /nlb/v1/networkLoadBalancers ; vpc subnet: /vpc/v1/subnets
"""

CASES = []

_LB = "/nlb/v1/networkLoadBalancers"
_VPC_SUBNETS = "/vpc/v1/subnets"

# GAP-1 same-zone contract text (acceptance ZC-NLB-ZONE-01, Q1-дефолт).
_MSG_SAME_ZONE = "dualstack load balancer families must resolve to the same zone"
# GAP-2 region-coherence contract text (acceptance ZC-NLB-REGION-01/02, verbatim).
# NOTE(reconcile): краткая формулировка задачи трека B ("load balancer VIP must be in
# the same region") — пересказ; источник истины RED→GREEN — acceptance-док (verbatim).
_MSG_WRONG_REGION = "load balancer vip subnet must be in the same region as the load balancer"


def _cidr_pre():
    """Pre-request: run-scoped уникальный v4 /24 и v6 /64 (separates parallel runs)."""
    return [
        "var __seq = parseInt(pm.environment.get('_zcSeq') || '0', 10) + 1;",
        "pm.environment.set('_zcSeq', String(__seq));",
        "var __run = (pm.environment.get('runId') || 'x0');",
        "var __h = 0; for (var i = 0; i < __run.length; i++) { __h = (__h * 31 + __run.charCodeAt(i)) & 0xffff; }",
        "pm.environment.set('_zcV4Cidr', '10.' + (150 + (__h % 40)) + '.' + (__seq % 250) + '.0/24');",
        "pm.environment.set('_zcV6Cidr', 'fd' + (10 + (__h % 80)).toString(16) + ':' + (__seq % 9000).toString(16) + '::/64');",
    ]


def _provision_zonal_subnet(zone_var, suffix, save_var, family="v4"):
    """Provision ZONAL vpc Subnet in a given zone env-var; save its id (fixture-tolerant)."""
    cidr_field = "v4CidrBlocks" if family == "v4" else "v6CidrBlocks"
    cidr_var = "_zcV4Cidr" if family == "v4" else "_zcV6Cidr"
    return [
        Step(name=f"prov-zonal-{suffix}", method="POST", path=_VPC_SUBNETS,
             pre_script=_cidr_pre(),
             body={"projectId": "{{_suiteProjectId}}", "networkId": "{{existingNetworkId}}",
                   "name": f"zc-{suffix}-{{{{runId}}}}", "placementType": "ZONAL",
                   "zoneId": f"{{{{{zone_var}}}}}", cidr_field: [f"{{{{{cidr_var}}}}}"]},
             test_script=[
                 f"pm.environment.unset('{save_var}');",
                 "if (pm.response.code === 200) {",
                 "  const j = pm.response.json();",
                 "  if (j.id) pm.environment.set('opId', j.id);",
                 f"  if (j.metadata && j.metadata.subnetId) pm.environment.set('{save_var}', j.metadata.subnetId);",
                 "} else { pm.environment.unset('opId'); }",
             ]),
        poll_operation_until_done(),
    ]


def _provision_regional_subnet(region_var, suffix, save_var):
    """Provision REGIONAL (anycast) vpc Subnet in a given region env-var; save its id."""
    return [
        Step(name=f"prov-regional-{suffix}", method="POST", path=_VPC_SUBNETS,
             pre_script=_cidr_pre(),
             body={"projectId": "{{_suiteProjectId}}", "networkId": "{{existingNetworkId}}",
                   "name": f"zc-{suffix}-{{{{runId}}}}", "placementType": "REGIONAL",
                   "regionId": f"{{{{{region_var}}}}}", "v4CidrBlocks": ["{{_zcV4Cidr}}"]},
             test_script=[
                 f"pm.environment.unset('{save_var}');",
                 "if (pm.response.code === 200) {",
                 "  const j = pm.response.json();",
                 "  if (j.id) pm.environment.set('opId', j.id);",
                 f"  if (j.metadata && j.metadata.subnetId) pm.environment.set('{save_var}', j.metadata.subnetId);",
                 "} else { pm.environment.unset('opId'); }",
             ]),
        poll_operation_until_done(),
    ]


def _cleanup_vpc(id_var):
    return [
        Step(name=f"zc-cleanup-{id_var}", method="DELETE", path=f"{_VPC_SUBNETS}/{{{{{id_var}}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ]


def _cleanup_lb():
    return [
        Step(name="zc-cleanup-lb", method="DELETE", path=f"{_LB}/{{{{zcLbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ]


# ---------------------------------------------------------------------------
# GAP-1 — ZONAL dualstack same-zone
# ---------------------------------------------------------------------------

CASES.append(Case(
    # index: ZC-NLB-ZONE-01 (placement-coherence GAP-1)
    id="ZC-NLB-ZONE-01-NEG-DUALSTACK-CROSS-ZONE",
    title="ZONAL dualstack v4/v6 в разных зонах одного региона → sync 400 same-zone "
          "(Verifies ZC-NLB-ZONE-01)",
    classes=["NEG", "CONF"], priority="P1",
    steps=[
        *_provision_zonal_subnet("existingZoneId", "z1v4", "zcSubV4Id", family="v4"),
        *_provision_zonal_subnet("existingZoneAltId", "z2v6", "zcSubV6Id", family="v6"),
        Step(name="create-cross-zone", method="POST", path=_LB,
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{existingRegionId}}",
                   "type": "INTERNAL", "placementType": "ZONAL", "name": "zc-xz-{{runId}}",
                   "v4Source": {"subnetId": "{{zcSubV4Id}}"},
                   "v6Source": {"subnetId": "{{zcSubV6Id}}"}},
             test_script=[
                 "if (pm.environment.get('zcSubV4Id') && pm.environment.get('zcSubV6Id')) {",
                 "  pm.test('cross-zone dualstack rejected sync 400', () => pm.expect(pm.response.code).to.eql(400));",
                 "  const j = pm.response.json();",
                 "  pm.test('grpc code 3 (INVALID_ARGUMENT)', () => pm.expect(j.code).to.eql(3));",
                 f"  pm.test('same-zone verbatim message', () => pm.expect(j.message).to.eql({_MSG_SAME_ZONE!r}));",
                 "} else {",
                 "  pm.test('no dual zonal subnet fixture → lawful rejection, never silent 200', () => "
                 "    pm.expect(pm.response.code).to.be.oneOf([400, 404, 503]));",
                 "}",
             ]),
        *_cleanup_vpc("zcSubV4Id"),
        *_cleanup_vpc("zcSubV6Id"),
    ],
))

CASES.append(Case(
    # index: ZC-NLB-ZONE-02 (placement-coherence GAP-1 happy)
    id="ZC-NLB-ZONE-02-DUALSTACK-SAME-ZONE-OK",
    title="ZONAL dualstack обе VIP в одной зоне → sync accept as Operation "
          "(Verifies ZC-NLB-ZONE-02)",
    classes=["CRUD"], priority="P1",
    steps=[
        *_provision_zonal_subnet("existingZoneId", "szv4", "zcSubV4Id", family="v4"),
        *_provision_zonal_subnet("existingZoneId", "szv6", "zcSubV6Id", family="v6"),
        Step(name="create-same-zone", method="POST", path=_LB,
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{existingRegionId}}",
                   "type": "INTERNAL", "placementType": "ZONAL", "name": "zc-sz-{{runId}}",
                   "v4Source": {"subnetId": "{{zcSubV4Id}}"},
                   "v6Source": {"subnetId": "{{zcSubV6Id}}"}},
             test_script=[
                 "pm.environment.unset('zcLbId');",
                 "if (pm.environment.get('zcSubV4Id') && pm.environment.get('zcSubV6Id')) {",
                 "  pm.test('same-zone dualstack accepted as Operation (200, not placement-rejected)', () => "
                 "    pm.expect(pm.response.code).to.eql(200));",
                 "  const j = pm.response.json();",
                 "  if (j.id) pm.environment.set('opId', j.id);",
                 "  if (j.metadata && j.metadata.networkLoadBalancerId) pm.environment.set('zcLbId', j.metadata.networkLoadBalancerId);",
                 "} else {",
                 "  pm.environment.unset('opId');",
                 "  pm.test('no fixture → lawful rejection', () => pm.expect(pm.response.code).to.be.oneOf([400, 404, 503]));",
                 "}",
             ]),
        poll_operation_until_done(),
        Step(name="zc-cleanup-lb-cond", method="DELETE", path=f"{_LB}/{{{{zcLbId}}}}",
             test_script=[
                 "if (!pm.environment.get('zcLbId')) { pm.environment.unset('opId'); return; }",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_vpc("zcSubV4Id"),
        *_cleanup_vpc("zcSubV6Id"),
    ],
))


# ---------------------------------------------------------------------------
# GAP-2 — region-coherence VIP↔LoadBalancer (INTERNAL)
# ---------------------------------------------------------------------------

CASES.append(Case(
    # index: ZC-NLB-REGION-01 (placement-coherence GAP-2)
    id="ZC-NLB-REGION-01-NEG-SUBNET-WRONG-REGION",
    title="INTERNAL REGIONAL LB (R1) + REGIONAL subnet-source региона R2 → sync 400 wrong-region "
          "(Verifies ZC-NLB-REGION-01/02)",
    classes=["NEG", "CONF"], priority="P1",
    steps=[
        *_provision_regional_subnet("existingRegionAltId", "r2", "zcSubR2Id"),
        Step(name="create-wrong-region", method="POST", path=_LB,
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{existingRegionId}}",
                   "type": "INTERNAL", "placementType": "REGIONAL", "name": "zc-wr-{{runId}}",
                   "v4Source": {"subnetId": "{{zcSubR2Id}}"}},
             test_script=[
                 "if (pm.environment.get('zcSubR2Id')) {",
                 "  pm.test('cross-region subnet rejected sync 400', () => pm.expect(pm.response.code).to.eql(400));",
                 "  const j = pm.response.json();",
                 "  pm.test('grpc code 3 (INVALID_ARGUMENT)', () => pm.expect(j.code).to.eql(3));",
                 f"  pm.test('wrong-region verbatim message', () => pm.expect(j.message).to.eql({_MSG_WRONG_REGION!r}));",
                 "} else {",
                 "  pm.test('no cross-region subnet fixture → lawful rejection, never silent 200', () => "
                 "    pm.expect(pm.response.code).to.be.oneOf([400, 404, 503]));",
                 "}",
             ]),
        *_cleanup_vpc("zcSubR2Id"),
    ],
))

CASES.append(Case(
    # index: ZC-NLB-REGION-03 (placement-coherence GAP-2 happy)
    id="ZC-NLB-REGION-03-SAME-REGION-OK",
    title="INTERNAL REGIONAL LB (R1) + REGIONAL subnet-source региона R1 → accepted as Operation "
          "(Verifies ZC-NLB-REGION-03)",
    classes=["CRUD"], priority="P1",
    steps=[
        *_provision_regional_subnet("existingRegionId", "r1", "zcSubR1Id"),
        Step(name="create-same-region", method="POST", path=_LB,
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{existingRegionId}}",
                   "type": "INTERNAL", "placementType": "REGIONAL", "name": "zc-sr-{{runId}}",
                   "v4Source": {"subnetId": "{{zcSubR1Id}}"}},
             test_script=[
                 "pm.environment.unset('zcLbId');",
                 "if (pm.environment.get('zcSubR1Id')) {",
                 "  pm.test('same-region subnet accepted as Operation (200, not region-rejected)', () => "
                 "    pm.expect(pm.response.code).to.eql(200));",
                 "  const j = pm.response.json();",
                 "  if (j.id) pm.environment.set('opId', j.id);",
                 "  if (j.metadata && j.metadata.networkLoadBalancerId) pm.environment.set('zcLbId', j.metadata.networkLoadBalancerId);",
                 "} else {",
                 "  pm.environment.unset('opId');",
                 "  pm.test('no fixture → lawful rejection', () => pm.expect(pm.response.code).to.be.oneOf([400, 404, 503]));",
                 "}",
             ]),
        poll_operation_until_done(),
        Step(name="zc-cleanup-lb-sr", method="DELETE", path=f"{_LB}/{{{{zcLbId}}}}",
             test_script=[
                 "if (!pm.environment.get('zcLbId')) { pm.environment.unset('opId'); return; }",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_vpc("zcSubR1Id"),
    ],
))
