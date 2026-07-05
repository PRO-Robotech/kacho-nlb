# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

"""Case-set для D-consumer (§11, D-40..D-47) — per-object filtered List в kacho-nlb.

Источник истины — docs/specs/rbac-rules-model-2026-acceptance.md под-фаза D
(LST-1..6, D-40..D-47); workspace issue PRO-Robotech/kacho-workspace#111.

Что проверяем (black-box через api-gateway, реальный iam + OpenFGA в стенде):

  - D-40/D-45 read==enforce (happy): авторизованный субъект (jwtProjectEditorA)
    создаёт NLB/TargetGroup и видит его в filtered List. Это RED→GREEN-пара
    D-consumer: до фикса nlb НЕ фильтровал List per-object (ScopeFiltered:true
    лишь пропускал per-RPC Check, без ListObjects-фильтра); после — List
    прогоняет id-set через iam.AuthorizeService.ListObjects(subject, action,
    "lb_*") и отдаёт пересечение. List возвращает 200 + доступные объекты;
    list-filter.enabled=true НЕ ломает List (нет 503 от broken verb).

  - D-44 no-leak (negative): well-formed-но-отсутствующий id → Get == 404
    NOT_FOUND (НЕ 403 PERMISSION_DENIED — existence не подтверждается) и объект
    отсутствует в List. read==enforce: List-видимость == Check-allow поверх тех
    же materialized tuples + scope_grant (relation viewer).

  - D-44 cross-subject no-leak: jwtStranger (нет грантов) НЕ видит чужой NLB в
    List (per-object фильтрация, не all-or-nothing leak), Get → 403/404.

Pre-conditions: те же JWT/проекты, что authz-deny.py (jwtProjectEditorA editor на
existingProjectId; jwtStranger без bindings). Требует list-filter.enabled=true на
стенде (KACHO_NLB_AUTHZ__LIST_FILTER__ENABLED).
"""

CASES = []

_NLB = "/nlb/v1/networkLoadBalancers"
_TGR = "/nlb/v1/targetGroups"


# ---------------------------------------------------------------------------
# D-40/D-45 — read==enforce happy: editor sees own NLB in (filtered) List.
# ---------------------------------------------------------------------------
CASES.append(Case(
    id="LF-NLB-LST-READ-ENFORCE-OWNER-SEES-OWN",
    title="[D-40/D-45] editor creates NLB and sees it in filtered List (read==enforce)",
    classes=["AZD", "LSG"], priority="P0",
    steps=[
        Step(name="create-own", method="POST", path=_NLB, auth="jwtProjectEditorA",
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "name": "lf-nlb-own-{{runId}}", "type": "EXTERNAL", "v4Source": {"public": {}}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "lfNlbId")]),
        poll_operation_until_done(),
        Step(name="list-own", method="GET",
             path=f"{_NLB}?projectId={{{{_suiteProjectId}}}}&pageSize=1000",
             auth="jwtProjectEditorA",
             test_script=[*assert_status(200),
                          "const lbs = pm.response.json().networkLoadBalancers || [];",
                          "pm.test('[D-45] filtered List returns 200 (not 503 from list-filter)', () => pm.expect(pm.response.code).to.eql(200));",
                          "const mine = lbs.find(x => x.id === pm.environment.get('lfNlbId'));",
                          "pm.test('[D-40] owner sees own NLB in filtered List (read==enforce)', () => pm.expect(mine, JSON.stringify(lbs.map(i=>i.id))).to.be.an('object'));"]),
        Step(name="del-own", method="DELETE", path=f"{_NLB}/{{{{lfNlbId}}}}",
             auth="jwtProjectEditorA",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))


