# Newman regression — taxonomy (KAC-NLB)

This document defines the **case-id naming convention** for the kacho-nlb newman
regression suite. The taxonomy mirrors `kacho-vpc/tests/newman/docs/TAXONOMY.md`
to keep cross-service cognitive load minimal.

## Case-id grammar

```
<DOMAIN>-<KIND>-<CLASS>-<DETAIL>
```

| Part | Allowed values |
|---|---|
| `DOMAIN` | `NLB` / `LST` / `TGR` / `TGT` / `OP` / `AZD` / `XRES` |
| `KIND` | RPC- or operation-shorthand: `CR` (Create), `GET`, `LST` (List), `UPD` (Update), `DEL` (Delete), `MV` (Move), `START`, `STOP`, `ATT` (AttachTargetGroup), `DET` (DetachTargetGroup), `GTS` (GetTargetStates), `ADD` (AddTargets), `RM` (RemoveTargets), `LOPS` (ListOperations), `CANCEL`. Also `METHOD` (HTTP verb tests), `HEADERS` (Content-Type etc.), `LIFECYCLE` (cross-RPC). |
| `CLASS` | Primary test class — see §1. |
| `DETAIL` | Short, kebab/upper-snake mnemonic for the specific scenario (e.g. `OK`, `DUP-NAME`, `MAX-63`, `PERM-DENIED`, `RACE-FK`, `BOGON-LOOPBACK`, …). |

Example case-ids:

| Case-id | Reads as |
|---|---|
| `NLB-CR-CRUD-OK` | NetworkLoadBalancer · Create · happy path |
| `NLB-CR-VAL-NAME-REGEX` | NetworkLoadBalancer · Create · validation · name regex |
| `NLB-UPD-STATE-IMMUTABLE-TYPE` | NetworkLoadBalancer · Update · state precondition · type is immutable |
| `LST-CR-CRUD-AUTO-VIP` | Listener · Create · happy path · auto VIP allocation |
| `LST-CR-STATE-BYO-USED` | Listener · Create · precondition · BYO address already used |
| `TGR-DEL-NEG-HAS-ATTACHED-LB` | TargetGroup · Delete · negative · has attached LB |
| `TGT-ADD-VAL-BOGON-LOOPBACK` | Targets · AddTargets · validation · bogon (loopback) external_ip |
| `TGT-RM-STATE-PHASE-A-DRAINING` | Targets · RemoveTargets · state · Phase A immediate DRAINING-mark |
| `OP-GET-NEG-NF-INVALID-PREFIX` | Operation · Get · negative not-found · invalid id prefix |
| `AZD-NLB-CR-VIEWER-DENIED` | Authz · NLB.Create · viewer is denied |
| `XRES-FULL-LIFECYCLE-E2E` | Cross-resource · full end-to-end lifecycle |

The `DETAIL` segment is intentionally informal — uniqueness is enforced by
`validate-cases.py`, not by a closed enum.

## 1. Test classes (`CLASS` segment)

| Class | Meaning | Typical assertion shape |
|---|---|---|
| **CRUD** | Basic happy paths — Create/Read/Update/Delete with valid input | 200 + Operation envelope + poll done + Get reflects state |
| **VAL** | Input validation (regex, enum, struct, oneof, range) | 400 + grpc code 3 INVALID_ARGUMENT + optional `BadRequest.field_violations` |
| **NEG** | Negative outcomes that are not pure validation: NotFound, Unavailable peer, missing resource | 404/503 + grpc code 5/14 + message |
| **BVA** | Boundary value analysis — min, max, off-by-one (length, port, threshold, weight, page-size) | mix of 200 (in-range) and 400 (out-of-range) |
| **CONF** | Concurrency / race / OCC / atomic-CAS / FK fallback | exactly-one-success deterministic outcome |
| **STATE** | State-transition preconditions (Start when ACTIVE → FailedPrecondition; immutable fields; etc.) | 400/409 + grpc code 3/9 + verbatim text |
| **IDEM** | Idempotency on retry — ON CONFLICT DO NOTHING / DO UPDATE | repeat operation = no duplicate state |
| **LSG** | List / selector / Get / filter / pagination | 200 + array semantics + page_token roundtrip |
| **AZD** | Authz deny + grant edges (FGA Check), per-RPC + lifecycle | 403 + grpc code 7 PERMISSION_DENIED + verbatim permission string |
| **PAGE** | Pagination-specific (page_token, next_page_token, pageSize edges) | 200 + token format |

A single case usually maps to one primary class plus optional secondary classes
(e.g. `LST-CR-VAL-BVA-PORT-ZERO` is both VAL and BVA). The case's `classes=[]`
list captures all of them; the primary class is the third segment of the case-id.

## 2. Priorities

| Priority | Meaning | Typical scope |
|---|---|---|
| **P0** | Blocker — failure breaks core promise of the API (NLB-CR-CRUD-OK, AZD-NLB-CR-VIEWER-DENIED, FGA fail-closed) |
| **P1** | High — per-RPC happy path, top-level NEG/VAL, single-RPC IDEM/CONF |
| **P2** | Medium — BVA edges, label/description boundary, multi-mask Update |
| **P3** | Low — HTTP-method semantics, Content-Type lenience, defensive transcode-error |

Priorities are advisory — none of them block CI green. They drive which subset
runs in PR-time vs nightly full sweep.

## 3. Folder layout in collections

Each case becomes a Postman **folder**. Each step inside the case becomes a
Postman **request** (with optional pre-request + test scripts). One newman
collection per case-file:

```
tests/newman/cases/load-balancer.py      → collections/load-balancer.postman_collection.json
tests/newman/cases/listener.py           → collections/listener.postman_collection.json
tests/newman/cases/target-group.py       → collections/target-group.postman_collection.json
tests/newman/cases/targets.py            → collections/targets.postman_collection.json
tests/newman/cases/operation.py          → collections/operation.postman_collection.json
tests/newman/cases/authz-deny.py         → collections/authz-deny.postman_collection.json
```

`cases/_helpers.py` is **not** a service module (prefixed with `_`) — gen.py
skips it. It only exists for shared imports inside case modules; today all
shared blocks live in `gen.py` and are auto-injected into each module's
namespace via `load_cases_module`. The `_helpers.py` stub is reserved for
future per-domain extraction if blocks grow large.

## 4. Folder isolation contract

- Each case is self-contained: setup + assert + cleanup live in one folder.
- All resource names use the `{{runId}}` suffix template (`gw-edge-{{runId}}`,
  `tg-backend-{{runId}}`, …) so parallel runs / multi-suite sweeps never collide
  on UNIQUE(project_id,name).
- Pre-allocated `existingProjectId` / `existingProjectCrossId` /
  `existingRegionId` / `existingRegionAltId` come from the environment file —
  the suite never creates Account / Project / Region resources.
- Cleanup steps in each case carry no assertions (DELETE is best-effort);
  they exist purely to keep the project tidy for the next run.

## 5. Compatibility with `run-incremental.sh`

`scripts/run-incremental.sh` calls `newman run --folder "<case-name>"` per
case, so case folders MUST be self-contained — every prerequisite created and
deleted within the same folder. The default `run.sh` runs the entire collection
sequentially per service.
