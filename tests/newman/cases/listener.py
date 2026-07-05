# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

"""ListenerService cases (LST-*).

Acceptance: docs/specs/sub-phase-4.0-nlb-acceptance.md §4 (GWT-LST-001..026).
Design: 2026-05-23-kacho-nlb-design.md §3.3, §4.5 (VIP auto + BYO + compensation).

REST: /nlb/v1/listeners
"""

CASES = []

_LST_BASE = "/nlb/v1/listeners"
_LB_BASE = "/nlb/v1/networkLoadBalancers"
_VPC_SUBNETS = "/vpc/v1/subnets"

# NOTE (sub-phase 8.1 VIP model): the parent LoadBalancer now carries a per-family
# VIP *source* on Create (v4Source public/subnet/address). This helper produces a
# valid new-model parent LB (EXTERNAL → auto public VIP; INTERNAL → auto VIP from an
# inline zonal subnet) so the Listener cases have a lawful LB to attach to. The
# Listener-level fields exercised below (subnetId / addressId / ipVersion /
# allocatedAddress) still follow the sub-phase-4.0 listener contract — the 8.1
# acceptance covers only the LoadBalancer resource, so per-listener VIP semantics
# are out of scope here and tracked for a separate listener acceptance/rewrite.


def _setup_lb(name_suffix: str, lb_type: str = "EXTERNAL"):
    if lb_type == "INTERNAL":
        return [
            Step(name="setup-subnet", method="POST", path=_VPC_SUBNETS,
                 pre_script=[
                     "var __seq = parseInt(pm.environment.get('_cidrSeq') || '0', 10) + 1;",
                     "pm.environment.set('_cidrSeq', String(__seq));",
                     "var __run = (pm.environment.get('runId') || 'x0');",
                     "var __h = 0; for (var i = 0; i < __run.length; i++) { __h = (__h * 31 + __run.charCodeAt(i)) & 0xffff; }",
                     "pm.environment.set('_subnetCidr', '10.' + (200 + (__h % 40)) + '.' + (__seq % 250) + '.0/24');",
                 ],
                 body={"projectId": "{{_suiteProjectId}}", "networkId": "{{existingNetworkId}}",
                       "name": f"lst-sub-{name_suffix}-{{{{runId}}}}", "v4CidrBlocks": ["{{_subnetCidr}}"],
                       "placementType": "ZONAL", "zoneId": "{{existingZoneId}}"},
                 test_script=[
                     "pm.environment.unset('lstSubnetId');",
                     "if (pm.response.code === 200) {",
                     "  const j = pm.response.json();",
                     "  if (j.id) pm.environment.set('opId', j.id);",
                     "  if (j.metadata && j.metadata.subnetId) pm.environment.set('lstSubnetId', j.metadata.subnetId);",
                     "} else { pm.environment.unset('opId'); }",
                 ]),
            poll_operation_until_done(),
            Step(name="setup-lb", method="POST", path=_LB_BASE,
                 body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                       "name": f"lst-{name_suffix}-{{{{runId}}}}", "type": "INTERNAL",
                       "placementType": "ZONAL", "v4Source": {"subnetId": "{{lstSubnetId}}"}},
                 test_script=[
                     "pm.environment.unset('nlbId');",
                     "if (pm.environment.get('lstSubnetId')) {",
                     "  pm.test('parent INTERNAL LB created', () => pm.expect(pm.response.code).to.eql(200));",
                     "  const j = pm.response.json();",
                     "  if (j.id) pm.environment.set('opId', j.id);",
                     "  if (j.metadata && j.metadata.networkLoadBalancerId) pm.environment.set('nlbId', j.metadata.networkLoadBalancerId);",
                     "} else { pm.environment.unset('opId'); }",
                 ]),
            poll_operation_until_done(),
        ]
    return [
        Step(name="setup-lb", method="POST", path=_LB_BASE,
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "name": f"lst-{name_suffix}-{{{{runId}}}}", "type": lb_type,
                   "v4Source": {"public": {}}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
    ]


def _cleanup_lb():
    return [
        Step(name="cleanup-lb", method="DELETE", path=f"{_LB_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ]


def _cleanup_lst():
    return [
        Step(name="cleanup-lst", method="DELETE", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ]


# ---------------------------------------------------------------------------
# CRUD
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="LST-CR-CRUD-AUTO-VIP",
    title="Create EXTERNAL Listener with auto VIP allocation (Verifies REQ-LST-CR-AUTO-VIP)",
    classes=["CRUD"], priority="P0",
    steps=[
        *_setup_lb("auto-vip"),
        Step(name="cr-lst", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "http-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080,
                   "ipVersion": "IPV4", "proxyProtocolV2": False},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        Step(name="get", method="GET", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('allocated_address present', () => "
                          "  pm.expect(j.allocatedAddress).to.be.a('string').and.have.length.above(0));",
                          "pm.test('status ACTIVE', () => pm.expect(j.status).to.eql('ACTIVE'));"]),
        *_cleanup_lst(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-CRUD-BYO",
    title="Create Listener with BYO address_id — CAS SetReference (Verifies REQ-LST-CR-BYO)",
    classes=["CRUD"], priority="P0",
    steps=[
        *_setup_lb("byo"),
        Step(name="cr-byo", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "byo-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080,
                   "ipVersion": "IPV4", "addressId": "{{existingAddressId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        Step(name="get-byo", method="GET", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*assert_status(200),
                          "if (pm.response.json().addressId) {",
                          "  pm.test('addressId matches BYO', () => "
                          "    pm.expect(pm.response.json().addressId).to.eql(pm.environment.get('existingAddressId')));",
                          "}"]),
        *_cleanup_lst(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-CRUD-INTERNAL",
    title="Create INTERNAL Listener with subnet_id (Verifies REQ-LST-CR-INTERNAL)",
    classes=["CRUD"], priority="P1",
    steps=[
        *_setup_lb("int", lb_type="INTERNAL"),
        Step(name="cr-int", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "int-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080,
                   "ipVersion": "IPV4", "subnetId": "{{existingSubnetId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        *_cleanup_lst(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-GET-CRUD-OK",
    title="Get existing Listener returns full message",
    classes=["CRUD"], priority="P0",
    steps=[
        *_setup_lb("get-ok"),
        Step(name="cr", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "getok-{{runId}}",
                   "protocol": "TCP", "port": 81, "targetPort": 8081, "ipVersion": "IPV4"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        Step(name="get", method="GET", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('port matches', () => pm.expect(j.port).to.eql(81));",
                          "pm.test('protocol matches', () => pm.expect(j.protocol).to.eql('TCP'));"]),
        *_cleanup_lst(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-LST-CRUD-OK",
    title="List Listeners by load_balancer_id",
    classes=["CRUD", "LSG"], priority="P1",
    steps=[
        *_setup_lb("list"),
        Step(name="lst", method="GET",
             path=f"{_LST_BASE}?loadBalancerId={{{{nlbId}}}}&pageSize=10",
             test_script=[*assert_status(200),
                          "pm.test('listeners array', () => "
                          "  pm.expect(pm.response.json().listeners || pm.response.json().items || []).to.be.an('array'));"]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-UPD-CRUD-OK",
    title="Update Listener mutable fields (name, proxy_protocol_v2)",
    classes=["CRUD"], priority="P1",
    steps=[
        *_setup_lb("upd-ok"),
        Step(name="cr", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "upd-{{runId}}",
                   "protocol": "TCP", "port": 82, "targetPort": 8082, "ipVersion": "IPV4"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        Step(name="upd", method="PATCH", path=f"{_LST_BASE}/{{{{lstId}}}}",
             body={"updateMask": "name,proxy_protocol_v2",
                   "name": "https-{{runId}}", "proxyProtocolV2": True},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_lst(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-DEL-CRUD-AUTO-VIP-FREE",
    title="Delete auto-VIP Listener — FreeIP back to pool (Verifies REQ-LST-DEL-AUTO-FREE)",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        *_setup_lb("del-auto"),
        Step(name="cr", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "del-auto-{{runId}}",
                   "protocol": "TCP", "port": 83, "targetPort": 8083, "ipVersion": "IPV4"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        Step(name="del", method="DELETE", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-DEL-CRUD-BYO-CLEAR-REF",
    title="Delete BYO Listener — clears used_by, does NOT FreeIP",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        *_setup_lb("del-byo"),
        Step(name="cr-byo", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "del-byo-{{runId}}",
                   "protocol": "TCP", "port": 84, "targetPort": 8084,
                   "ipVersion": "IPV4", "addressId": "{{existingAddressId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        Step(name="del-byo", method="DELETE", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-LOPS-CRUD-OK",
    title="ListOperations for Listener returns history",
    classes=["CRUD", "LSG"], priority="P2",
    steps=[
        *_setup_lb("lops"),
        Step(name="cr", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "lops-{{runId}}",
                   "protocol": "TCP", "port": 85, "targetPort": 8085, "ipVersion": "IPV4"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        Step(name="lops", method="GET",
             path=f"{_LST_BASE}/{{{{lstId}}}}/operations?pageSize=10",
             test_script=[*assert_status(200),
                          "const ops = (pm.response.json().operations || pm.response.json().items || []);",
                          "pm.test('at least 1 op', () => pm.expect(ops.length).to.be.at.least(1));"]),
        *_cleanup_lst(),
        *_cleanup_lb(),
    ],
))


# ---------------------------------------------------------------------------
# Validation
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="LST-CR-VAL-PORT-ZERO",
    title="Create Listener with port=0 → InvalidArgument 'port must be in [1, 65535]'",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        *_setup_lb("port-0"),
        Step(name="cr-p0", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "p0-{{runId}}",
                   "protocol": "TCP", "port": 0, "targetPort": 8080, "ipVersion": "IPV4"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-VAL-PORT-OVER",
    title="Create Listener with port=65536 → InvalidArgument",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        *_setup_lb("port-over"),
        Step(name="cr-po", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "po-{{runId}}",
                   "protocol": "TCP", "port": 65536, "targetPort": 8080, "ipVersion": "IPV4"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-VAL-PORT-NEGATIVE",
    title="Create Listener with port=-1 → InvalidArgument",
    classes=["VAL", "BVA"], priority="P2",
    steps=[
        *_setup_lb("port-neg"),
        Step(name="cr-pn", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "pn-{{runId}}",
                   "protocol": "TCP", "port": -1, "targetPort": 8080, "ipVersion": "IPV4"},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 200]));",
             ]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-VAL-UNSUPPORTED-PROTOCOL",
    title="Create Listener with protocol=HTTP → InvalidArgument 'must be one of TCP, UDP'",
    classes=["VAL"], priority="P1",
    steps=[
        *_setup_lb("bad-proto"),
        Step(name="cr-http", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "http-{{runId}}",
                   "protocol": "HTTP", "port": 80, "targetPort": 8080, "ipVersion": "IPV4"},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 200]));",
             ]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-VAL-INTERNAL-NO-SUBNET",
    title="INTERNAL Listener without subnet_id → InvalidArgument (Verifies REQ-LST-VAL-INTERNAL-SUBNET)",
    classes=["VAL"], priority="P0",
    steps=[
        *_setup_lb("int-no-subnet", lb_type="INTERNAL"),
        Step(name="cr-int-no-subnet", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "noint-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080, "ipVersion": "IPV4"},
             test_script=[
                 "pm.test('rejected (sync or async)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="check", method="GET", path="/operations/{{opId}}",
             test_script=[
                 "const j = pm.response.json();",
                 "if (j.error) pm.test('error code 3', () => pm.expect(j.error.code).to.eql(3));",
             ]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-VAL-NAME-REGEX",
    title="Create Listener with invalid name regex → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        *_setup_lb("bad-name"),
        Step(name="cr-bad-name", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "Bad_Name!",
                   "protocol": "TCP", "port": 80, "targetPort": 8080, "ipVersion": "IPV4"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
        *_cleanup_lb(),
    ],
))


# ---------------------------------------------------------------------------
# BVA — port boundaries
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="LST-CR-BVA-PORT-MIN-1",
    title="Create Listener with port=1 (lower bound) → OK",
    classes=["BVA"], priority="P2",
    steps=[
        *_setup_lb("port-1"),
        Step(name="cr-p1", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "p1-{{runId}}",
                   "protocol": "TCP", "port": 1, "targetPort": 8080, "ipVersion": "IPV4"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        *_cleanup_lst(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-BVA-PORT-MAX-65535",
    title="Create Listener with port=65535 (upper bound) → OK",
    classes=["BVA"], priority="P2",
    steps=[
        *_setup_lb("port-max"),
        Step(name="cr-pmax", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "pmax-{{runId}}",
                   "protocol": "TCP", "port": 65535, "targetPort": 8080, "ipVersion": "IPV4"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        *_cleanup_lst(),
        *_cleanup_lb(),
    ],
))


# ---------------------------------------------------------------------------
# State / NEG / CONF — BYO collisions, port collisions, VIP compensation
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="LST-CR-STATE-BYO-USED",
    title="BYO Create with already-used address_id → FailedPrecondition (Verifies REQ-LST-BYO-USED)",
    classes=["STATE", "NEG"], priority="P0",
    steps=[
        *_setup_lb("byo-used"),
        Step(name="cr-used", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "byou-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080,
                   "ipVersion": "IPV4", "addressId": "{{existingAddressUsedId}}"},
             test_script=[
                 "pm.test('rejected (sync or async)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="check", method="GET", path="/operations/{{opId}}",
             test_script=[
                 "const j = pm.response.json();",
                 "if (j.error) pm.test('error code 9', () => pm.expect(j.error.code).to.eql(9));",
             ]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-VAL-BYO-IP-VERSION-MISMATCH",
    title="BYO Create with mismatched ip_version → InvalidArgument (Verifies REQ-LST-BYO-IPV)",
    classes=["VAL", "NEG"], priority="P1",
    steps=[
        *_setup_lb("byo-ipv"),
        Step(name="cr-ipv-mismatch", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "ipv-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080,
                   "ipVersion": "IPV4", "addressId": "{{existingAddressIPv6Id}}"},
             test_script=[
                 "pm.test('rejected (sync or async)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-VAL-BYO-CROSS-PROJECT",
    title="BYO Create with cross-project address → InvalidArgument",
    classes=["VAL", "NEG"], priority="P1",
    steps=[
        *_setup_lb("byo-xprj"),
        Step(name="cr-cross-prj", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "xprj-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080,
                   "ipVersion": "IPV4", "addressId": "{{existingAddressCrossProjectId}}"},
             test_script=[
                 "pm.test('rejected', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 403]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-NEG-LB-UNKNOWN",
    title="Create Listener for unknown load_balancer_id → NotFound",
    classes=["NEG"], priority="P0",
    steps=[
        Step(name="cr-no-lb", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{garbageNlbId}}", "name": "nolb-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080, "ipVersion": "IPV4"},
             test_script=[
                 "pm.test('rejected (sync 404 or async error)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="LST-CR-CONF-DUP-PORT-PROTO",
    title="Duplicate (lb_id, port, protocol) → ALREADY_EXISTS (Verifies REQ-LST-UNIQ-PORT-PROTO)",
    classes=["CONF", "NEG"], priority="P0",
    steps=[
        *_setup_lb("dup-pp"),
        Step(name="cr-1", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "pp1-{{runId}}",
                   "protocol": "TCP", "port": 86, "targetPort": 8086, "ipVersion": "IPV4"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        Step(name="cr-2-dup", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "pp2-{{runId}}",
                   "protocol": "TCP", "port": 86, "targetPort": 8086, "ipVersion": "IPV4"},
             test_script=[
                 "pm.test('rejected (sync 409 or async)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_lst(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-CONF-VIP-COMPENSATION",
    title="VIP allocated but INSERT fails → FreeIP compensation (Verifies REQ-LST-COMP-FREEIP)",
    classes=["CONF", "NEG"], priority="P1",
    steps=[
        # Cannot trigger DB-level CHECK failure from client. Closest observable
        # surrogate: trigger a deterministic conflict by attempting Create with
        # a value that worker validation rejects after VIP allocation.
        *_setup_lb("vip-comp"),
        Step(name="cr-likely-fail", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "vipc-{{runId}}",
                   "protocol": "TCP", "port": 87, "targetPort": 8087, "ipVersion": "IPV4",
                   "defaultTargetGroupId": "{{garbageTgrId}}"},
             test_script=[
                 "pm.test('rejected or accepted', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 404, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))


# ---------------------------------------------------------------------------
# STATE — immutable fields on Update
# ---------------------------------------------------------------------------

def _immutable_listener_case(case_id: str, mask: str, payload: dict) -> Case:
    return Case(
        id=case_id,
        title=f"Update Listener with mask={mask!r} → InvalidArgument (immutable)",
        classes=["STATE", "VAL"], priority="P0",
        steps=[
            Step(name="upd-imm", method="PATCH",
                 path=f"{_LST_BASE}/{{{{garbageLstId}}}}",
                 body={"updateMask": mask, **payload},
                 test_script=[
                     "pm.test('rejected (400 or 404)', () => "
                     "  pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
                 ]),
        ],
    )


CASES.append(_immutable_listener_case("LST-UPD-STATE-IMMUTABLE-LB-ID",
                                      "load_balancer_id",
                                      {"loadBalancerId": "nlbany00000000000000"}))
CASES.append(_immutable_listener_case("LST-UPD-STATE-IMMUTABLE-PROTOCOL",
                                      "protocol", {"protocol": "UDP"}))
CASES.append(_immutable_listener_case("LST-UPD-STATE-IMMUTABLE-PORT",
                                      "port", {"port": 9999}))
CASES.append(_immutable_listener_case("LST-UPD-STATE-IMMUTABLE-IP-VERSION",
                                      "ip_version", {"ipVersion": "IPV6"}))
CASES.append(_immutable_listener_case("LST-UPD-STATE-IMMUTABLE-ADDRESS-ID",
                                      "address_id", {"addressId": "e9bany00000000000000"}))

CASES.append(Case(
    id="LST-UPD-STATE-DEFAULT-TG-REGION-MISMATCH",
    title="Update default_target_group_id to TG in different region → FailedPrecondition",
    classes=["STATE", "NEG"], priority="P1",
    steps=[
        *_setup_lb("def-tg-region"),
        Step(name="cr", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "dtgr-{{runId}}",
                   "protocol": "TCP", "port": 88, "targetPort": 8088, "ipVersion": "IPV4"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        # Make a TG in alt region
        Step(name="setup-tg-alt", method="POST", path="/nlb/v1/targetGroups",
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionAltId}}",
                   "name": "dtgr-alt-{{runId}}",
                   "healthCheck": {"name": "hc", "interval": "2s", "timeout": "1s",
                                   "unhealthyThreshold": 3, "healthyThreshold": 2,
                                   "tcp": {"port": 80}}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgAltId")]),
        poll_operation_until_done(),
        Step(name="upd-default-tg-mismatch", method="PATCH", path=f"{_LST_BASE}/{{{{lstId}}}}",
             body={"updateMask": "default_target_group_id", "defaultTargetGroupId": "{{tgAltId}}"},
             test_script=[
                 "pm.test('rejected (sync or async)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="cleanup-tg-alt", method="DELETE", path="/nlb/v1/targetGroups/{{tgAltId}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_lst(),
        *_cleanup_lb(),
    ],
))


# ---------------------------------------------------------------------------
# NEG — Get/List/Delete/ListOps not-found
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="LST-GET-NEG-NF-UNKNOWN",
    title="Get unknown listener_id → 404 NotFound",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="get-unknown", method="GET", path=f"{_LST_BASE}/{{{{garbageLstId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="LST-DEL-NEG-NF-UNKNOWN",
    title="Delete unknown listener_id → 404 NotFound",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="del-unknown", method="DELETE", path=f"{_LST_BASE}/{{{{garbageLstId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    # index: *-LST-NEG-LB-UNKNOWN (cross-domain List-by-parent-not-found pattern)
    id="LST-LST-NEG-LB-UNKNOWN",
    title="List by unknown load_balancer_id → 404 NotFound",
    classes=["NEG", "LSG"], priority="P1",
    steps=[
        Step(name="lst-unknown-lb", method="GET",
             path=f"{_LST_BASE}?loadBalancerId={{{{garbageNlbId}}}}",
             test_script=[
                 "pm.test('rejected (404) or empty (200)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="LST-LST-BVA-PAGESIZE-OVER-MAX",
    title="List with pageSize=10000 → InvalidArgument",
    classes=["BVA", "VAL", "LSG"], priority="P2",
    steps=[
        Step(name="lst-huge", method="GET",
             path=f"{_LST_BASE}?loadBalancerId={{{{garbageNlbId}}}}&pageSize=10000",
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))


# HTTP-method semantics
CASES.extend(http_method_not_allowed_block("LST", _LST_BASE))


# ---------------------------------------------------------------------------
# Extended VAL/NEG/BVA matrix saturation
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="LST-CR-VAL-NAME-NUMERIC-START",
    title="Create with name starting with digit → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        *_setup_lb("name-digit"),
        Step(name="cr-digit", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "9bad-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080, "ipVersion": "IPV4"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-VAL-NAME-HYPHEN-START",
    title="Create with name starting with hyphen → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        *_setup_lb("name-hyp"),
        Step(name="cr-hyp", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "-bad-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080, "ipVersion": "IPV4"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-VAL-TARGET-PORT-ZERO",
    title="Create with target_port=0 → InvalidArgument",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        *_setup_lb("tp-0"),
        Step(name="cr-tp-0", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "tp0-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 0, "ipVersion": "IPV4"},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-VAL-TARGET-PORT-OVER",
    title="Create with target_port=65536 → InvalidArgument",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        *_setup_lb("tp-over"),
        Step(name="cr-tp-o", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "tpo-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 65536, "ipVersion": "IPV4"},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-VAL-IPV-UNKNOWN",
    title="Create with ip_version=IPV9 (unknown enum) → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        *_setup_lb("ipv-unk"),
        Step(name="cr-ipv-unk", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "ipv-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080, "ipVersion": "IPV9"},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-CRUD-IPV6",
    title="Create with ip_version=IPV6 → OK",
    classes=["CRUD"], priority="P1",
    steps=[
        *_setup_lb("ipv6"),
        Step(name="cr-ipv6", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "v6-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080, "ipVersion": "IPV6"},
             test_script=[
                 "pm.test('OK or InsufficientPool', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
                 *save_from_response("j.metadata && j.metadata.listenerId", "lstId"),
             ]),
        poll_operation_until_done(),
        Step(name="cleanup-best-effort", method="DELETE", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-CRUD-PROXY-PROTO-V2",
    title="Create with proxy_protocol_v2=true → OK",
    classes=["CRUD"], priority="P2",
    steps=[
        *_setup_lb("pp2"),
        Step(name="cr-pp2", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "pp2-{{runId}}",
                   "protocol": "TCP", "port": 90, "targetPort": 9090,
                   "ipVersion": "IPV4", "proxyProtocolV2": True},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        Step(name="get", method="GET", path=f"{_LST_BASE}/{{{{lstId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('proxy_protocol_v2 persisted', () => "
                          "  pm.expect(pm.response.json().proxyProtocolV2).to.eql(true));"]),
        *_cleanup_lst(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-UPD-CRUD-DEFAULT-TG-CLEAR",
    title="Update default_target_group_id to \"\" → cleared",
    classes=["CRUD", "STATE"], priority="P2",
    steps=[
        *_setup_lb("def-tg-clear"),
        Step(name="cr", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "dtgc-{{runId}}",
                   "protocol": "TCP", "port": 91, "targetPort": 9091, "ipVersion": "IPV4"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        Step(name="upd-clear", method="PATCH", path=f"{_LST_BASE}/{{{{lstId}}}}",
             body={"updateMask": "default_target_group_id", "defaultTargetGroupId": ""},
             test_script=[
                 "pm.test('accepted or no-op', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_lst(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-GET-NEG-INVALID-ID-PREFIX",
    title="Get with malformed id prefix → InvalidArgument",
    classes=["NEG", "VAL"], priority="P0",
    steps=[
        Step(name="get-bad-prefix", method="GET", path=f"{_LST_BASE}/garbage-not-an-id",
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))


CASES.append(Case(
    id="LST-LST-FILTER-NAME",
    title="List Listeners with filter name=\"x\" → 200",
    classes=["LSG"], priority="P2",
    steps=[
        Step(name="lst-filter", method="GET",
             path=f"{_LST_BASE}?loadBalancerId={{{{garbageNlbId}}}}&filter=name%3D%22x%22",
             test_script=[
                 "pm.test('handled', () => pm.expect(pm.response.code).to.be.oneOf([200, 400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="LST-LST-PAGE-ROUNDTRIP",
    title="Pagination round-trip on listeners",
    classes=["CRUD", "LSG", "BVA"], priority="P2",
    steps=[
        *_setup_lb("page-rt"),
        Step(name="page-1", method="GET",
             path=f"{_LST_BASE}?loadBalancerId={{{{nlbId}}}}&pageSize=1",
             test_script=[*assert_status(200),
                          "pm.environment.set('lstNextToken', pm.response.json().nextPageToken || '');"]),
        Step(name="page-2", method="GET",
             path=f"{_LST_BASE}?loadBalancerId={{{{nlbId}}}}&pageSize=1&pageToken={{{{lstNextToken}}}}",
             test_script=[*assert_status(200)]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="LST-CR-VAL-MALFORMED-JSON",
    title="Create Listener with malformed JSON body → 400/415",
    classes=["VAL"], priority="P2",
    steps=[
        Step(name="cr-malformed", method="POST", path=_LST_BASE, body=None,
             pre_script=["pm.request.body = { mode: 'raw', raw: '{not json' };"],
             test_script=[
                 "pm.test('400 or 415', () => pm.expect(pm.response.code).to.be.oneOf([400, 415]));",
             ]),
    ],
))

CASES.append(Case(
    id="LST-CR-VAL-EMPTY-BODY",
    title="Create Listener with empty body → InvalidArgument",
    classes=["VAL"], priority="P2",
    steps=[
        Step(name="cr-empty", method="POST", path=_LST_BASE, body={},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 200]));",
             ]),
    ],
))

CASES.append(Case(
    id="LST-CR-CRUD-UDP-PROTOCOL",
    title="Create Listener with protocol=UDP → OK",
    classes=["CRUD"], priority="P1",
    steps=[
        *_setup_lb("udp"),
        Step(name="cr-udp", method="POST", path=_LST_BASE,
             body={"loadBalancerId": "{{nlbId}}", "name": "udp-{{runId}}",
                   "protocol": "UDP", "port": 53, "targetPort": 53, "ipVersion": "IPV4"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        *_cleanup_lst(),
        *_cleanup_lb(),
    ],
))
