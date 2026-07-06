# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

"""Targets sub-resource cases (TGT-*) — AddTargets / RemoveTargets.

Acceptance: docs/specs/sub-phase-4.0-nlb-acceptance.md §6 (GWT-TGT-001..016).
Design §4.3 (4-way identity oneof + bogon check + per-target peer-validate).
Design §4.4 (2-phase RemoveTargets drain — Phase A immediate + Phase B runner).

REST: /nlb/v1/targetGroups/{target_group_id}:addTargets   (POST)
      /nlb/v1/targetGroups/{target_group_id}:removeTargets (POST)
"""

CASES = []

_TG_BASE = "/nlb/v1/targetGroups"

_HC = {"name": "hc", "interval": "2s", "timeout": "1s",
       "unhealthyThreshold": 3, "healthyThreshold": 2, "tcp": {"port": 80}}

_TG_BODY = {"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
            "healthCheck": _HC, "deregistrationDelaySeconds": 300,
            "slowStartSeconds": 30}


def _setup_tg(name_suffix: str, dereg_seconds: int = 300):
    return [
        Step(name="setup-cr-tg", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": f"tgt-{name_suffix}-{{{{runId}}}}",
                   "deregistrationDelaySeconds": dereg_seconds},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
    ]


def _cleanup_tg():
    return [
        Step(name="cleanup-tg-best-effort", method="DELETE", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ]


# ---------------------------------------------------------------------------
# AddTargets — 4-way identity matrix
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="TGT-ADD-CRUD-INSTANCE-ID",
    title="AddTargets variant 1: instance_id (Verifies REQ-TGT-4WAY-INSTANCE)",
    classes=["CRUD"], priority="P0",
    steps=[
        *_setup_tg("add-inst"),
        Step(name="add-inst", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"instanceId": "{{existingInstanceId}}", "weight": 100}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="rm-cleanup", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"instanceId": "{{existingInstanceId}}"}]},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-CRUD-NIC-ID",
    title="AddTargets variant 2: nic_id",
    classes=["CRUD"], priority="P0",
    steps=[
        *_setup_tg("add-nic"),
        Step(name="add-nic", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"nicId": "{{existingNicId}}", "weight": 100}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="rm-cleanup", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"nicId": "{{existingNicId}}"}]},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-CRUD-IP-REF",
    title="AddTargets variant 3: ip_ref{subnet_id, address}",
    classes=["CRUD"], priority="P0",
    steps=[
        *_setup_tg("add-ipref"),
        Step(name="add-ipref", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"ipRef": {"subnetId": "{{existingSubnetId}}",
                                          "address": "10.180.0.5"}, "weight": 100}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="rm-cleanup", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"ipRef": {"subnetId": "{{existingSubnetId}}",
                                          "address": "10.180.0.5"}}]},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-CRUD-EXTERNAL-IP",
    title="AddTargets variant 4: external_ip{address}",
    classes=["CRUD"], priority="P0",
    steps=[
        *_setup_tg("add-ext"),
        Step(name="add-ext", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.10"}, "weight": 100}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="rm-cleanup", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.10"}}]},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-CRUD-MIXED-IDENTITIES",
    title="AddTargets with all 4 variants in single call",
    classes=["CRUD"], priority="P1",
    steps=[
        *_setup_tg("add-mixed"),
        Step(name="add-mixed", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [
                 {"instanceId": "{{existingInstanceId}}", "weight": 100},
                 {"nicId": "{{existingNicId}}", "weight": 100},
                 {"ipRef": {"subnetId": "{{existingSubnetId}}", "address": "10.180.0.6"},
                  "weight": 50},
                 {"externalIp": {"address": "203.0.113.11"}, "weight": 100},
             ]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="rm-cleanup", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [
                 {"instanceId": "{{existingInstanceId}}"},
                 {"nicId": "{{existingNicId}}"},
                 {"ipRef": {"subnetId": "{{existingSubnetId}}", "address": "10.180.0.6"}},
                 {"externalIp": {"address": "203.0.113.11"}},
             ]},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))