# ---------------------------------------------------------------------------
# D-40/D-45 — same for TargetGroup (per-арм для всех ресурсов nlb).
# ---------------------------------------------------------------------------
CASES.append(Case(
    id="LF-TGR-LST-READ-ENFORCE-OWNER-SEES-OWN",
    title="[D-40/D-45] editor creates TargetGroup and sees it in filtered List",
    classes=["AZD", "LSG"], priority="P0",
    steps=[
        Step(name="create-tg", method="POST", path=_TGR, auth="jwtProjectEditorA",
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "name": "lf-tg-own-{{runId}}",
                   "healthCheck": {"name": "hc", "tcp": {"port": 80}}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.targetGroupId", "lfTgId")]),
        poll_operation_until_done(),
        Step(name="list-tg", method="GET",
             path=f"{_TGR}?projectId={{{{_suiteProjectId}}}}&pageSize=1000",
             auth="jwtProjectEditorA",
             test_script=[*assert_status(200),
                          "const tgs = pm.response.json().targetGroups || [];",
                          "pm.test('[D-45] filtered TG List returns 200', () => pm.expect(pm.response.code).to.eql(200));",
                          "const mine = tgs.find(x => x.id === pm.environment.get('lfTgId'));",
                          "pm.test('[D-40] owner sees own TG in filtered List', () => pm.expect(mine, JSON.stringify(tgs.map(i=>i.id))).to.be.an('object'));"]),
        Step(name="del-tg", method="DELETE", path=f"{_TGR}/{{{{lfTgId}}}}",
             auth="jwtProjectEditorA",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))


# ---------------------------------------------------------------------------
# D-44 — no-leak: well-formed-but-absent id → 404 (NOT 403).
# ---------------------------------------------------------------------------
CASES.append(Case(
    id="LF-NLB-GET-NOLEAK-404-NOT-403",
    title="[D-44] editor Get well-formed-but-absent NLB id → 404 NOT_FOUND (no-leak, not 403)",
    classes=["AZD", "NEG", "LSG"], priority="P0",
    steps=[
        Step(name="get-absent", method="GET", path=f"{_NLB}/{{{{garbageNlbId}}}}",
             auth="jwtProjectEditorA",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND"),
                          "pm.test('[D-44] no-leak: NOT_FOUND, not PERMISSION_DENIED', () => pm.expect(pm.response.json().code).to.not.eql(7));"]),
    ],
))


# ---------------------------------------------------------------------------
# D-44 — cross-subject no-leak: stranger does not see editor's NLB in List.
# ---------------------------------------------------------------------------
CASES.append(Case(
    id="LF-NLB-LST-STRANGER-NO-LEAK",
    title="[D-44] stranger List → editor's NLB not visible (per-object isolation)",
    classes=["AZD", "NEG", "LSG"], priority="P1",
    steps=[
        Step(name="create-a", method="POST", path=_NLB, auth="jwtProjectEditorA",
             body={"projectId": "{{_suiteProjectId}}", "regionId": "{{_suiteRegionId}}",
                   "name": "lf-nlb-xleak-{{runId}}", "type": "EXTERNAL", "v4Source": {"public": {}}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkLoadBalancerId", "lfXleakNlbId")]),
        poll_operation_until_done(),
        # Stranger lists the same project → must NOT see editor's NLB (filter
        # either empties the list or 403s before any row; either way no leak).
        Step(name="list-stranger", method="GET",
             path=f"{_NLB}?projectId={{{{_suiteProjectId}}}}&pageSize=1000",
             auth="jwtStranger",
             test_script=[
                 "pm.test('[D-44] stranger List does not leak NLB (403 or filtered-out)', () => {",
                 "  if (pm.response.code === 200) {",
                 "    const ids = (pm.response.json().networkLoadBalancers || []).map(x => x.id);",
                 "    pm.expect(ids).to.not.include(pm.environment.get('lfXleakNlbId'));",
                 "  } else {",
                 "    pm.expect(pm.response.code).to.be.oneOf([403, 404]);",
                 "  }",
                 "});"]),
        Step(name="del-a", method="DELETE", path=f"{_NLB}/{{{{lfXleakNlbId}}}}",
             auth="jwtProjectEditorA",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))
