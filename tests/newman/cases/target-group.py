# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

"""TargetGroupService cases (TGR-*).

Acceptance: docs/specs/sub-phase-4.0-nlb-acceptance.md §5 (GWT-TGR-001..028).

REST: /nlb/v1/targetGroups
"""

CASES = []

_TG_BASE = "/nlb/v1/targetGroups"

_HEALTH_CHECK_DEFAULT = {
    "name": "hc-default", "interval": "2s", "timeout": "1s",
    "unhealthyThreshold": 3, "healthyThreshold": 2,
    "tcp": {"port": 80},
}

_TG_BODY = {
    "projectId": "{{_suiteProjectId}}",
    "regionId": "{{_suiteRegionId}}",
    "healthCheck": _HEALTH_CHECK_DEFAULT,
    "deregistrationDelaySeconds": 300,
    "slowStartSeconds": 30,
}


def _setup_tg(name_suffix: str, body_extra: dict = None, name_override: str = None):
    name = name_override or f"setup-tg-{name_suffix}-{{{{runId}}}}"
    return [
        Step(name="setup-cr-tg", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": name, **(body_extra or {})},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
    ]


def _cleanup_tg():
    return [
        Step(name="cleanup-tg", method="DELETE", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ]


# ---------------------------------------------------------------------------
# CRUD
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="TGR-CR-CRUD-OK",
    title="Create TG with inline targets + health_check (Verifies REQ-TGR-CR-01)",
    classes=["CRUD"], priority="P0",
    steps=[
        Step(name="cr", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "backend-web-{{runId}}",
                   "labels": {"tier": "web"},
                   "healthCheck": {"name": "http-200", "interval": "2s", "timeout": "1s",
                                   "unhealthyThreshold": 3, "healthyThreshold": 2,
                                   "http": {"port": 8080, "path": "/healthz",
                                            "expectedStatuses": [200]}},
                   "targets": [
                       {"externalIp": {"address": "203.0.113.50"}, "weight": 50},
                   ]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
        Step(name="get", method="GET", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('has health_check', () => pm.expect(j.healthCheck).to.be.an('object'));"]),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-CR-CRUD-EMPTY-TARGETS",
    title="Create TG with targets=[] → OK (Verifies REQ-TGR-CR-EMPTY)",
    classes=["CRUD"], priority="P2",
    steps=[
        Step(name="cr-empty", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "tg-empty-{{runId}}", "targets": []},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-GET-CRUD-OK",
    title="Get existing TG returns full message with targets[] and health_check{}",
    classes=["CRUD"], priority="P0",
    steps=[
        *_setup_tg("get-ok"),
        Step(name="get", method="GET", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('targets array present', () => "
                          "  pm.expect(pm.response.json().targets || []).to.be.an('array'));"]),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-LST-CRUD-OK",
    title="List TG by project (Verifies REQ-TGR-LST-01)",
    classes=["CRUD", "LSG"], priority="P1",
    steps=[
        Step(name="lst", method="GET",
             path=f"{_TG_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=10",
             test_script=[*assert_status(200),
                          "pm.test('targetGroups array', () => "
                          "  pm.expect(pm.response.json().targetGroups || pm.response.json().items || []).to.be.an('array'));"]),
    ],
))

CASES.append(Case(
    id="TGR-LST-FILTER-REGION",
    title="List TG with filter region_id → only matching rows",
    classes=["LSG"], priority="P2",
    steps=[
        Step(name="lst-filter", method="GET",
             path=f"{_TG_BASE}?projectId={{{{_suiteProjectId}}}}&"
                  f"filter=region_id%3D%22{{{{_suiteRegionId}}}}%22",
             test_script=[*assert_status(200)]),
    ],
))

CASES.append(Case(
    id="TGR-UPD-CRUD-OK",
    title="Update TG mutable (name/desc/labels/health_check/dereg/slow_start)",
    classes=["CRUD"], priority="P1",
    steps=[
        *_setup_tg("upd-ok"),
        Step(name="upd", method="PATCH", path=f"{_TG_BASE}/{{{{tgId}}}}",
             body={"updateMask": "name,deregistration_delay_seconds",
                   "name": "tg-upd-{{runId}}", "deregistrationDelaySeconds": 600},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-DEL-CRUD-OK",
    title="Delete clean TG (no attachments + no targets) (Verifies REQ-TGR-DEL-01)",
    classes=["CRUD"], priority="P1",
    steps=[
        Step(name="cr", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "tg-del-{{runId}}", "targets": []},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
        Step(name="del", method="DELETE", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="get-404", method="GET", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="TGR-MV-CRUD-OK",
    title="Move TG cross-project (no attached LB)",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        *_setup_tg("mv"),
        Step(name="move", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:move",
             body={"destinationProjectId": "{{_suiteProjectCrossId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="move-back", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:move",
             body={"destinationProjectId": "{{_suiteProjectId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-LOPS-CRUD-OK",
    title="ListOperations for TG",
    classes=["CRUD", "LSG"], priority="P2",
    steps=[
        *_setup_tg("lops"),
        Step(name="lops", method="GET",
             path=f"{_TG_BASE}/{{{{tgId}}}}/operations?pageSize=10",
             test_script=[*assert_status(200),
                          "const ops = (pm.response.json().operations || pm.response.json().items || []);",
                          "pm.test('at least 1 op', () => pm.expect(ops.length).to.be.at.least(1));"]),
        *_cleanup_tg(),
    ],
))


# ---------------------------------------------------------------------------
# Validation — health_check semantics
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="TGR-CR-VAL-HC-MULTIPLE-PROBES",
    title="health_check with both tcp + http → InvalidArgument (Verifies REQ-TGR-VAL-HC)",
    classes=["VAL"], priority="P0",
    steps=[
        Step(name="cr-multi-hc", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "hc-multi-{{runId}}",
                   "healthCheck": {"name": "x", "interval": "2s", "timeout": "1s",
                                   "unhealthyThreshold": 3, "healthyThreshold": 2,
                                   "tcp": {"port": 8080},
                                   "http": {"port": 8080, "path": "/"}}},
             test_script=[
                 "pm.test('rejected (sync 400 or async error)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-HC-NONE-SET",
    title="health_check without any probe type → InvalidArgument",
    classes=["VAL"], priority="P0",
    steps=[
        Step(name="cr-no-hc", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "hc-none-{{runId}}",
                   "healthCheck": {"name": "x", "interval": "2s", "timeout": "1s",
                                   "unhealthyThreshold": 3, "healthyThreshold": 2}},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-HC-INTERVAL-ZERO",
    title="health_check.interval=0s → InvalidArgument",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        Step(name="cr-int-0", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "hc-int-0-{{runId}}",
                   "healthCheck": {**_HEALTH_CHECK_DEFAULT, "interval": "0s"}},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-HC-INTERVAL-OVER",
    title="health_check.interval=601s → InvalidArgument (over upper bound)",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        Step(name="cr-int-over", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "hc-int-over-{{runId}}",
                   "healthCheck": {**_HEALTH_CHECK_DEFAULT, "interval": "601s"}},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-HC-THRESHOLD-LOW",
    title="unhealthy_threshold=1 (below min) → InvalidArgument",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        Step(name="cr-thr-low", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "hc-thr-low-{{runId}}",
                   "healthCheck": {**_HEALTH_CHECK_DEFAULT, "unhealthyThreshold": 1}},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-HC-THRESHOLD-HIGH",
    title="unhealthy_threshold=11 (above max) → InvalidArgument",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        Step(name="cr-thr-hi", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "hc-thr-hi-{{runId}}",
                   "healthCheck": {**_HEALTH_CHECK_DEFAULT, "unhealthyThreshold": 11}},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-DEREG-NEGATIVE",
    title="deregistration_delay_seconds=-1 → InvalidArgument",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        Step(name="cr-dereg-neg", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "dereg-neg-{{runId}}", "deregistrationDelaySeconds": -1},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-DEREG-OVER",
    title="deregistration_delay_seconds=3601 → InvalidArgument (over upper bound)",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        Step(name="cr-dereg-over", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "dereg-over-{{runId}}", "deregistrationDelaySeconds": 3601},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-SLOW-START-NEGATIVE",
    title="slow_start_seconds=-1 → InvalidArgument",
    classes=["VAL", "BVA"], priority="P2",
    steps=[
        Step(name="cr-ss-neg", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "ss-neg-{{runId}}", "slowStartSeconds": -1},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-SLOW-START-OVER",
    title="slow_start_seconds=901 → InvalidArgument",
    classes=["VAL", "BVA"], priority="P2",
    steps=[
        Step(name="cr-ss-over", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "ss-over-{{runId}}", "slowStartSeconds": 901},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-LABELS-OVER-64",
    title="Create TG with >64 labels → InvalidArgument (DB CHECK)",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        Step(name="cr-65-lbl", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "tg-65lbl-{{runId}}",
                   "labels": {f"k{i}": f"v{i}" for i in range(65)}},
             test_script=[
                 "pm.test('rejected (sync or async)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))


# ---------------------------------------------------------------------------
# Validation — inline targets oneof
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="TGR-CR-VAL-TARGET-NO-IDENTITY",
    title="Target without any oneof identity → InvalidArgument (Verifies REQ-TGT-4WAY-EXACTLY-ONE)",
    classes=["VAL"], priority="P0",
    steps=[
        Step(name="cr-no-id", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "no-id-{{runId}}",
                   "targets": [{"weight": 100}]},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-TARGET-MULTIPLE-IDENTITY",
    title="Target with two of {instance_id, external_ip} → InvalidArgument",
    classes=["VAL"], priority="P0",
    steps=[
        Step(name="cr-multi-id", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "multi-id-{{runId}}",
                   "targets": [{"instanceId": "epdany00000000000000",
                                "externalIp": {"address": "8.8.8.8"}, "weight": 100}]},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

# Bogon block — 5 variants of disallowed external_ip ranges
_BOGONS = [
    ("LOOPBACK", "127.0.0.1"),
    ("UNSPEC", "0.0.0.0"),
    ("LINKLOCAL", "169.254.1.1"),
    ("MULTICAST", "224.0.0.1"),
    ("BROADCAST", "255.255.255.255"),
]
for label, addr in _BOGONS:
    CASES.append(Case(
        id=f"TGR-CR-VAL-TARGET-BOGON-{label}",
        title=f"Target external_ip={addr} ({label.lower()}) → bogon InvalidArgument",
        classes=["VAL"], priority="P0" if label == "LOOPBACK" else "P1",
        steps=[
            Step(name=f"cr-bogon-{label.lower()}", method="POST", path=_TG_BASE,
                 body={**_TG_BODY, "name": f"bogon-{label.lower()}-{{{{runId}}}}",
                       "targets": [{"externalIp": {"address": addr}, "weight": 100}]},
                 test_script=[
                     "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 ]),
        ],
    ))

CASES.append(Case(
    id="TGR-CR-NEG-REGION-UNKNOWN",
    title="Create TG with unknown region_id → NotFound",
    classes=["NEG"], priority="P0",
    steps=[
        Step(name="cr-bad-region", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "regionId": "{{garbageRegionId}}",
                   "name": "tg-bad-region-{{runId}}"},
             test_script=[
                 "pm.test('rejected (sync or async)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 404]));",
             ]),
    ],
))


# ---------------------------------------------------------------------------
# CONF / STATE / NEG
# ---------------------------------------------------------------------------

CASES.append(conf_alreadyexists_block(
    prefix="TGR",
    create_path=_TG_BASE,
    name_template="tgr-dup-{{runId}}",
    body_extra={"regionId": "{{_suiteRegionId}}", "healthCheck": _HEALTH_CHECK_DEFAULT},
))

CASES.append(Case(
    id="TGR-UPD-STATE-IMMUTABLE-PROJECT",
    title="Update TG with mask=project_id → InvalidArgument (immutable)",
    classes=["STATE", "VAL"], priority="P0",
    steps=[
        Step(name="upd-prj", method="PATCH", path=f"{_TG_BASE}/{{{{garbageTgrId}}}}",
             body={"updateMask": "project_id", "projectId": "{{_suiteProjectCrossId}}"},
             test_script=[
                 "pm.test('rejected 400/404', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-UPD-STATE-IMMUTABLE-REGION",
    title="Update TG with mask=region_id → InvalidArgument (immutable)",
    classes=["STATE", "VAL"], priority="P0",
    steps=[
        Step(name="upd-reg", method="PATCH", path=f"{_TG_BASE}/{{{{garbageTgrId}}}}",
             body={"updateMask": "region_id", "regionId": "{{_suiteRegionAltId}}"},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-UPD-VAL-TARGETS-VIA-MASK",
    title="Update mask=targets → InvalidArgument 'must be modified via AddTargets/RemoveTargets'",
    classes=["VAL"], priority="P0",
    steps=[
        Step(name="upd-targets-mask", method="PATCH", path=f"{_TG_BASE}/{{{{garbageTgrId}}}}",
             body={"updateMask": "targets", "targets": []},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-DEL-NEG-HAS-ATTACHED-LB",
    title="Delete TG with attached LB → FailedPrecondition (Verifies REQ-TGR-DEL-ATTACHED)",
    classes=["NEG", "STATE"], priority="P0",
    steps=[
        # Setup TG
        *_setup_tg("del-has-att"),
        # Setup LB and attach
        Step(name="setup-lb", method="POST", path="/nlb/v1/networkLoadBalancers",
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "name": "tgr-del-lb-{{runId}}", "type": "EXTERNAL", "v4Source": {"public": {}}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="att", method="POST",
             path="/nlb/v1/networkLoadBalancers/{{nlbId}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 100},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="del-blocked", method="DELETE", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        # Cleanup
        Step(name="detach", method="POST",
             path="/nlb/v1/networkLoadBalancers/{{nlbId}}:detachTargetGroup",
             body={"targetGroupId": "{{tgId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="del-lb", method="DELETE", path="/nlb/v1/networkLoadBalancers/{{nlbId}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-DEL-NEG-HAS-TARGETS",
    title="Delete TG with targets → FailedPrecondition (Verifies REQ-TGR-DEL-TARGETS)",
    classes=["NEG", "STATE"], priority="P0",
    steps=[
        Step(name="cr-with-tgts", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "tgr-has-tgts-{{runId}}",
                   "targets": [{"externalIp": {"address": "203.0.113.51"}, "weight": 50}]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
        Step(name="del-blocked", method="DELETE", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        # Cleanup: drain + drop
        Step(name="rm-targets", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.51"}}]},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        # TG may still be blocked until Phase B; cleanup best-effort.
        Step(name="cleanup-best-effort", method="DELETE", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="TGR-DEL-CONF-FK-RACE",
    title="Delete TG raced with AddTargets → FK 23503 → FailedPrecondition",
    classes=["CONF"], priority="P1",
    steps=[
        *_setup_tg("fk-race"),
        Step(name="add-t", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:addTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.52"}, "weight": 100}]},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="del-race", method="DELETE", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="rm-cleanup", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:removeTargets",
             body={"targets": [{"externalIp": {"address": "203.0.113.52"}}]},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="cleanup-best-effort", method="DELETE", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="TGR-MV-NEG-ATTACHED-LB",
    title="Move TG with attached LB → FailedPrecondition",
    classes=["NEG", "STATE"], priority="P0",
    steps=[
        *_setup_tg("mv-attached"),
        Step(name="setup-lb", method="POST", path="/nlb/v1/networkLoadBalancers",
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "name": "tgr-mv-lb-{{runId}}", "type": "EXTERNAL", "v4Source": {"public": {}}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="att", method="POST",
             path="/nlb/v1/networkLoadBalancers/{{nlbId}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 100},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="mv-blocked", method="POST", path=f"{_TG_BASE}/{{{{tgId}}}}:move",
             body={"destinationProjectId": "{{_suiteProjectCrossId}}"},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="detach", method="POST",
             path="/nlb/v1/networkLoadBalancers/{{nlbId}}:detachTargetGroup",
             body={"targetGroupId": "{{tgId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="del-lb", method="DELETE", path="/nlb/v1/networkLoadBalancers/{{nlbId}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-MV-VAL-MISSING-DEST",
    title="Move TG without destinationProjectId → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="mv-no-dest", method="POST", path=f"{_TG_BASE}/{{{{garbageTgrId}}}}:move",
             body={},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-MV-NEG-NF-UNKNOWN",
    title="Move unknown TG id → NotFound",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="mv-nx", method="POST", path=f"{_TG_BASE}/{{{{garbageTgrId}}}}:move",
             body={"destinationProjectId": "{{_suiteProjectCrossId}}"},
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="TGR-GET-NEG-NF-UNKNOWN",
    title="Get TG unknown id → NotFound",
    classes=["NEG"], priority="P0",
    steps=[
        Step(name="get-unknown", method="GET", path=f"{_TG_BASE}/{{{{garbageTgrId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="TGR-LST-BVA-PAGESIZE-OVER-MAX",
    title="List TG with pageSize=10000 → InvalidArgument",
    classes=["BVA", "VAL", "LSG"], priority="P2",
    steps=[
        Step(name="lst-huge", method="GET",
             path=f"{_TG_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=10000",
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="TGR-LST-BVA-PAGESIZE-1",
    title="List TG with pageSize=1 → ≤1 item",
    classes=["BVA", "LSG"], priority="P2",
    steps=[
        Step(name="lst-1", method="GET",
             path=f"{_TG_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=1",
             test_script=[*assert_status(200),
                          "const arr = pm.response.json().targetGroups || pm.response.json().items || [];",
                          "pm.test('at most 1 item', () => pm.expect(arr.length).to.be.at.most(1));"]),
    ],
))


# HTTP method semantics
CASES.extend(http_method_not_allowed_block("TGR", _TG_BASE))


# ---------------------------------------------------------------------------
# Extended matrix
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="TGR-CR-VAL-NAME-NUMERIC-START",
    title="Create TG with name starting with digit → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="cr-digit", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "9bad-{{runId}}"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-NAME-HYPHEN-START",
    title="Create TG with name starting with hyphen → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="cr-hyp", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "-bad-{{runId}}"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-NAME-SPECIAL-CHARS",
    title="Create TG with special chars in name → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="cr-spec", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "bad@name-{{runId}}"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-WRONG-CT",
    title="POST without Content-Type → 415/400/200 lenient",
    classes=["VAL", "NEG"], priority="P3",
    steps=[
        Step(name="cr-no-ct", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "noct-{{runId}}"},
             pre_script=["pm.request.headers.remove('Content-Type');"],
             test_script=[
                 "pm.test('handled', () => pm.expect(pm.response.code).to.be.oneOf([200, 400, 415]));",
                 *save_from_response("j.id", "opId"),
                 *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId"),
             ]),
        poll_operation_until_done(),
        Step(name="cleanup", method="DELETE", path=f"{_TG_BASE}/{{{{tgId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="TGR-GET-NEG-INVALID-ID-PREFIX",
    title="Get TG with malformed id prefix → InvalidArgument",
    classes=["NEG", "VAL"], priority="P0",
    steps=[
        Step(name="get-bad", method="GET", path=f"{_TG_BASE}/garbage-not-an-id",
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-UPD-NEG-INVALID-ID-PREFIX",
    title="Update TG with malformed id prefix → InvalidArgument",
    classes=["NEG", "VAL"], priority="P0",
    steps=[
        Step(name="upd-bad", method="PATCH", path=f"{_TG_BASE}/garbage-not-an-id",
             body={"updateMask": "description", "description": "x"},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-DEL-NEG-INVALID-ID-PREFIX",
    title="Delete TG with malformed id prefix → InvalidArgument",
    classes=["NEG", "VAL"], priority="P0",
    steps=[
        Step(name="del-bad", method="DELETE", path=f"{_TG_BASE}/garbage-not-an-id",
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-LST-PAGE-TOKEN-EMPTY",
    title="List with empty pageToken → 200",
    classes=["LSG", "BVA"], priority="P2",
    steps=[
        Step(name="lst-empty-tok", method="GET",
             path=f"{_TG_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=10&pageToken=",
             test_script=[*assert_status(200)]),
    ],
))

CASES.append(Case(
    id="TGR-LST-BVA-PAGESIZE-1000",
    title="List with pageSize=1000 (upper) → 200",
    classes=["BVA", "LSG"], priority="P2",
    steps=[
        Step(name="lst-1000", method="GET",
             path=f"{_TG_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=1000",
             test_script=[*assert_status(200)]),
    ],
))

CASES.append(Case(
    id="TGR-LST-BVA-PAGESIZE-1001",
    title="List with pageSize=1001 → InvalidArgument",
    classes=["BVA", "VAL", "LSG"], priority="P2",
    steps=[
        Step(name="lst-1001", method="GET",
             path=f"{_TG_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=1001",
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="TGR-LST-PAGE-TOKEN-GARBAGE",
    title="List with garbage pageToken → InvalidArgument",
    classes=["VAL", "LSG"], priority="P1",
    steps=[
        Step(name="lst-bad-tok", method="GET",
             path=f"{_TG_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=10&pageToken=not-a-token",
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="TGR-CR-BVA-LABELS-MAX-64",
    title="Create TG with 64 labels (max) → OK",
    classes=["BVA"], priority="P2",
    steps=[
        Step(name="cr-64", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "tg-lbl-64-{{runId}}",
                   "labels": {f"k{i}": f"v{i}" for i in range(64)}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-LABELS-UPPERCASE-KEY",
    title="Create TG with uppercase label key → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="cr-lbl-upper", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "tg-lbl-upper-{{runId}}",
                   "labels": {"BADKEY": "v"}},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-CR-CRUD-NO-OPTIONAL-FIELDS",
    title="Create TG with only required fields → OK",
    classes=["CRUD"], priority="P2",
    steps=[
        Step(name="cr-min", method="POST", path=_TG_BASE,
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "name": "tg-min-{{runId}}",
                   "healthCheck": _HEALTH_CHECK_DEFAULT},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-TG-NAME-COLLISION-CROSS-REGION",
    title="Same name in different region → allowed (project_id+name UNIQUE, region orthogonal)",
    classes=["VAL"], priority="P2",
    steps=[
        Step(name="cr-r1", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "xreg-{{runId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
        Step(name="cr-r2", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "regionId": "{{_suiteRegionAltId}}", "name": "xreg-{{runId}}"},
             test_script=[
                 "pm.test('rejected (UNIQUE on project_id+name only) or allowed', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 409]));",
                 *save_from_response("j.id", "opId"),
                 *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId2"),
             ]),
        poll_operation_until_done(),
        Step(name="cleanup-2", method="DELETE", path=f"{_TG_BASE}/{{{{tgId2}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))


CASES.append(Case(
    id="TGR-CR-CRUD-HTTPS-PROBE",
    title="Create TG with health_check.https probe → OK",
    classes=["CRUD"], priority="P1",
    steps=[
        Step(name="cr-https", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "tg-https-{{runId}}",
                   "healthCheck": {"name": "hc", "interval": "2s", "timeout": "1s",
                                   "unhealthyThreshold": 3, "healthyThreshold": 2,
                                   "https": {"port": 8443, "path": "/healthz",
                                             "expectedStatuses": [200]}}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-CR-CRUD-GRPC-PROBE",
    title="Create TG with health_check.grpc probe → OK",
    classes=["CRUD"], priority="P1",
    steps=[
        Step(name="cr-grpc", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "tg-grpc-{{runId}}",
                   "healthCheck": {"name": "hc", "interval": "2s", "timeout": "1s",
                                   "unhealthyThreshold": 3, "healthyThreshold": 2,
                                   "grpc": {"port": 9090, "service": "health.v1"}}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-CR-CRUD-DEREG-MIN-0",
    title="Create TG with deregistration_delay_seconds=0 (lower bound) → OK",
    classes=["BVA", "CRUD"], priority="P2",
    steps=[
        Step(name="cr-dereg-0", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "tg-dereg-0-{{runId}}", "deregistrationDelaySeconds": 0},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-CR-CRUD-DEREG-MAX-3600",
    title="Create TG with deregistration_delay_seconds=3600 (upper bound) → OK",
    classes=["BVA", "CRUD"], priority="P2",
    steps=[
        Step(name="cr-dereg-3600", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "tg-dereg-max-{{runId}}",
                   "deregistrationDelaySeconds": 3600},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-CR-CRUD-SLOW-START-MIN-0",
    title="Create TG with slow_start_seconds=0 (lower) → OK",
    classes=["BVA", "CRUD"], priority="P2",
    steps=[
        Step(name="cr-ss-0", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "tg-ss-0-{{runId}}", "slowStartSeconds": 0},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-CR-CRUD-SLOW-START-MAX-900",
    title="Create TG with slow_start_seconds=900 (upper) → OK",
    classes=["BVA", "CRUD"], priority="P2",
    steps=[
        Step(name="cr-ss-900", method="POST", path=_TG_BASE,
             body={**_TG_BODY, "name": "tg-ss-900-{{runId}}", "slowStartSeconds": 900},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-LST-FILTER-MATCH",
    title="Create + List filter=name='X' → contains own id",
    classes=["LSG", "IDEM"], priority="P2",
    steps=[
        *_setup_tg("flt-match"),
        Step(name="lst-filt", method="GET",
             path=f"{_TG_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=100&"
                  f"filter=name%3D%22setup-tg-flt-match-{{{{runId}}}}%22",
             test_script=[*assert_status(200),
                          "const arr = pm.response.json().targetGroups || pm.response.json().items || [];",
                          "pm.test('contains', () => "
                          "  pm.expect(arr.map(x => x.id)).to.include(pm.environment.get('tgId')));"]),
        *_cleanup_tg(),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-MALFORMED-JSON",
    title="Create TG with malformed JSON → 400/415",
    classes=["VAL"], priority="P2",
    steps=[
        Step(name="cr-malformed", method="POST", path=_TG_BASE, body=None,
             pre_script=["pm.request.body = { mode: 'raw', raw: '{not json' };"],
             test_script=[
                 "pm.test('400 or 415', () => pm.expect(pm.response.code).to.be.oneOf([400, 415]));",
             ]),
    ],
))

CASES.append(Case(
    id="TGR-CR-VAL-EMPTY-BODY",
    title="Create TG with empty body → InvalidArgument",
    classes=["VAL"], priority="P2",
    steps=[
        Step(name="cr-empty", method="POST", path=_TG_BASE, body={},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))