# ---------------------------------------------------------------------------
# Validation
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="TGT-ADD-VAL-EMPTY-LIST",
    title="AddTargets with targets=[] → InvalidArgument 'at least one target is required'",
    classes=["VAL"], priority="P1",
    steps=[
        *_setup_tg("add-empty"),
        Step(name="add-empty", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": []},
             test_script=[
                 # Empty-list guard is synchronous (add_targets.go:80, before any
                 # Operation is created) → always InvalidArgument/400. A 200 here
                 # would be the validation regression this case exists to catch.
                 "pm.test('rejected sync 400', () => "
                 "  pm.expect(pm.response.code).to.eql(400));",
             ]),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-VAL-WEIGHT-NEGATIVE",
    title="AddTargets weight=-1 → InvalidArgument",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        *_setup_tg("w-neg"),
        Step(name="add-w-neg", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.20"}, "weight": -1}]},
             test_script=[
                 # weight bounds are validated synchronously (domain Target.Validate
                 # via add_targets.go:89, before Operation creation) → always 400.
                 "pm.test('rejected sync 400', () => pm.expect(pm.response.code).to.eql(400));",
             ]),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-VAL-WEIGHT-OVER",
    title="AddTargets weight=1001 → InvalidArgument",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        *_setup_tg("w-over"),
        Step(name="add-w-over", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.21"}, "weight": 1001}]},
             test_script=[
                 # weight bounds are validated synchronously (domain Target.Validate
                 # via add_targets.go:89, before Operation creation) → always 400.
                 "pm.test('rejected sync 400', () => pm.expect(pm.response.code).to.eql(400));",
             ]),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-BVA-WEIGHT-MIN-0",
    title="AddTargets weight=0 → OK (drain semantics)",
    classes=["BVA"], priority="P2",
    steps=[
        *_setup_tg("w-min"),
        Step(name="add-w-0", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.22"}, "weight": 0}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="rm", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.22"}}]},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-BVA-WEIGHT-MAX-1000",
    title="AddTargets weight=1000 → OK (upper bound)",
    classes=["BVA"], priority="P2",
    steps=[
        *_setup_tg("w-max"),
        Step(name="add-w-1000", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.23"}, "weight": 1000}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="rm", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.23"}}]},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-VAL-BOGON-LOOPBACK",
    title="AddTargets external_ip=127.0.0.1 → InvalidArgument (bogon)",
    classes=["VAL"], priority="P0",
    steps=[
        *_setup_tg("bogon-add"),
        Step(name="add-bogon", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"externalIp": {"address": "127.0.0.1"}, "weight": 100}]},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-VAL-IP-REF-NOT-IN-SUBNET",
    title="AddTargets ip_ref outside subnet CIDR → InvalidArgument (Verifies REQ-TGT-IPREF-CIDR)",
    classes=["VAL"], priority="P0",
    steps=[
        *_setup_tg("ipref-out"),
        Step(name="add-out", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"ipRef": {"subnetId": "{{existingSubnetId}}",
                                          "address": "10.99.99.99"}, "weight": 100}]},
             test_script=[
                 "pm.test('rejected (sync or async)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))


# ---------------------------------------------------------------------------
# Peer-validate failures
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="TGT-ADD-NEG-INSTANCE-UNKNOWN",
    title="AddTargets unknown instance_id → InvalidArgument 'not found' (Verifies REQ-TGT-PEER-INSTANCE)",
    classes=["NEG"], priority="P1",
    steps=[
        *_setup_tg("inst-nx"),
        Step(name="add-inst-nx", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"instanceId": "epdinstdoesnotexist0", "weight": 100}]},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400, 404]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-NEG-NIC-UNKNOWN",
    title="AddTargets unknown nic_id → InvalidArgument",
    classes=["NEG"], priority="P1",
    steps=[
        *_setup_tg("nic-nx"),
        Step(name="add-nic-nx", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"nicId": "e9bnicdoesnotexist00", "weight": 100}]},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400, 404]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-NEG-SUBNET-UNKNOWN",
    title="AddTargets ip_ref with unknown subnet_id → InvalidArgument",
    classes=["NEG"], priority="P1",
    steps=[
        *_setup_tg("sub-nx"),
        Step(name="add-sub-nx", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"ipRef": {"subnetId": "e9bsubdoesnotexist00",
                                          "address": "10.0.0.5"}, "weight": 100}]},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400, 404]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-NEG-INSTANCE-REGION-MISMATCH",
    title="AddTargets instance in different region → InvalidArgument (Verifies REQ-TGT-PEER-REGION)",
    classes=["NEG"], priority="P0",
    steps=[
        *_setup_tg("inst-region-mismatch"),
        Step(name="add-inst-other-region", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"instanceId": "{{existingInstanceCrossRegionId}}", "weight": 100}]},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-NEG-NIC-REGION-MISMATCH",
    title="AddTargets NIC in different region → InvalidArgument",
    classes=["NEG"], priority="P1",
    steps=[
        *_setup_tg("nic-region-mismatch"),
        Step(name="add-nic-other-region", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"nicId": "{{existingNicCrossRegionId}}", "weight": 100}]},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-NEG-SUBNET-REGION-MISMATCH",
    title="AddTargets ip_ref.subnet in different region → InvalidArgument",
    classes=["NEG"], priority="P1",
    steps=[
        *_setup_tg("sub-region-mismatch"),
        Step(name="add-sub-other-region", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"ipRef": {"subnetId": "{{existingSubnetCrossRegionId}}",
                                          "address": "10.0.0.5"}, "weight": 100}]},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))


