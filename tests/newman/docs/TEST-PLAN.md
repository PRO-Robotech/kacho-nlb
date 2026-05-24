# TEST-PLAN — RPC × class coverage matrix (KAC-NLB)

This is the **normative coverage matrix** for the kacho-nlb newman regression
suite. Each cell with a tick (✅) is covered by at least one case in
`tests/newman/cases/*.py`; case-ids are listed in `CASES-INDEX.md`. The
production-readiness gate (acceptance D-4 — see sub-phase-4.0-nlb-acceptance.md
§17) requires 100% of cells marked **REQUIRED** to be ✅ and green in newman.

> Reading the matrix: each row is one RPC, each column is one test class
> (TAXONOMY.md §1). Empty cells mean "not applicable" — e.g. `Get` has no
> Update semantics so the IDEM column for `Get` is naturally blank.

## Legend
- ✅ — covered (one or more cases exist)
- ⚪ — not applicable for this RPC (semantically meaningless)
- ❌ — required but **not yet covered** (would block D-4)
- 🔬 — covered indirectly via integration tests (not newman)

---

## 1. NetworkLoadBalancerService (12 RPC)

| RPC | CRUD | VAL | NEG | BVA | CONF | STATE | IDEM | LSG | AZD |
|---|---|---|---|---|---|---|---|---|---|
| `Get` | ✅ | ⚪ | ✅ | ⚪ | ⚪ | ⚪ | ⚪ | ✅ | ✅ |
| `List` | ✅ | ✅ | ✅ | ✅ | ⚪ | ⚪ | ✅ | ✅ | ✅ |
| `Create` | ✅ | ✅ | ✅ | ✅ | ✅ | ⚪ | ✅ | ⚪ | ✅ |
| `Update` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ⚪ | ⚪ | ✅ |
| `Delete` | ✅ | ⚪ | ✅ | ⚪ | ✅ | ✅ | ⚪ | ⚪ | ✅ |
| `Start` | ✅ | ⚪ | ✅ | ⚪ | ⚪ | ✅ | ⚪ | ⚪ | ✅ |
| `Stop` | ✅ | ⚪ | ✅ | ⚪ | ⚪ | ✅ | ⚪ | ⚪ | ✅ |
| `Move` | ✅ | ✅ | ✅ | ⚪ | ⚪ | ✅ | ✅ | ⚪ | ✅ |
| `AttachTargetGroup` | ✅ | ✅ | ✅ | ✅ | ⚪ | ✅ | ✅ | ⚪ | ✅ |
| `DetachTargetGroup` | ✅ | ⚪ | ✅ | ⚪ | ⚪ | ✅ | ⚪ | ⚪ | ✅ |
| `GetTargetStates` | ✅ | ⚪ | ✅ | ⚪ | ⚪ | ✅ | ⚪ | ⚪ | ✅ |
| `ListOperations` | ✅ | ⚪ | ✅ | ⚪ | ⚪ | ⚪ | ⚪ | ✅ | ✅ |

## 2. ListenerService (6 RPC)

| RPC | CRUD | VAL | NEG | BVA | CONF | STATE | IDEM | LSG | AZD |
|---|---|---|---|---|---|---|---|---|---|
| `Get` | ✅ | ⚪ | ✅ | ⚪ | ⚪ | ⚪ | ⚪ | ⚪ | ✅ |
| `List` | ✅ | ✅ | ✅ | ✅ | ⚪ | ⚪ | ⚪ | ✅ | ✅ |
| `Create` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ⚪ | ⚪ | ✅ |
| `Update` | ✅ | ✅ | ✅ | ⚪ | ⚪ | ✅ | ⚪ | ⚪ | ✅ |
| `Delete` | ✅ | ⚪ | ✅ | ⚪ | ⚪ | ✅ | ⚪ | ⚪ | ✅ |
| `ListOperations` | ✅ | ⚪ | ✅ | ⚪ | ⚪ | ⚪ | ⚪ | ✅ | ✅ |

## 3. TargetGroupService (9 RPC)

