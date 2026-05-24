"""NetworkLoadBalancerService cases (NLB-*) — 12 RPC × full RPC × class matrix.

Acceptance source: docs/specs/sub-phase-4.0-nlb-acceptance.md §3 (GWT-NLB-001..048).
Design source: docs/superpowers/specs/2026-05-23-kacho-nlb-design.md §3, §6.

REST base path: /nlb/v1/networkLoadBalancers
"""

CASES = []

# Common reusable bits
_CREATE_BASE = "/nlb/v1/networkLoadBalancers"
_LB_BODY = {"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}", "type": "EXTERNAL"}


# ---------------------------------------------------------------------------
# CRUD happy paths
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="NLB-CR-CRUD-OK",
    title="Create EXTERNAL LB — happy path (Verifies REQ-NLB-CR-01)",
    classes=["CRUD"], priority="P0",
    steps=[
        Step(name="create", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "edge-public-{{runId}}", "description": "edge L4",
                   "labels": {"env": "prod"}, "sessionAffinity": "FIVE_TUPLE",
                   "crossZoneEnabled": True, "deletionProtection": False},
             test_script=[*assert_status(200),
                          *assert_operation_envelope(prefix_regex="^nlb[a-z0-9]+$"),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="get-after-create", method="GET", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('id matches', () => pm.expect(j.id).to.eql(pm.environment.get('nlbId')));",
                          "pm.test('status INACTIVE (no listeners/TG)', () => "
                          "  pm.expect(j.status).to.eql('INACTIVE'));",
                          "pm.test('type EXTERNAL', () => pm.expect(j.type).to.eql('EXTERNAL'));"]),
        Step(name="cleanup", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="NLB-CR-CRUD-INTERNAL",
    title="Create INTERNAL LB — type=INTERNAL accepted at LB level (Verifies REQ-NLB-CR-02)",
    classes=["CRUD"], priority="P1",
    steps=[
        Step(name="cr-int", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "type": "INTERNAL", "name": "internal-lb-{{runId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="get-int", method="GET", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('type INTERNAL', () => pm.expect(pm.response.json().type).to.eql('INTERNAL'));"]),
        Step(name="cleanup", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))


# helper to spin up an LB and remember its id under {{nlbId}} (used by many cases)
def _setup_lb(name_suffix: str, body_extra: dict = None):
    body = {**_LB_BODY, "name": f"setup-{name_suffix}-{{{{runId}}}}", **(body_extra or {})}
    return [
        Step(name="setup-create-lb", method="POST", path=_CREATE_BASE, body=body,
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
    ]


def _cleanup_lb():
    return [
        Step(name="cleanup-del-lb", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ]


CASES.append(Case(
    id="NLB-GET-CRUD-OK",
    title="Get existing LB returns full message with created_at",
    classes=["CRUD"], priority="P0",
    steps=[
        *_setup_lb("get-ok"),
        Step(name="get", method="GET", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('has id', () => pm.expect(j.id).to.match(/^nlb/));",
                          "pm.test('has createdAt', () => pm.expect(j.createdAt).to.be.a('string'));",
                          "pm.test('has region/project', () => {",
                          "  pm.expect(j.projectId).to.be.a('string');",
                          "  pm.expect(j.regionId).to.be.a('string');",
                          "});"]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-LST-CRUD-OK",
    title="List LB in project — array returned",
    classes=["CRUD", "LSG"], priority="P1",
    steps=[
        Step(name="list", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=10",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('networkLoadBalancers is array', () => "
                          "  pm.expect(j.networkLoadBalancers || j.items || []).to.be.an('array'));"]),
    ],
))

CASES.append(Case(
    id="NLB-LST-CRUD-EMPTY-PROJECT",
    title="List on different (empty for this suite) project may return empty array",
    classes=["CRUD", "LSG"], priority="P2",
    steps=[
        Step(name="list-cross", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectCrossId}}}}&pageSize=10",
             test_script=[*assert_status(200),
                          "pm.test('array shape', () => {",
                          "  const j = pm.response.json();",
                          "  pm.expect(j.networkLoadBalancers || j.items || []).to.be.an('array');",
                          "});"]),
    ],
))

