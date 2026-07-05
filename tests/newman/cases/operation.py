# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

"""OperationService cases (OP-*) — kacho-nlb opsproxy through api-gateway."""

CASES = []


# -- OP-GET-CRUD-IN-FLIGHT — happy path: Get returns in-flight op then polls to done
CASES.append(Case(
    id="OP-GET-CRUD-IN-FLIGHT",
    title="Get just-created operation eventually polls to done=true",
    classes=["CRUD"], priority="P0",
    steps=[
        Step(name="trigger-create", method="POST", path="/nlb/v1/networkLoadBalancers",
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "name": "opget-inflight-{{runId}}", "type": "EXTERNAL", "v4Source": {"public": {}}},
             test_script=[*assert_status(200),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId"),
                          "pm.test('done initially false or true', () => {"
                          "  const j = pm.response.json();"
                          "  pm.expect(typeof j.done).to.eql('boolean');"
                          "});"]),
        Step(name="get-op-immediate", method="GET", path="/operations/{{opId}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('has metadata', () => pm.expect(j.metadata).to.be.an('object'));",
                          "pm.test('id matches', () => pm.expect(j.id).to.eql(pm.environment.get('opId')));"]),
        poll_operation_until_done(),
        Step(name="cleanup", method="DELETE", path="/nlb/v1/networkLoadBalancers/{{nlbId}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))


# -- OP-GET-CRUD-COMPLETED — completed op shape
CASES.append(Case(
    id="OP-GET-CRUD-COMPLETED",
    title="Get of completed Create-LB op returns done=true with response payload",
    classes=["CRUD"], priority="P0",
    steps=[
        Step(name="create", method="POST", path="/nlb/v1/networkLoadBalancers",
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "name": "opget-done-{{runId}}", "type": "EXTERNAL", "v4Source": {"public": {}}},
             test_script=[*assert_status(200),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="get-op-done", method="GET", path="/operations/{{opId}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('done=true', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('has response or error', () => pm.expect(j.response || j.error).to.exist);",
                          "if (j.response) pm.test('metadata has networkLoadBalancerId', "
                          "  () => pm.expect(j.metadata && j.metadata.networkLoadBalancerId).to.match(/^nlb/));"]),
        Step(name="cleanup", method="DELETE", path="/nlb/v1/networkLoadBalancers/{{nlbId}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))


# -- OP-GET-NEG-NF-INVALID-PREFIX — opsproxy verbatim-YC: malformed id → InvalidArgument
CASES.append(Case(
    id="OP-GET-NEG-NF-INVALID-PREFIX",
    title="Get with malformed opId (no known prefix) → 400 INVALID_ARGUMENT 'invalid operation id'",
    classes=["NEG"], priority="P0",
    steps=[
        Step(name="get-garbage", method="GET", path="/operations/{{garbageInvalidOpId}}",
             test_script=[
                 *assert_status(400),
                 *assert_grpc_code(3, "INVALID_ARGUMENT"),
                 "pm.test('mentions invalid operation id', () => "
                 "  pm.expect(pm.response.json().message.toLowerCase()).to.include('invalid operation id'));",
             ]),
    ],
))


# -- OP-GET-NEG-NF-VALID-PREFIX — well-formed prefix but no row
CASES.append(Case(
    id="OP-GET-NEG-NF-VALID-PREFIX",
    title="Get of well-formed but missing opId → 404 NOT_FOUND",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="get-missing", method="GET", path="/operations/{{garbageOpId}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))


# -- OP-LST-CRUD-OK — list ops visible to subject in project
CASES.append(Case(
    id="OP-LST-CRUD-OK",
    title="List operations in project — returns array (may be empty)",
    classes=["CRUD", "LSG"], priority="P1",
    steps=[
        Step(name="list-ops", method="GET",
             path="/operations?projectId={{_suiteProjectId}}&pageSize=10",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('operations array', () => "
                          "  pm.expect(j.operations || j.items || []).to.be.an('array'));"]),
    ],
))


# -- OP-CANCEL-STATE-ALREADY-DONE — Cancel on already-done op → FailedPrecondition
CASES.append(Case(
    id="OP-CANCEL-STATE-ALREADY-DONE",
    title="Cancel an already-done op → 400/409 FailedPrecondition 'operation is already completed'",
    classes=["STATE", "NEG"], priority="P1",
    steps=[
        Step(name="create-fast", method="POST", path="/nlb/v1/networkLoadBalancers",
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "name": "opcanc-{{runId}}", "type": "EXTERNAL", "v4Source": {"public": {}}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "nlbId")]),
        poll_operation_until_done(),
        Step(name="cancel-done", method="POST", path="/operations/{{opId}}:cancel",
             test_script=[
                 "pm.test('rejected with FailedPrecondition', () => "
                 "  pm.expect(pm.response.code).to.be.oneOf([400, 409]));",
                 "if (pm.response.code === 400 || pm.response.code === 409) {",
                 "  pm.test('grpc code 9 (FAILED_PRECONDITION)', () => "
                 "    pm.expect(pm.response.json().code).to.eql(9));",
                 "  pm.test('mentions already completed', () => "
                 "    pm.expect((pm.response.json().message||'').toLowerCase()).to.include('already'));",
                 "}",
             ]),
        Step(name="cleanup", method="DELETE", path="/nlb/v1/networkLoadBalancers/{{nlbId}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))