| RPC | CRUD | VAL | NEG | BVA | CONF | STATE | IDEM | LSG | AZD |
|---|---|---|---|---|---|---|---|---|---|
| `Get` | ✅ | ⚪ | ✅ | ⚪ | ⚪ | ⚪ | ⚪ | ⚪ | ✅ |
| `List` | ✅ | ✅ | ✅ | ✅ | ⚪ | ⚪ | ⚪ | ✅ | ✅ |
| `Create` | ✅ | ✅ | ✅ | ✅ | ✅ | ⚪ | ⚪ | ⚪ | ✅ |
| `Update` | ✅ | ✅ | ✅ | ⚪ | ⚪ | ✅ | ⚪ | ⚪ | ✅ |
| `Delete` | ✅ | ⚪ | ✅ | ⚪ | ✅ | ✅ | ⚪ | ⚪ | ✅ |
| `Move` | ✅ | ✅ | ✅ | ⚪ | ⚪ | ✅ | ✅ | ⚪ | ✅ |
| `AddTargets` | ✅ | ✅ | ✅ | ✅ | ⚪ | ✅ | ✅ | ⚪ | ✅ |
| `RemoveTargets` | ✅ | ⚪ | ⚪ | ⚪ | ⚪ | ✅ | ✅ | ⚪ | ✅ |
| `ListOperations` | ✅ | ⚪ | ✅ | ⚪ | ⚪ | ⚪ | ⚪ | ✅ | ✅ |

## 4. OperationService (3 RPC)

| RPC | CRUD | VAL | NEG | BVA | CONF | STATE | IDEM | LSG | AZD |
|---|---|---|---|---|---|---|---|---|---|
| `Get` | ✅ | ⚪ | ✅ | ⚪ | ⚪ | ⚪ | ⚪ | ⚪ | ✅ |
| `List` | ✅ | ⚪ | ✅ | ⚪ | ⚪ | ⚪ | ⚪ | ✅ | ✅ |
| `Cancel` | ⚪ | ⚪ | ✅ | ⚪ | ⚪ | ✅ | ⚪ | ⚪ | ✅ |

## 5. InternalResourceLifecycleService

Internal-only (cluster-internal port 9091, not via api-gateway public mux).
Not exercised through newman; covered by integration tests + mTLS-restriction
AZD case (`AZD-LIFECYCLE-INTERNAL-MTLS-ONLY`).

---

## 6. Coverage of design-level invariants