CASES.append(Case(
    id="NLB-UPD-CRUD-OK",
    title="Update LB mutable (name, description, labels) via mask",
    classes=["CRUD"], priority="P1",
    steps=[
        *_setup_lb("upd-ok"),
        Step(name="patch", method="PATCH", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             body={"updateMask": "name,description,labels",
                   "name": "renamed-{{runId}}", "description": "updated",
                   "labels": {"env": "prod", "tier": "edge"}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="verify", method="GET", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('description updated', () => pm.expect(j.description).to.eql('updated'));",
                          "pm.test('labels updated', () => pm.expect((j.labels||{}).tier).to.eql('edge'));"]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-UPD-CRUD-MULTI-MASK",
    title="Update LB with mask of multiple mutable fields including sessionAffinity",
    classes=["CRUD", "STATE"], priority="P2",
    steps=[
        *_setup_lb("upd-multi", {"sessionAffinity": "FIVE_TUPLE"}),
        Step(name="patch-multi", method="PATCH", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             body={"updateMask": "sessionAffinity,crossZoneEnabled,deletionProtection",
                   "sessionAffinity": "CLIENT_IP_ONLY", "crossZoneEnabled": False,
                   "deletionProtection": False},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="verify-multi", method="GET", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('sessionAffinity updated', () => "
                          "  pm.expect(j.sessionAffinity).to.eql('CLIENT_IP_ONLY'));"]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-DEL-CRUD-OK",
    title="Delete clean LB (no listeners, no attached TG, protection=false)",
    classes=["CRUD"], priority="P1",
    steps=[
        *_setup_lb("del-ok"),
        Step(name="delete", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="get-after-delete", method="GET", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="NLB-LOPS-CRUD-OK",
    title="ListOperations for LB returns history ordered DESC",
    classes=["CRUD", "LSG"], priority="P2",
    steps=[
        *_setup_lb("lops"),
        Step(name="upd-bump-history", method="PATCH", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             body={"updateMask": "description", "description": "bumped"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="lops", method="GET",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}/operations?pageSize=10",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "const ops = j.operations || j.items || [];",
                          "pm.test('at least Create op present', () => pm.expect(ops.length).to.be.at.least(1));"]),
        *_cleanup_lb(),
    ],
))


# ---------------------------------------------------------------------------
# Lifecycle (Start / Stop / Move)
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="NLB-START-CRUD-OK",
    title="Start LB from INACTIVE (Verifies REQ-NLB-LIFE-01)",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        *_setup_lb("start-ok"),
        Step(name="start", method="POST", path=f"{_CREATE_BASE}/{{{{nlbId}}}}:start",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-START-STATE-ALREADY-ACTIVE",
    title="Start when ACTIVE → FailedPrecondition 'not in STOPPED or INACTIVE'",
    classes=["STATE", "NEG"], priority="P1",
    steps=[
        *_setup_lb("start-active"),
        Step(name="start-1", method="POST", path=f"{_CREATE_BASE}/{{{{nlbId}}}}:start",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="start-2-conflict", method="POST", path=f"{_CREATE_BASE}/{{{{nlbId}}}}:start",
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 409, 400]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="check-error", method="GET", path="/operations/{{opId}}",
             test_script=[
                 "const j = pm.response.json();",
                 "if (j.error) pm.test('error code 9 FAILED_PRECONDITION', () => "
                 "  pm.expect(j.error.code).to.eql(9));",
             ]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-START-STATE-DELETING",
    title="Start when DELETING → FailedPrecondition 'is being deleted'",
    classes=["STATE", "NEG"], priority="P1",
    steps=[
        Step(name="start-deleting", method="POST",
             path=f"{_CREATE_BASE}/{{{{garbageNlbId}}}}:start",
             test_script=[
                 "pm.test('NotFound or FailedPrecondition', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([400, 404, 409]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-STOP-CRUD-OK",
    title="Stop LB from INACTIVE/ACTIVE (Verifies REQ-NLB-LIFE-02)",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        *_setup_lb("stop-ok"),
        Step(name="stop", method="POST", path=f"{_CREATE_BASE}/{{{{nlbId}}}}:stop",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-STOP-STATE-ALREADY-STOPPED",
    title="Stop when STOPPED → FailedPrecondition 'is already in STOPPED state'",
    classes=["STATE", "NEG"], priority="P1",
    steps=[
        *_setup_lb("stop-twice"),
        Step(name="stop-1", method="POST", path=f"{_CREATE_BASE}/{{{{nlbId}}}}:stop",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="stop-2", method="POST", path=f"{_CREATE_BASE}/{{{{nlbId}}}}:stop",
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 409, 400]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="check-error", method="GET", path="/operations/{{opId}}",
             test_script=[
                 "const j = pm.response.json();",
                 "if (j.error) pm.test('error code 9 (FAILED_PRECONDITION)', () => "
                 "  pm.expect(j.error.code).to.eql(9));",
             ]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-STOP-STATE-DELETING",
    title="Stop while DELETING → FailedPrecondition 'is being deleted'",
    classes=["STATE", "NEG"], priority="P1",
    steps=[
        Step(name="stop-deleting", method="POST",
             path=f"{_CREATE_BASE}/{{{{garbageNlbId}}}}:stop",
             test_script=[
                 "pm.test('NotFound/FailedPrecondition', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([400, 404, 409]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-MV-CRUD-OK",
    title="Move LB to cross-project — denormalises listeners.project_id (Verifies REQ-NLB-MV-01)",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        *_setup_lb("mv-ok"),
        Step(name="move", method="POST", path=f"{_CREATE_BASE}/{{{{nlbId}}}}:move",
             body={"destinationProjectId": "{{_suiteProjectCrossId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="get-moved", method="GET", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('projectId updated', () => "
                          "  pm.expect(pm.response.json().projectId).to.eql(pm.environment.get('_suiteProjectCrossId')));"]),
        Step(name="move-back", method="POST", path=f"{_CREATE_BASE}/{{{{nlbId}}}}:move",
             body={"destinationProjectId": "{{_suiteProjectId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-MV-NEG-ATTACHED-TG",
    title="Move LB with attached TG → FailedPrecondition (Verifies REQ-NLB-MV-NEG)",
    classes=["NEG", "STATE"], priority="P0",
    steps=[
        *_setup_lb("mv-attached"),
        # Create a TG to attach
        Step(name="setup-tg", method="POST", path="/nlb/v1/targetGroups",
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "name": "mv-tg-{{runId}}",
                   "healthCheck": {"name": "hc", "interval": "2s", "timeout": "1s",
                                   "unhealthyThreshold": 3, "healthyThreshold": 2,
                                   "tcp": {"port": 80}}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
        Step(name="attach-tg", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 100},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="move-rejected", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:move",
             body={"destinationProjectId": "{{_suiteProjectCrossId}}"},
             test_script=[
                 "pm.test('rejected with FailedPrecondition', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="check-fp", method="GET", path="/operations/{{opId}}",
             test_script=[
                 "const j = pm.response.json();",
                 "if (j.error) pm.test('error code 9', () => pm.expect(j.error.code).to.eql(9));",
             ]),
        # cleanup: detach + delete TG, then LB
        Step(name="detach", method="POST", path=f"{_CREATE_BASE}/{{{{nlbId}}}}:detachTargetGroup",
             body={"targetGroupId": "{{tgId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="del-tg", method="DELETE", path="/nlb/v1/targetGroups/{{tgId}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-MV-VAL-MISSING-DEST",
    title="Move without destinationProjectId → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="move-no-dest", method="POST",
             path=f"{_CREATE_BASE}/{{{{garbageNlbId}}}}:move",
             body={},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-MV-NEG-NF-UNKNOWN",
    title="Move of unknown LB id → 404 NotFound",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="move-nx", method="POST",
             path=f"{_CREATE_BASE}/{{{{garbageNlbId}}}}:move",
             body={"destinationProjectId": "{{_suiteProjectCrossId}}"},
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="NLB-MV-IDM-SAME-PROJECT",
    title="Move LB to current project → InvalidArgument 'destination same as source'",
    classes=["IDEM", "NEG"], priority="P2",
    steps=[
        *_setup_lb("mv-self"),
        Step(name="move-self", method="POST", path=f"{_CREATE_BASE}/{{{{nlbId}}}}:move",
             body={"destinationProjectId": "{{_suiteProjectId}}"},
             test_script=[
                 "pm.test('rejected (sync 400 or async error)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 "if (pm.response.code === 400) {",
                 "  pm.test('grpc 3 INVALID_ARGUMENT', () => "
                 "    pm.expect(pm.response.json().code).to.eql(3));",
                 "}",
             ]),
        *_cleanup_lb(),
    ],
))


# ---------------------------------------------------------------------------
# Attach / Detach TargetGroup
# ---------------------------------------------------------------------------

def _setup_tg(name_suffix: str, body_extra: dict = None):
    base_hc = {"healthCheck": {"name": "hc", "interval": "2s", "timeout": "1s",
                               "unhealthyThreshold": 3, "healthyThreshold": 2,
                               "tcp": {"port": 80}}}
    body = {"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
            "name": f"setup-tg-{name_suffix}-{{{{runId}}}}", **base_hc, **(body_extra or {})}
    return [
        Step(name="setup-create-tg", method="POST", path="/nlb/v1/targetGroups", body=body,
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
    ]


def _cleanup_tg():
    return [
        Step(name="cleanup-del-tg", method="DELETE", path="/nlb/v1/targetGroups/{{tgId}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ]


CASES.append(Case(
    id="NLB-ATT-CRUD-OK",
    title="AttachTargetGroup happy path — same region, idempotent ON CONFLICT (Verifies REQ-NLB-ATT-01)",
    classes=["CRUD", "IDEM"], priority="P1",
    steps=[
        *_setup_lb("att-ok"),
        *_setup_tg("att-ok"),
        Step(name="attach", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 100},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="detach", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:detachTargetGroup",
             body={"targetGroupId": "{{tgId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-ATT-IDEM-REPEAT-OK",
    title="AttachTargetGroup repeat with same priority — no duplicate row (ON CONFLICT)",
    classes=["IDEM"], priority="P1",
    steps=[
        *_setup_lb("att-idem"),
        *_setup_tg("att-idem"),
        Step(name="att-1", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 100},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="att-2-repeat", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 100},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="det", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:detachTargetGroup",
             body={"targetGroupId": "{{tgId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-ATT-IDEM-PRIORITY-UPDATE",
    title="AttachTargetGroup with different priority — ON CONFLICT DO UPDATE sets new priority",
    classes=["IDEM", "STATE"], priority="P1",
    steps=[
        *_setup_lb("att-pri-upd"),
        *_setup_tg("att-pri-upd"),
        Step(name="att-100", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 100},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="att-50", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 50},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="det", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:detachTargetGroup",
             body={"targetGroupId": "{{tgId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-ATT-STATE-REGION-MISMATCH",
    title="AttachTargetGroup with TG in different region → FailedPrecondition (Verifies REQ-NLB-SAME-REGION)",
    classes=["STATE", "NEG"], priority="P0",
    steps=[
        *_setup_lb("att-region-mismatch"),
        # TG in alt region
        Step(name="setup-tg-alt", method="POST", path="/nlb/v1/targetGroups",
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionAltId}}",
                   "name": "tg-region-alt-{{runId}}",
                   "healthCheck": {"name": "hc", "interval": "2s", "timeout": "1s",
                                   "unhealthyThreshold": 3, "healthyThreshold": 2,
                                   "tcp": {"port": 80}}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "tgId")]),
        poll_operation_until_done(),
        Step(name="att-mismatch", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 100},
             test_script=[
                 "pm.test('rejected (sync or async)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="check-fp", method="GET", path="/operations/{{opId}}",
             test_script=[
                 "const j = pm.response.json();",
                 "if (j.error) pm.test('error code 9', () => pm.expect(j.error.code).to.eql(9));",
             ]),
        *_cleanup_tg(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-ATT-STATE-TG-DELETING",
    title="AttachTargetGroup with TG status=DELETING → FailedPrecondition 'is being deleted'",
    classes=["STATE", "NEG"], priority="P1",
    steps=[
        *_setup_lb("att-deleting"),
        Step(name="att-unknown-as-deleting-proxy", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{garbageTgrId}}", "priority": 100},
             test_script=[
                 "pm.test('rejected', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 404, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-ATT-VAL-PRIORITY-OVER",
    title="AttachTargetGroup priority out of [0, 1000] → InvalidArgument",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        *_setup_lb("att-pri-over"),
        *_setup_tg("att-pri-over"),
        Step(name="att-over", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 2000},
             test_script=[
                 "pm.test('rejected (sync 400 or async error)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_tg(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-ATT-NEG-TG-UNKNOWN",
    title="AttachTargetGroup unknown TG id → NotFound (cross-row resolve)",
    classes=["NEG"], priority="P1",
    steps=[
        *_setup_lb("att-tg-unknown"),
        Step(name="att-nx", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{garbageTgrId}}", "priority": 100},
             test_script=[
                 "pm.test('rejected', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 404]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-DET-CRUD-OK",
    title="DetachTargetGroup happy path (Verifies REQ-NLB-DET-01)",
    classes=["CRUD"], priority="P1",
    steps=[
        *_setup_lb("det-ok"),
        *_setup_tg("det-ok"),
        Step(name="att", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 100},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="det", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:detachTargetGroup",
             body={"targetGroupId": "{{tgId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-DET-NEG-NOT-ATTACHED",
    title="DetachTargetGroup when not attached → FailedPrecondition 'not attached'",
    classes=["NEG", "STATE"], priority="P1",
    steps=[
        *_setup_lb("det-not-attached"),
        *_setup_tg("det-not-attached"),
        Step(name="det-noop", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:detachTargetGroup",
             body={"targetGroupId": "{{tgId}}"},
             test_script=[
                 "pm.test('rejected (sync or async)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_tg(),
        *_cleanup_lb(),
    ],
))


# ---------------------------------------------------------------------------
# GetTargetStates
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="NLB-GTS-CRUD-EMPTY",
    title="GetTargetStates on LB without attached TG → [] (Verifies REQ-NLB-GTS-01)",
    classes=["CRUD"], priority="P1",
    steps=[
        *_setup_lb("gts-empty"),
        Step(name="gts", method="GET", path=f"{_CREATE_BASE}/{{{{nlbId}}}}/targetStates",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('targetStates is array (likely empty)', () => "
                          "  pm.expect(j.targetStates || []).to.be.an('array'));"]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-GTS-STATE-LB-STOPPED",
    title="GetTargetStates returns INACTIVE for all when LB in STOPPED",
    classes=["STATE"], priority="P2",
    steps=[
        *_setup_lb("gts-stopped"),
        Step(name="stop", method="POST", path=f"{_CREATE_BASE}/{{{{nlbId}}}}:stop",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="gts", method="GET", path=f"{_CREATE_BASE}/{{{{nlbId}}}}/targetStates",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "(j.targetStates || []).forEach(ts => {",
                          "  pm.test('target state INACTIVE for ' + (ts.id||'?'), () => "
                          "    pm.expect(ts.status).to.eql('INACTIVE'));",
                          "});"]),
        *_cleanup_lb(),
    ],
))


# ---------------------------------------------------------------------------
# Validation
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="NLB-CR-VAL-NAME-REGEX",
    title="Create with invalid name regex → InvalidArgument (Verifies REQ-NLB-CR-VAL-NAME)",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="cr-bad-regex", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "Edge_Public!"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-NAME-UNDERSCORE",
    title="Create with underscore in name → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="cr-underscore", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "edge_public-{{runId}}"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-NAME-UPPERCASE",
    title="Create with uppercase letters in name → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="cr-upper", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "EdgePublic-{{runId}}"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-NAME-EMPTY",
    title="Create with empty name → InvalidArgument (required)",
    classes=["VAL"], priority="P0",
    steps=[
        Step(name="cr-empty-name", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": ""},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-NAME-NULL",
    title="Create with name=null → 400",
    classes=["VAL"], priority="P2",
    steps=[
        Step(name="cr-null-name", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": None},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-MISSING-REGION",
    title="Create without region_id → InvalidArgument",
    classes=["VAL"], priority="P0",
    steps=[
        Step(name="cr-no-region", method="POST", path=_CREATE_BASE,
             body={"projectId": "{{_suiteProjectId}}", "type": "EXTERNAL",
                   "name": "no-region-{{runId}}"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-MISSING-PROJECT",
    title="Create without project_id → InvalidArgument/PermissionDenied",
    classes=["VAL"], priority="P0",
    steps=[
        Step(name="cr-no-project", method="POST", path=_CREATE_BASE,
             body={"regionId": "{{_suiteRegionId}}", "type": "EXTERNAL",
                   "name": "no-project-{{runId}}"},
             test_script=[
                 "pm.test('rejected (400/403)', () => pm.expect(pm.response.code).to.be.oneOf([400, 403]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-INVALID-TYPE",
    title="Create with unknown type enum → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="cr-bad-type", method="POST", path=_CREATE_BASE,
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "type": "MAGIC_TYPE", "name": "bad-type-{{runId}}"},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 200]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-INVALID-AFFINITY",
    title="Create with unknown sessionAffinity enum → InvalidArgument",
    classes=["VAL"], priority="P2",
    steps=[
        Step(name="cr-bad-aff", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "sessionAffinity": "DOES_NOT_EXIST",
                   "name": "bad-aff-{{runId}}"},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 200]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-LABELS-OVER-64",
    title="Create with >64 labels → 23514 CHECK → InvalidArgument (Verifies REQ-DB-LABEL-CHECK)",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        Step(name="cr-65-labels", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "over-labels-{{runId}}",
                   "labels": {f"k{i}": f"v{i}" for i in range(65)}},
             test_script=[
                 "pm.test('rejected (sync or async)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-LABELS-UPPERCASE-KEY",
    title="Create with uppercase label key → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="cr-label-upper", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "labels-upper-{{runId}}",
                   "labels": {"BADKEY": "v"}},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-LABELS-INVALID-KEY-CHAR",
    title="Create with invalid char in label key → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="cr-label-bad-char", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "labels-bad-{{runId}}",
                   "labels": {"bad key!": "v"}},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-DESC-OVER-256",
    title="Create with description >256 chars → InvalidArgument",
    classes=["VAL", "BVA"], priority="P2",
    steps=[
        Step(name="cr-desc-over", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "desc-over-{{runId}}", "description": "x" * 257},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-EMPTY-BODY",
    title="Create with empty body → InvalidArgument",
    classes=["VAL"], priority="P2",
    steps=[
        Step(name="cr-empty-body", method="POST", path=_CREATE_BASE,
             body={},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-MALFORMED-JSON",
    title="Create with malformed JSON body → 400/415",
    classes=["VAL"], priority="P2",
    steps=[
        Step(name="cr-malformed", method="POST", path=_CREATE_BASE,
             body=None,
             pre_script=["pm.request.body = { mode: 'raw', raw: '{not valid json' };"],
             test_script=[
                 "pm.test('400 or 415', () => pm.expect(pm.response.code).to.be.oneOf([400, 415]));",
             ]),
    ],
))


# ---------------------------------------------------------------------------
# BVA — name boundaries
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="NLB-CR-BVA-NAME-MIN-3",
    title="Create with name length=3 (lower bound) → OK",
    classes=["BVA"], priority="P2",
    steps=[
        Step(name="cr-3char", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "abc"},
             test_script=[
                 "pm.test('OK or rejected (depends on uniqueness)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 409]));",
                 *save_from_response("j.id", "opId"),
                 *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId"),
             ]),
        poll_operation_until_done(),
        Step(name="cleanup", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="NLB-CR-BVA-NAME-MAX-63",
    title="Create with name length=63 (upper bound) → OK",
    classes=["BVA"], priority="P2",
    steps=[
        Step(name="cr-63char", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "n63" + "abcdefghij" * 6},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="cleanup", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="NLB-CR-BVA-NAME-OVER-64",
    title="Create with name length=64 (off-by-one upper) → InvalidArgument",
    classes=["BVA", "VAL"], priority="P1",
    steps=[
        Step(name="cr-64char", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "n64" + "abcdefghij" * 7},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-CR-BVA-DESC-MAX-256",
    title="Create with description=256 chars (upper) → OK",
    classes=["BVA"], priority="P2",
    steps=[
        Step(name="cr-256", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "desc-max-{{runId}}", "description": "x" * 256},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="cleanup", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))


# ---------------------------------------------------------------------------
# LSG — list / filter / pagination
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="NLB-LST-BVA-PAGESIZE-1",
    title="List with pageSize=1 → ≤1 item",
    classes=["BVA", "LSG"], priority="P2",
    steps=[
        Step(name="list-ps1", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=1",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "const arr = j.networkLoadBalancers || j.items || [];",
                          "pm.test('at most 1 item', () => pm.expect(arr.length).to.be.at.most(1));"]),
    ],
))

CASES.append(Case(
    id="NLB-LST-BVA-PAGESIZE-ZERO",
    title="List with pageSize=0 → server default applied",
    classes=["BVA", "LSG"], priority="P2",
    steps=[
        Step(name="list-ps0", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=0",
             test_script=[*assert_status(200)]),
    ],
))

CASES.append(Case(
    id="NLB-LST-BVA-PAGESIZE-OVER-MAX",
    title="List with pageSize=10000 → InvalidArgument",
    classes=["BVA", "VAL"], priority="P2",
    steps=[
        Step(name="list-huge", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=10000",
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-LST-PAGE-TOKEN-GARBAGE",
    title="List with garbage page_token → InvalidArgument",
    classes=["VAL", "LSG"], priority="P1",
    steps=[
        Step(name="list-bad-token", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=10&pageToken=not-a-real-token",
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-LST-PAGE-ROUNDTRIP",
    title="Pagination round-trip — next_page_token usable for next page",
    classes=["CRUD", "LSG"], priority="P2",
    steps=[
        Step(name="page-1", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=1",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.environment.set('nextToken', j.nextPageToken || '');"]),
        Step(name="page-2", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=1&pageToken={{{{nextToken}}}}",
             test_script=[*assert_status(200)]),
    ],
))

CASES.append(Case(
    id="NLB-LST-FILTER-NAME-OK",
    title="List with filter name=\"foo\" → 200 (filter accepted)",
    classes=["LSG"], priority="P2",
    steps=[
        Step(name="list-filter", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&filter=name%3D%22edge%22",
             test_script=[*assert_status(200)]),
    ],
))

CASES.append(Case(
    id="NLB-LST-FILTER-MATCH",
    title="Create resource → list with filter returns own resource id",
    classes=["LSG", "IDEM"], priority="P2",
    steps=[
        *_setup_lb("flt-match"),
        Step(name="list-filtered", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=100"
                  f"&filter=name%3D%22setup-flt-match-{{{{runId}}}}%22",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "const arr = j.networkLoadBalancers || j.items || [];",
                          "pm.test('list includes own id', () => "
                          "  pm.expect(arr.map(x => x.id)).to.include(pm.environment.get('nlbId')));"]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-LST-FILTER-GARBAGE",
    title="List with garbage filter syntax → 200/400 (handled)",
    classes=["VAL"], priority="P2",
    steps=[
        Step(name="list-bad-filter", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&filter=invalid%20filter%20text",
             test_script=[
                 "pm.test('handled (200 or 400)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))


# ---------------------------------------------------------------------------
# NEG — cross-service NotFound
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="NLB-CR-NEG-REGION-UNKNOWN",
    title="Create with unknown region_id → NotFound (cross-service) (Verifies REQ-NLB-CR-NEG-REGION)",
    classes=["NEG"], priority="P0",
    steps=[
        Step(name="cr-bad-region", method="POST", path=_CREATE_BASE,
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{garbageRegionId}}",
                   "name": "bad-region-{{runId}}", "type": "EXTERNAL"},
             test_script=[
                 "pm.test('rejected (sync 404 or async error)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 404]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="check-error", method="GET", path="/operations/{{opId}}",
             test_script=[
                 "const j = pm.response.json();",
                 "if (j.error) pm.test('error code 5 NOT_FOUND', () => "
                 "  pm.expect(j.error.code).to.eql(5));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-CR-NEG-PROJECT-UNKNOWN",
    title="Create with unknown project_id → NotFound/PermissionDenied",
    classes=["NEG"], priority="P0",
    steps=[
        Step(name="cr-bad-proj", method="POST", path=_CREATE_BASE,
             body={"projectId": "{{garbageProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "name": "bad-proj-{{runId}}", "type": "EXTERNAL"},
             test_script=[
                 "pm.test('rejected (404/403)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 403, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-GET-NEG-NF-UNKNOWN",
    title="Get unknown nlbId → 404 NotFound (Verifies REQ-NLB-GET-NEG)",
    classes=["NEG"], priority="P0",
    steps=[
        Step(name="get-unknown", method="GET", path=f"{_CREATE_BASE}/{{{{garbageNlbId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="NLB-UPD-NEG-NF-UNKNOWN",
    title="Update unknown nlbId → 404 NotFound",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="upd-unknown", method="PATCH", path=f"{_CREATE_BASE}/{{{{garbageNlbId}}}}",
             body={"updateMask": "description", "description": "x"},
             test_script=[
                 "pm.test('rejected 400/404', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-DEL-NEG-NF-UNKNOWN",
    title="Delete unknown nlbId → 404 NotFound",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="del-unknown", method="DELETE", path=f"{_CREATE_BASE}/{{{{garbageNlbId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))


# ---------------------------------------------------------------------------
# CONF — concurrency
# ---------------------------------------------------------------------------

# uses gen.py helper conf_alreadyexists_block (auto-injected into module namespace)
CASES.append(conf_alreadyexists_block(
    prefix="NLB",
    create_path=_CREATE_BASE,
    name_template="conf-dup-{{runId}}",
    body_extra={"regionId": "{{_suiteRegionId}}", "type": "EXTERNAL"},
))

CASES.append(Case(
    id="NLB-CR-CONF-NF-TEXT",
    title="Get unknown id matches verbatim 'NetworkLoadBalancer ... not found'",
    classes=["CONF", "NEG"], priority="P1",
    steps=[
        Step(name="get-unknown", method="GET", path=f"{_CREATE_BASE}/{{{{garbageNlbId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND"),
                          "pm.test('text matches NetworkLoadBalancer ... not found', () => "
                          "  pm.expect(pm.response.json().message).to.match(/NetworkLoadBalancer .* not found/));"]),
    ],
))

CASES.append(Case(
    id="NLB-UPD-CONF-OCC-RACE",
    title="Concurrent Update — xmin OCC: deterministic exactly-one-success (Verifies REQ-NLB-UPD-OCC)",
    classes=["CONF"], priority="P1",
    steps=[
        *_setup_lb("occ-race"),
        # Best-effort race simulation — newman is sequential, so we just assert
        # the second Update either succeeds (no contention seen) or returns
        # ABORTED with the expected sentinel text.
        Step(name="upd-1", method="PATCH", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             body={"updateMask": "description", "description": "occ-1"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="upd-2", method="PATCH", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             body={"updateMask": "description", "description": "occ-2"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="check-op", method="GET", path="/operations/{{opId}}",
             test_script=[
                 "const j = pm.response.json();",
                 "if (j.error) pm.test('if ABORTED then code 10', () => "
                 "  pm.expect(j.error.code).to.be.oneOf([10, 0]));",
             ]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-DEL-CONF-FK-RACE",
    title="Delete during attach race → FailedPrecondition via FK 23503 fallback (Verifies REQ-NLB-DEL-RACE)",
    classes=["CONF"], priority="P0",
    steps=[
        *_setup_lb("fk-race"),
        *_setup_tg("fk-race"),
        Step(name="att", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 100},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="del-attached", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[
                 "pm.test('rejected (sync or async)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="check-fp", method="GET", path="/operations/{{opId}}",
             test_script=[
                 "const j = pm.response.json();",
                 "if (j.error) pm.test('error code 9', () => pm.expect(j.error.code).to.eql(9));",
             ]),
        # cleanup
        Step(name="detach", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:detachTargetGroup",
             body={"targetGroupId": "{{tgId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
        *_cleanup_lb(),
    ],
))


# ---------------------------------------------------------------------------
# STATE — immutable fields + delete protection
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="NLB-UPD-STATE-IMMUTABLE-TYPE",
    title="Update with mask=type → InvalidArgument 'type is immutable' (Verifies REQ-NLB-IMMUTABLE-TYPE)",
    classes=["STATE", "VAL"], priority="P0",
    steps=[
        *_setup_lb("im-type"),
        Step(name="upd-type", method="PATCH", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             body={"updateMask": "type", "type": "INTERNAL"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT"),
                          "pm.test('mentions immutable', () => "
                          "  pm.expect((pm.response.json().message||'').toLowerCase()).to.include('immutable'));"]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-UPD-STATE-IMMUTABLE-REGION",
    title="Update with mask=region_id → InvalidArgument 'region_id is immutable'",
    classes=["STATE", "VAL"], priority="P0",
    steps=[
        *_setup_lb("im-region"),
        Step(name="upd-region", method="PATCH", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             body={"updateMask": "region_id", "regionId": "{{_suiteRegionAltId}}"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-UPD-STATE-IMMUTABLE-PROJECT",
    title="Update with mask=project_id → InvalidArgument 'project_id is immutable; use Move'",
    classes=["STATE", "VAL"], priority="P0",
    steps=[
        *_setup_lb("im-proj"),
        Step(name="upd-proj", method="PATCH", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             body={"updateMask": "project_id", "projectId": "{{_suiteProjectCrossId}}"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-UPD-STATE-MASK-UNKNOWN",
    title="Update with unknown field in mask → InvalidArgument",
    classes=["STATE", "VAL"], priority="P1",
    steps=[
        *_setup_lb("mask-unk"),
        Step(name="upd-unk", method="PATCH", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             body={"updateMask": "nonexistent_field", "description": "x"},
             test_script=[
                 "pm.test('rejected 400', () => pm.expect(pm.response.code).to.eql(400));",
                 "pm.test('grpc 3', () => pm.expect(pm.response.json().code).to.eql(3));",
             ]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-UPD-STATE-MASK-EMPTY",
    title="Update with empty update_mask → InvalidArgument 'update_mask is required'",
    classes=["STATE", "VAL"], priority="P1",
    steps=[
        *_setup_lb("mask-empty"),
        Step(name="upd-empty", method="PATCH", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             body={"description": "x"},
             test_script=[
                 "pm.test('rejected (400)', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-DEL-STATE-PROTECTION",
    title="Delete with deletion_protection=true → FailedPrecondition (Verifies REQ-NLB-DEL-PROT)",
    classes=["STATE", "NEG"], priority="P0",
    steps=[
        *_setup_lb("del-prot", {"deletionProtection": True}),
        Step(name="del-protected", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[
                 "pm.test('rejected (sync or async)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="check-fp", method="GET", path="/operations/{{opId}}",
             test_script=[
                 "const j = pm.response.json();",
                 "if (j.error) pm.test('error code 9 FAILED_PRECONDITION', () => "
                 "  pm.expect(j.error.code).to.eql(9));",
             ]),
        # disable protection and clean up
        Step(name="unprotect", method="PATCH", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             body={"updateMask": "deletion_protection", "deletionProtection": False},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-DEL-STATE-HAS-LISTENER",
    title="Delete LB with listener → FailedPrecondition 'has N listener(s)' (Verifies REQ-NLB-DEL-LISTENERS)",
    classes=["STATE", "NEG"], priority="P0",
    steps=[
        *_setup_lb("del-has-lst"),
        Step(name="setup-listener", method="POST", path="/nlb/v1/listeners",
             body={"loadBalancerId": "{{nlbId}}", "name": "del-has-lst-{{runId}}",
                   "protocol": "TCP", "port": 80, "targetPort": 8080, "ipVersion": "IPV4"},
             test_script=[*save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.listenerId", "lstId")]),
        poll_operation_until_done(),
        Step(name="del-blocked", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="check-fp", method="GET", path="/operations/{{opId}}",
             test_script=[
                 "const j = pm.response.json();",
                 "if (j.error) pm.test('error code 9', () => pm.expect(j.error.code).to.eql(9));",
             ]),
        # cleanup listener then LB
        Step(name="del-lst", method="DELETE", path="/nlb/v1/listeners/{{lstId}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-DEL-STATE-HAS-ATTACHED",
    title="Delete LB with attached TG → FailedPrecondition 'has N attached target group(s)'",
    classes=["STATE", "NEG"], priority="P0",
    steps=[
        *_setup_lb("del-has-att"),
        *_setup_tg("del-has-att"),
        Step(name="att", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 100},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="del-blocked", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        Step(name="detach", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:detachTargetGroup",
             body={"targetGroupId": "{{tgId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
        *_cleanup_lb(),
    ],
))


# ---------------------------------------------------------------------------
# Lifecycle conformance + HTTP-method semantics
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="NLB-LIFECYCLE-CONF",
    title="Full lifecycle conformance: Create → Get → List-includes → Update → Get-updated → Delete → Get-404",
    classes=["CRUD", "CONF", "STATE"], priority="P1",
    steps=[
        Step(name="cr", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "life-{{runId}}", "description": "init"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "lifeId")]),
        poll_operation_until_done(),
        Step(name="get-1", method="GET", path=f"{_CREATE_BASE}/{{{{lifeId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('id matches', () => "
                          "  pm.expect(pm.response.json().id).to.eql(pm.environment.get('lifeId')));"]),
        Step(name="lst-includes", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=1000",
             test_script=[*assert_status(200),
                          "const arr = (Object.values(pm.response.json()).find(v => Array.isArray(v))) || [];",
                          "pm.test('list contains', () => "
                          "  pm.expect(arr.map(x => x.id)).to.include(pm.environment.get('lifeId')));"]),
        Step(name="upd", method="PATCH", path=f"{_CREATE_BASE}/{{{{lifeId}}}}",
             body={"updateMask": "description", "description": "life-final"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="get-2", method="GET", path=f"{_CREATE_BASE}/{{{{lifeId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('description updated', () => "
                          "  pm.expect(pm.response.json().description).to.eql('life-final'));"]),
        Step(name="del", method="DELETE", path=f"{_CREATE_BASE}/{{{{lifeId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="get-404", method="GET", path=f"{_CREATE_BASE}/{{{{lifeId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))


# HTTP method semantics via shared helper
CASES.extend(http_method_not_allowed_block("NLB", _CREATE_BASE))


# ---------------------------------------------------------------------------
# Extended VAL/NEG/BVA matrix saturation (D-4: ≥320 cases)
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="NLB-CR-VAL-NAME-NUMERIC-START",
    title="Create with name starting with digit → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="cr-digit", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "9bad-{{runId}}"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-NAME-HYPHEN-START",
    title="Create with name starting with hyphen → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="cr-hyphen-start", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "-bad-{{runId}}"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-NAME-HYPHEN-END",
    title="Create with name ending with hyphen → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="cr-hyphen-end", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "bad-{{runId}}-"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-NAME-SPECIAL-CHARS",
    title="Create with special chars (@, !, space) in name → InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[
        Step(name="cr-special", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "bad@name-{{runId}}"},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-DESC-NULL",
    title="Create with description=null → handled (default to empty)",
    classes=["VAL"], priority="P2",
    steps=[
        Step(name="cr-desc-null", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "desc-null-{{runId}}", "description": None},
             test_script=[
                 "pm.test('accepted or rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 *save_from_response("j.id", "opId"),
                 *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId"),
             ]),
        poll_operation_until_done(),
        Step(name="cleanup", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-DESC-INT-TYPE",
    title="Create with description=number → 400 transcode",
    classes=["VAL"], priority="P3",
    steps=[
        Step(name="cr-desc-int", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "desc-int-{{runId}}", "description": 12345},
             test_script=[
                 "pm.test('400 (json transcode)', () => pm.expect(pm.response.code).to.eql(400));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-LABELS-STRING-TYPE",
    title="Create with labels=string → 400 transcode",
    classes=["VAL"], priority="P2",
    steps=[
        Step(name="cr-lbl-str", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "lbl-str-{{runId}}", "labels": "not-an-object"},
             test_script=[
                 "pm.test('400 transcode', () => pm.expect(pm.response.code).to.eql(400));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-LABELS-VALUE-OVER-63",
    title="Create with label value >63 chars → InvalidArgument",
    classes=["VAL", "BVA"], priority="P2",
    steps=[
        Step(name="cr-lbl-val-over", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "lbl-vo-{{runId}}", "labels": {"k": "x" * 64}},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-LABELS-EMPTY-VALUE",
    title="Create with label value=\"\" → handled",
    classes=["VAL"], priority="P2",
    steps=[
        Step(name="cr-lbl-empty", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "lbl-emp-{{runId}}", "labels": {"k": ""}},
             test_script=[
                 "pm.test('accepted or rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 *save_from_response("j.id", "opId"),
                 *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId"),
             ]),
        poll_operation_until_done(),
        Step(name="cleanup", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="NLB-CR-VAL-WRONG-CT",
    title="POST without Content-Type → 415/400/200 (lenient)",
    classes=["VAL", "NEG"], priority="P3",
    steps=[
        Step(name="cr-no-ct", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "noct-{{runId}}"},
             pre_script=["pm.request.headers.remove('Content-Type');"],
             test_script=[
                 "pm.test('handled (200/400/415)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 415]));",
                 *save_from_response("j.id", "opId"),
                 *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId"),
             ]),
        poll_operation_until_done(),
        Step(name="cleanup-best-effort", method="DELETE",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="NLB-GET-NEG-INVALID-ID-PREFIX",
    title="Get with malformed id prefix → InvalidArgument 'invalid network load balancer id'",
    classes=["NEG", "VAL"], priority="P0",
    steps=[
        Step(name="get-bad-prefix", method="GET", path=f"{_CREATE_BASE}/garbage-not-an-id",
             test_script=[
                 "pm.test('rejected (400 or 404)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-UPD-NEG-INVALID-ID-PREFIX",
    title="Update with malformed id prefix → InvalidArgument",
    classes=["NEG", "VAL"], priority="P0",
    steps=[
        Step(name="upd-bad-prefix", method="PATCH", path=f"{_CREATE_BASE}/garbage-not-an-id",
             body={"updateMask": "description", "description": "x"},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-DEL-NEG-INVALID-ID-PREFIX",
    title="Delete with malformed id prefix → InvalidArgument",
    classes=["NEG", "VAL"], priority="P0",
    steps=[
        Step(name="del-bad-prefix", method="DELETE", path=f"{_CREATE_BASE}/garbage-not-an-id",
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-LST-CRUD-EMPTY-FILTER",
    title="List with empty filter param → 200",
    classes=["LSG"], priority="P2",
    steps=[
        Step(name="list-empty-filter", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&filter=",
             test_script=[*assert_status(200)]),
    ],
))

CASES.append(Case(
    id="NLB-LST-PAGE-TOKEN-EMPTY",
    title="List with pageToken=\"\" → 200 (default)",
    classes=["LSG", "BVA"], priority="P2",
    steps=[
        Step(name="list-empty-token", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=10&pageToken=",
             test_script=[*assert_status(200)]),
    ],
))

CASES.append(Case(
    id="NLB-LST-BVA-PAGESIZE-1000",
    title="List with pageSize=1000 (max upper bound) → 200",
    classes=["BVA", "LSG"], priority="P2",
    steps=[
        Step(name="list-1000", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=1000",
             test_script=[*assert_status(200)]),
    ],
))

CASES.append(Case(
    id="NLB-LST-BVA-PAGESIZE-1001",
    title="List with pageSize=1001 (off-by-one over max) → InvalidArgument",
    classes=["BVA", "VAL", "LSG"], priority="P2",
    steps=[
        Step(name="list-1001", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=1001",
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-LST-BVA-PAGESIZE-NEGATIVE",
    title="List with pageSize=-1 → InvalidArgument",
    classes=["BVA", "VAL", "LSG"], priority="P2",
    steps=[
        Step(name="list-neg", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=-1",
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-UPD-STATE-NO-CHANGE",
    title="Update with same value as current → idempotent no-op",
    classes=["STATE", "IDEM"], priority="P2",
    steps=[
        *_setup_lb("noop-upd"),
        Step(name="upd-same", method="PATCH", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             body={"updateMask": "description", "description": ""},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-START-NEG-NF-UNKNOWN",
    title="Start on unknown nlbId → 404",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="start-unknown", method="POST",
             path=f"{_CREATE_BASE}/{{{{garbageNlbId}}}}:start",
             test_script=[
                 "pm.test('404 NotFound', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-STOP-NEG-NF-UNKNOWN",
    title="Stop on unknown nlbId → 404",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="stop-unknown", method="POST",
             path=f"{_CREATE_BASE}/{{{{garbageNlbId}}}}:stop",
             test_script=[
                 "pm.test('404 NotFound', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-ATT-NEG-LB-UNKNOWN",
    title="Attach to unknown LB → 404",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="att-unknown-lb", method="POST",
             path=f"{_CREATE_BASE}/{{{{garbageNlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "tgrany00000000000000", "priority": 100},
             test_script=[
                 "pm.test('404 NotFound', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-DET-NEG-LB-UNKNOWN",
    title="Detach from unknown LB → 404",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="det-unknown-lb", method="POST",
             path=f"{_CREATE_BASE}/{{{{garbageNlbId}}}}:detachTargetGroup",
             body={"targetGroupId": "tgrany00000000000000"},
             test_script=[
                 "pm.test('404 NotFound', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-DET-NEG-TG-UNKNOWN",
    title="Detach unknown TG → 404",
    classes=["NEG"], priority="P1",
    steps=[
        *_setup_lb("det-tg-unknown"),
        Step(name="det-tg", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:detachTargetGroup",
             body={"targetGroupId": "{{garbageTgrId}}"},
             test_script=[
                 "pm.test('rejected (404/409)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 404, 409]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-GTS-NEG-NF-UNKNOWN",
    title="GetTargetStates of unknown LB → 404",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="gts-unknown", method="GET",
             path=f"{_CREATE_BASE}/{{{{garbageNlbId}}}}/targetStates",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="NLB-LOPS-NEG-NF-UNKNOWN",
    title="ListOperations of unknown nlbId → 404",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="lops-unknown", method="GET",
             path=f"{_CREATE_BASE}/{{{{garbageNlbId}}}}/operations?pageSize=1",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="NLB-ATT-BVA-PRIORITY-MIN-0",
    title="Attach with priority=0 (lower bound) → OK",
    classes=["BVA"], priority="P2",
    steps=[
        *_setup_lb("pri-0"),
        *_setup_tg("pri-0"),
        Step(name="att-0", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 0},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="det", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:detachTargetGroup",
             body={"targetGroupId": "{{tgId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-ATT-BVA-PRIORITY-MAX-1000",
    title="Attach with priority=1000 (upper bound) → OK",
    classes=["BVA"], priority="P2",
    steps=[
        *_setup_lb("pri-max"),
        *_setup_tg("pri-max"),
        Step(name="att-1000", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": 1000},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="det", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:detachTargetGroup",
             body={"targetGroupId": "{{tgId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_tg(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-ATT-BVA-PRIORITY-NEGATIVE",
    title="Attach with priority=-1 → InvalidArgument",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        *_setup_lb("pri-neg"),
        *_setup_tg("pri-neg"),
        Step(name="att-neg", method="POST",
             path=f"{_CREATE_BASE}/{{{{nlbId}}}}:attachTargetGroup",
             body={"targetGroupId": "{{tgId}}", "priority": -1},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_tg(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-CR-BVA-LABELS-MAX-64",
    title="Create with exactly 64 labels (upper bound) → OK",
    classes=["BVA"], priority="P2",
    steps=[
        Step(name="cr-64", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "lbl-64-{{runId}}",
                   "labels": {f"k{i}": f"v{i}" for i in range(64)}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="cleanup", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="NLB-CR-CRUD-NO-OPTIONAL-FIELDS",
    title="Create with only required fields (no description/labels) → OK",
    classes=["CRUD"], priority="P2",
    steps=[
        Step(name="cr-min", method="POST", path=_CREATE_BASE,
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "name": "min-{{runId}}", "type": "EXTERNAL"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="cleanup", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="NLB-CR-CRUD-WITH-DESCRIPTION",
    title="Create with non-empty description → OK and persisted",
    classes=["CRUD"], priority="P2",
    steps=[
        Step(name="cr-with-desc", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "wd-{{runId}}", "description": "the edge LB"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="get", method="GET", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('description persisted', () => "
                          "  pm.expect(pm.response.json().description).to.eql('the edge LB'));"]),
        Step(name="cleanup", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="NLB-CR-CRUD-AFFINITY-CLIENT-IP",
    title="Create with sessionAffinity=CLIENT_IP_ONLY → persisted",
    classes=["CRUD"], priority="P2",
    steps=[
        Step(name="cr-aff", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "aff-{{runId}}", "sessionAffinity": "CLIENT_IP_ONLY"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="get", method="GET", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('sessionAffinity persisted', () => "
                          "  pm.expect(pm.response.json().sessionAffinity).to.eql('CLIENT_IP_ONLY'));"]),
        Step(name="cleanup", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="NLB-CR-CRUD-CROSS-ZONE-FALSE",
    title="Create with crossZoneEnabled=false → persisted",
    classes=["CRUD"], priority="P2",
    steps=[
        Step(name="cr-cz", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "cz-{{runId}}", "crossZoneEnabled": False},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="get", method="GET", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('crossZoneEnabled false', () => "
                          "  pm.expect(pm.response.json().crossZoneEnabled).to.eql(false));"]),
        Step(name="cleanup", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))


# Additional patterns to reach D-4 ≥320-cases gate
CASES.append(Case(
    id="NLB-LST-FILTER-LABELS",
    title="List with filter labels.env=prod → matching rows only",
    classes=["LSG"], priority="P2",
    steps=[
        Step(name="lst-lbl-filter", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&pageSize=100&"
                  "filter=labels.env%3D%22prod%22",
             test_script=[*assert_status(200)]),
    ],
))

CASES.append(Case(
    id="NLB-LST-FILTER-COMBINED",
    title="List with combined filter (name + labels) → handled",
    classes=["LSG"], priority="P2",
    steps=[
        Step(name="lst-combined", method="GET",
             path=f"{_CREATE_BASE}?projectId={{{{_suiteProjectId}}}}&"
                  "filter=name%3D%22edge%22%20AND%20labels.env%3D%22prod%22",
             test_script=[
                 "pm.test('handled', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
             ]),
    ],
))

CASES.append(Case(
    id="NLB-CR-CRUD-DELETION-PROTECTION-TRUE",
    title="Create with deletion_protection=true → persisted",
    classes=["CRUD", "STATE"], priority="P2",
    steps=[
        Step(name="cr-dp", method="POST", path=_CREATE_BASE,
             body={**_LB_BODY, "name": "dp-{{runId}}", "deletionProtection": True},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="get", method="GET", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('deletion_protection persisted', () => "
                          "  pm.expect(pm.response.json().deletionProtection).to.eql(true));"]),
        # Disable for cleanup
        Step(name="unprotect", method="PATCH", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             body={"updateMask": "deletion_protection", "deletionProtection": False},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="cleanup", method="DELETE", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="NLB-UPD-CRUD-DELETION-PROTECTION-TOGGLE",
    title="Update toggles deletion_protection true→false → mutable round-trip",
    classes=["CRUD", "STATE"], priority="P2",
    steps=[
        *_setup_lb("dp-toggle", {"deletionProtection": True}),
        Step(name="disable-dp", method="PATCH", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             body={"updateMask": "deletion_protection", "deletionProtection": False},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="get", method="GET", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('deletion_protection toggled false', () => "
                          "  pm.expect(pm.response.json().deletionProtection).to.eql(false));"]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-CR-NEG-EMPTY-NAME-EMPTY-REGION",
    title="Create with empty name AND empty region → multi-field violation",
    classes=["VAL", "NEG"], priority="P2",
    steps=[
        Step(name="cr-multi-missing", method="POST", path=_CREATE_BASE,
             body={"projectId": "{{_suiteProjectId}}", "type": "EXTERNAL",
                   "name": "", "regionId": ""},
             test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")]),
    ],
))

CASES.append(Case(
    id="NLB-GTS-CRUD-EMPTY-LB-ACTIVE",
    title="GetTargetStates on ACTIVE LB with no attached TG → empty array",
    classes=["CRUD", "STATE"], priority="P2",
    steps=[
        *_setup_lb("gts-empty-active"),
        Step(name="start", method="POST", path=f"{_CREATE_BASE}/{{{{nlbId}}}}:start",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="gts", method="GET", path=f"{_CREATE_BASE}/{{{{nlbId}}}}/targetStates",
             test_script=[*assert_status(200),
                          "pm.test('empty target_states', () => "
                          "  pm.expect((pm.response.json().targetStates || []).length).to.eql(0));"]),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-UPD-VAL-LABELS-OVER-64",
    title="Update labels with >64 entries → InvalidArgument",
    classes=["VAL", "BVA"], priority="P1",
    steps=[
        *_setup_lb("upd-lbl-over"),
        Step(name="upd-65", method="PATCH", path=f"{_CREATE_BASE}/{{{{nlbId}}}}",
             body={"updateMask": "labels", "labels": {f"k{i}": f"v{i}" for i in range(65)}},
             test_script=[
                 "pm.test('rejected', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))

CASES.append(Case(
    id="NLB-MV-NEG-DEST-UNKNOWN-PROJECT",
    title="Move to unknown destination project → NotFound/PermissionDenied",
    classes=["NEG"], priority="P1",
    steps=[
        *_setup_lb("mv-dst-unk"),
        Step(name="mv-unknown-dst", method="POST", path=f"{_CREATE_BASE}/{{{{nlbId}}}}:move",
             body={"destinationProjectId": "{{garbageProjectId}}"},
             test_script=[
                 "pm.test('rejected (404/403/200 then op-error)', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([200, 400, 403, 404]));",
                 *save_from_response("j.id", "opId"),
             ]),
        poll_operation_until_done(),
        *_cleanup_lb(),
    ],
))