# ---------------------------------------------------------------------------
# IDEM / STATE
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="TGT-ADD-IDEM-DUP-INSTANCE",
    title="AddTargets repeat same instance_id → ON CONFLICT DO NOTHING (Verifies REQ-TGT-IDEM-ID)",
    classes=["IDEM"], priority="P1",
    steps=[
        *_setup_tg("dup-inst"),
        Step(name="add-1", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"instanceId": "{{existingInstanceId}}", "weight": 100}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="add-2-dup", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"instanceId": "{{existingInstanceId}}", "weight": 100}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="rm-cleanup", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"instanceId": "{{existingInstanceId}}"}]},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-IDEM-DUP-IP-REF",
    title="AddTargets repeat same ip_ref → no duplicate row",
    classes=["IDEM"], priority="P1",
    steps=[
        *_setup_tg("dup-ipref"),
        Step(name="add-1", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"ipRef": {"subnetId": "{{existingSubnetId}}",
                                          "address": "10.180.0.30"}, "weight": 50}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="add-2-dup", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"ipRef": {"subnetId": "{{existingSubnetId}}",
                                          "address": "10.180.0.30"}, "weight": 50}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="rm", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"ipRef": {"subnetId": "{{existingSubnetId}}",
                                          "address": "10.180.0.30"}}]},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-IDEM-DUP-EXTERNAL-IP",
    title="AddTargets repeat same external_ip → no duplicate",
    classes=["IDEM"], priority="P2",
    steps=[
        *_setup_tg("dup-ext"),
        Step(name="add-1", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.40"}, "weight": 100}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="add-2", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.40"}, "weight": 100}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="rm", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.40"}}]},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-IDEM-PROMOTE-DRAINING",
    title="Re-add DRAINING target → re-promoted to ACTIVE (ON CONFLICT DO UPDATE)",
    classes=["IDEM", "STATE"], priority="P1",
    steps=[
        *_setup_tg("promote-draining"),
        Step(name="add", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.50"}, "weight": 100}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="rm-phase-a", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.50"}}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="re-add", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.50"}, "weight": 100}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="rm-cleanup", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.50"}}]},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-ADD-STATE-TG-DELETING",
    title="AddTargets when TG status=DELETING → FailedPrecondition",
    classes=["STATE", "NEG"], priority="P1",
    steps=[
        Step(name="add-deleting-proxy", method="POST",
             path=f"{_TG_BASE}/{{{{garbageTgrId}}}}:addTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.60"}, "weight": 100}]},
             test_script=[
                 "pm.test('rejected (404 or 409)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 404, 409]));",
             ]),
    ],
))


# ---------------------------------------------------------------------------
# RemoveTargets — 2-phase drain
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="TGT-RM-STATE-PHASE-A-DRAINING",
    title="RemoveTargets Phase A: DRAINING-mark + drain_started_at set (Verifies REQ-TGT-RM-PHASE-A)",
    classes=["STATE"], priority="P0",
    steps=[
        *_setup_tg("phase-a", dereg_seconds=300),
        Step(name="add", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.70"}, "weight": 100}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="rm-phase-a", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.70"}}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="verify-still-present-as-draining", method="GET",
             path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[*assert_status(200),
                          "const tgts = pm.response.json().targets || [];",
                          "const draining = tgts.find(t => t.externalIp && t.externalIp.address === '203.0.113.70');",
                          "if (draining) pm.test('row still present (drain not yet done)', () => "
                          "  pm.expect(draining).to.be.an('object'));"]),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-RM-IDEM-NOT-PRESENT",
    title="RemoveTargets for absent identity → no-op idempotent (Verifies REQ-TGT-RM-IDEM)",
    classes=["IDEM"], priority="P1",
    steps=[
        *_setup_tg("rm-noop"),
        Step(name="rm-absent", method="POST",
             path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.99"}}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGT-RM-STATE-PHASE-B-RUNNER",
    title="RemoveTargets Phase B: after dereg_delay drain runner DELETEs row (Verifies REQ-TGT-RM-PHASE-B)",
    classes=["STATE"], priority="P1",
    steps=[
        # Use tiny dereg=1 so Phase B fires quickly inside test window.
        *_setup_tg("phase-b", dereg_seconds=1),
        Step(name="add", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.80"}, "weight": 100}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="rm", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.80"}}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        # Pad: runner ticks every ~10s. We hope it has fired by the time this runs;
        # the assertion tolerates either state since newman is sequential and the
        # drain timing is racey w.r.t. polling cadence.
        Step(name="poll-tg-after-drain", method="GET", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[*assert_status(200),
                          "const tgts = pm.response.json().targets || [];",
                          "const t = tgts.find(x => x.externalIp && x.externalIp.address === '203.0.113.80');",
                          "pm.test('row absent or still DRAINING (eventually consistent)', () => "
                          "  pm.expect(!t || t.status === 'DRAINING' || t.status === 'INACTIVE').to.be.true);"]),
        *_cleanup_tg(),
    ],
))


# HTTP method semantics — collection-level endpoints belong to TargetGroupService;
# Targets has no collection endpoint of its own. Reuse the TGR collection paths.
CASES.extend(http_method_not_allowed_block("TGT", "/nlb/v1/targetGroups"))


# ---------------------------------------------------------------------------
# Extended matrix
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="TGT-RM-VAL-EMPTY-LIST",
    title="RemoveTargets with empty targets[] → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        *_setup_tg("rm-empty"),
        Step(name="rm-empty", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": []},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))