| Invariant (design §) | Newman coverage |
|---|---|
| `attached_target_groups` ON CONFLICT idempotent (§3.2) | `NLB-ATT-IDEM-REPEAT-OK`, `NLB-ATT-IDEM-PRIORITY-UPDATE` |
| `targets` partial UNIQUE NULLS NOT DISTINCT × 4 identity types (§4.3) | `TGT-ADD-IDEM-DUP-INSTANCE`, `TGT-ADD-IDEM-DUP-IP-REF`, `TGT-ADD-IDEM-DUP-EXTERNAL-IP` |
| 4-way Target identity exactly-one CHECK (§4.3) | `TGR-CR-VAL-TARGET-NO-IDENTITY`, `TGR-CR-VAL-TARGET-MULTIPLE-IDENTITY` |
| Bogon-rejection list (§4.3 / §3.5) | `TGR-CR-VAL-TARGET-BOGON-*` (5 variants), `TGT-ADD-VAL-BOGON-LOOPBACK` |
| `listeners` UNIQUE `(load_balancer_id, port, protocol)` (§3.3) | `LST-CR-CONF-DUP-PORT-PROTO` |
| `listeners` partial UNIQUE `(region_id, allocated_address, port, protocol) WHERE status<>'DELETING'` | `LST-CR-STATE-BYO-USED` (CAS fails before INSERT) |
| Same-region constraint LB ↔ TG (§3.2) | `NLB-ATT-STATE-REGION-MISMATCH` |
| FK RESTRICT bottom-up delete order (§3.6) | `NLB-DEL-STATE-HAS-LISTENER`, `NLB-DEL-STATE-HAS-ATTACHED`, `TGR-DEL-NEG-HAS-ATTACHED-LB`, `TGR-DEL-NEG-HAS-TARGETS`, plus race-fallback `*-CONF-FK-RACE` |
| 2-phase RemoveTargets drain (§4.4) | `TGT-RM-STATE-PHASE-A-DRAINING` (immediate), `TGT-RM-STATE-PHASE-B-RUNNER` (after dereg_delay) |
| `lb_status_recompute` trigger (§3.7) | `NLB-ATT-CRUD-OK` (INACTIVE→ACTIVE), `NLB-DET-CRUD-OK` (back to INACTIVE), `LST-DEL-CRUD-AUTO-VIP-FREE` (recompute on listener loss) |
| Move denorm sync of listener.project_id (§4.7) | `NLB-MV-CRUD-OK` |
| VIP compensation on Create-fail (§4.5) | `LST-CR-CONF-VIP-COMPENSATION` |
| BYO `SetReference` atomic CAS (§4.5) | `LST-CR-STATE-BYO-USED`, `LST-CR-CRUD-BYO`, `LST-DEL-CRUD-BYO-CLEAR-REF` |
| xmin OCC on Update (§3.1, запрет #10) | `NLB-UPD-CONF-OCC-RACE` |
| Permission catalog completeness (§6.2) | `AZD-PERMISSION-CATALOG-COMPLETE` (explicit 30-strings enumeration) |
| FGA fail-closed for all failure modes (§15) | `AZD-FGA-UNAVAILABLE-FAIL-CLOSED`, `AZD-NLB-CR-ANONYMOUS-UNAUTH` |
| D-11 sync creator-tuple (§6.3) | `AZD-OWNER-RELATION-CREATOR` |
| D-13 stream cleanup (§5.2) | `AZD-LIFECYCLE-DELETED-TUPLE-CLEANUP` |
| Cache invalidation ≤10s (NFR-9) | `AZD-CACHE-INVALIDATION-REVOKE` |
| Custom roles (3-relation cascade) (§6.4) | `AZD-CUSTOM-ROLE-OPERATOR-START`, `AZD-CUSTOM-ROLE-TARGET-MANAGER`, `AZD-CUSTOM-ROLE-UNKNOWN-PERMISSION` |
| Scope-conditional Move (src+dst Check) (§6.5) | `NLB-MV-SCOPE-DST-DENIED`, `TGR-MV-SCOPE-DST-DENIED` |
| `Operation.Cancel` owner-only (§7) | `AZD-OP-CANCEL-NON-CREATOR-DENIED` |

---

## 7. Out-of-scope (explicitly NOT covered by newman)

- Real data-plane forwarding (§16.2 — control-plane only).
- Real healthcheck probes (§16.3 — `GetTargetStates` returns deterministic ramp).
- `GlobalLoadBalancer` cross-region composite (§16.1 — reserved slot only).
- k6 baseline SLO (NFR-8) — covered separately by `tests/k6/` (out of newman scope).
- Drift-test on `permission_map.go` — Go unit test, not newman.

---

## 8. Test-execution dependencies (data fixtures)

The suite assumes the following pre-seeded fixtures in the kind-stand /
production-like environment (populated by `kacho-iam` and `kacho-vpc` fixtures
before newman runs):

| Env var | Role |
|---|---|
| `existingProjectId` | Subject jwtProjectEditorA is editor here; jwtProjectViewerA is viewer |
| `existingProjectCrossId` | For cross-project tests (Move dst, isolation) |
| `existingRegionId` | Primary region (e.g. `ru-central1`) — must exist in compute |
| `existingRegionAltId` | Secondary region — for region-mismatch tests |
| `existingZoneId` | Primary zone (e.g. `ru-central1-a`) |
| `existingSubnetId` | Subnet in `existingProjectId` + `existingRegionId` — for INTERNAL Listener + ip_ref Target |
| `existingInstanceId` | Compute Instance in `existingProjectId`+`existingRegionId` — for instance_id Target |
| `existingNicId` | NIC of that instance — for nic_id Target |
| `existingAddressId` | Free vpc.Address in same project+region — for BYO Listener |
| `existingAddressUsedId` | Address already used by another listener — for BYO-conflict |
| `existingAddressIPv6Id` | Free IPv6 Address — for ip_version mismatch |
| `existingAddressCrossId` | Address in different project — for cross-project rejection |
| `jwtProjectEditorA` | editor on existingProjectId |
| `jwtProjectViewerA` | viewer on existingProjectId |
| `jwtProjectEditorB` | editor on existingProjectCrossId only (Move scope test) |
| `jwtStranger` | No bindings at all |
| `jwtServiceAccountEditor` | SA editor on existingProjectId |
| `jwtGroupMemberEditor` | User in group with editor binding |
| `garbageRegionId` | `ru-doesnt-exist` literal |
| `garbageNlbId` | `nlbnonexistent99999999` |
| `garbageLstId` | `lstnonexistent99999999` |
| `garbageTgrId` | `tgrnonexistent99999999` |
| `garbageOpId` | `nlbnonexistent00000000` (well-formed prefix) |
| `garbageInvalidOpId` | `garbage-id-no-prefix` |

The kind-stand `setup.sh` allocates these and writes `kind-stand.postman_environment.json`;
local development uses `local.postman_environment.json` with the same shape.
