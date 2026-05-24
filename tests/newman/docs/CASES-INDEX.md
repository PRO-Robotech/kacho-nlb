# CASES-INDEX — catalogue of unique patterns (KAC-NLB)

This catalogue enumerates every **unique case pattern** present in the kacho-nlb
newman suite. `validate-cases.py` enforces that **every** case-id is either
literally listed here OR matches one of the `*-<SUFFIX>` suffix patterns below
OR carries a `# index: <pattern-ref>` tag in the case-file (= "instance of an
existing pattern, no separate catalogue entry needed").

> Format: `<pattern-or-id>` — `<classes>` — `<priority>` — `<one-line meaning>`
> Each pattern includes its `Verifies REQ-*` mapping when one exists in
> `PRODUCT-REQUIREMENTS.md`.

---

## 1. NetworkLoadBalancer (NLB-*) — 12 RPC × ~5 classes

### CRUD-OK happy paths
- `*-CR-CRUD-OK` — CRUD/P0 — Create + poll + Get (Verifies REQ-NLB-CR-01)
- `*-CR-CRUD-INTERNAL` — CRUD/P1 — Create with type=INTERNAL (Verifies REQ-NLB-CR-02)
- `*-GET-CRUD-OK` — CRUD/P0 — Get an existing resource (Verifies REQ-NLB-GET-01)
- `*-LST-CRUD-OK` — CRUD,LSG/P1 — List in project returns array (Verifies REQ-NLB-LST-01)
- `*-LST-CRUD-EMPTY-PROJECT` — CRUD,LSG/P2 — List on empty project → `[]`
- `*-UPD-CRUD-OK` — CRUD/P1 — Update mutable fields via mask (Verifies REQ-NLB-UPD-01)
- `*-UPD-CRUD-MULTI-MASK` — CRUD,STATE/P2 — Update multi-field mask
- `*-DEL-CRUD-OK` — CRUD/P1 — Delete a clean resource (Verifies REQ-NLB-DEL-01)
- `*-LOPS-CRUD-OK` — CRUD,LSG/P2 — ListOperations returns ordered history
- `*-START-CRUD-OK` — CRUD,STATE/P1 — Start from INACTIVE (Verifies REQ-NLB-LIFE-01)
- `*-STOP-CRUD-OK` — CRUD,STATE/P1 — Stop from ACTIVE/INACTIVE (Verifies REQ-NLB-LIFE-02)
- `*-MV-CRUD-OK` — CRUD,STATE/P1 — Move cross-project (Verifies REQ-NLB-MV-01)
- `*-ATT-CRUD-OK` — CRUD,IDEM/P1 — AttachTargetGroup happy (Verifies REQ-NLB-ATT-01)
- `*-DET-CRUD-OK` — CRUD/P1 — DetachTargetGroup happy (Verifies REQ-NLB-DET-01)
- `*-GTS-CRUD-EMPTY` — CRUD/P1 — GetTargetStates on LB with no attached TG → `[]` (Verifies REQ-NLB-GTS-01)
- `*-GTS-STATE-LB-STOPPED` — STATE/P2 — GetTargetStates returns INACTIVE for all when LB STOPPED

### Validation (VAL)
- `*-CR-VAL-NAME-REGEX` — VAL/P1 — invalid name regex → 400 INVALID_ARGUMENT (Verifies REQ-NLB-CR-VAL-NAME)
- `*-CR-VAL-NAME-UNDERSCORE` — VAL/P1 — `_` not allowed in name
- `*-CR-VAL-NAME-UPPERCASE` — VAL/P1 — uppercase rejected (per LbName domain newtype)
- `*-CR-VAL-NAME-EMPTY` — VAL/P0 — empty name → required-field violation
- `*-CR-VAL-NAME-NULL` — VAL/P2 — null name → validation
- `*-CR-VAL-MISSING-REGION` — VAL/P0 — region_id required
- `*-CR-VAL-MISSING-PROJECT` — VAL/P0 — project_id required
- `*-CR-VAL-INVALID-TYPE` — VAL/P1 — unknown type enum
- `*-CR-VAL-INVALID-AFFINITY` — VAL/P2 — unknown session_affinity enum
- `*-CR-VAL-LABELS-OVER-64` — VAL,BVA/P1 — >64 label pairs → 23514 → InvalidArgument (Verifies REQ-DB-LABEL-CHECK)
- `*-CR-VAL-LABELS-UPPERCASE-KEY` — VAL/P1 — uppercase label key rejected
- `*-CR-VAL-LABELS-INVALID-KEY-CHAR` — VAL/P1 — `!`/space in label key
- `*-CR-VAL-DESC-OVER-256` — VAL,BVA/P2 — description >256 chars
- `*-CR-VAL-EMPTY-BODY` — VAL/P2 — empty JSON body → 400
- `*-CR-VAL-MALFORMED-JSON` — VAL/P2 — invalid JSON syntax → 400/415

### Negative + cross-service NotFound
- `*-CR-NEG-REGION-UNKNOWN` — NEG/P0 — unknown region_id (cross-service NotFound) (Verifies REQ-NLB-CR-NEG-REGION)
- `*-CR-NEG-PROJECT-UNKNOWN` — NEG/P0 — unknown project_id (cross-service NotFound)
- `*-GET-NEG-NF-UNKNOWN` — NEG/P0 — unknown id → 404 NotFound (Verifies REQ-NLB-GET-NEG)
- `*-UPD-NEG-NF-UNKNOWN` — NEG/P1 — Update unknown id → 404
- `*-DEL-NEG-NF-UNKNOWN` — NEG/P1 — Delete unknown id → 404

### Boundary value (BVA)
- `*-CR-BVA-NAME-MIN-3` — BVA/P2 — name length=3 (lower bound) → OK
- `*-CR-BVA-NAME-MAX-63` — BVA/P2 — name length=63 (upper bound) → OK
- `*-CR-BVA-NAME-OVER-64` — BVA,VAL/P1 — name length=64 → InvalidArgument
- `*-CR-BVA-DESC-MAX-256` — BVA/P2 — description=256 chars → OK
- `*-LST-BVA-PAGESIZE-1` — BVA,LSG/P2 — pageSize=1 → ≤1 item
- `*-LST-BVA-PAGESIZE-ZERO` — BVA,LSG/P2 — pageSize=0 → default applied
- `*-LST-BVA-PAGESIZE-OVER-MAX` — BVA,VAL/P2 — pageSize=10000 → InvalidArgument
- `*-LST-PAGE-TOKEN-GARBAGE` — VAL,LSG/P1 — garbage page_token → InvalidArgument
- `*-LST-PAGE-ROUNDTRIP` — CRUD,LSG/P2 — pagination round-trip with next_page_token
- `*-LST-FILTER-NAME-OK` — LSG/P2 — filter by exact name returns row
- `*-LST-FILTER-MATCH` — LSG,IDEM/P2 — create + filter sees own resource
- `*-LST-FILTER-GARBAGE` — VAL/P2 — garbage filter syntax → handled (200/400)

### Conflict / concurrency (CONF)
- `*-CR-CONF-ALREADY-EXISTS` — CONF,IDEM,NEG/P1 — duplicate (project_id,name) → 409 ALREADY_EXISTS (Verifies REQ-DB-NLB-NAME-UNIQ)
- `*-CR-CONF-NF-TEXT` — CONF,NEG/P1 — verbatim "<Resource> ... not found" text matches NLB-specific shape
- `*-UPD-CONF-OCC-RACE` — CONF/P1 — concurrent Update with stale xmin → exactly one OK + one ABORTED (Verifies REQ-NLB-UPD-OCC)
- `*-DEL-CONF-FK-RACE` — CONF/P0 — concurrent attach during Delete → FAILED_PRECONDITION via FK 23503 (Verifies REQ-NLB-DEL-RACE)

### State transitions (STATE)
- `*-UPD-STATE-IMMUTABLE-TYPE` — STATE,VAL/P0 — type immutable after Create (Verifies REQ-NLB-IMMUTABLE-TYPE)
- `*-UPD-STATE-IMMUTABLE-REGION` — STATE,VAL/P0 — region_id immutable
- `*-UPD-STATE-IMMUTABLE-PROJECT` — STATE,VAL/P0 — project_id immutable (Move only)
- `*-UPD-STATE-MASK-UNKNOWN` — STATE,VAL/P1 — unknown field in mask → InvalidArgument
- `*-UPD-STATE-MASK-EMPTY` — STATE,VAL/P1 — empty mask → InvalidArgument
- `*-START-STATE-ALREADY-ACTIVE` — STATE,NEG/P1 — Start on ACTIVE → FailedPrecondition (Verifies REQ-NLB-START-NEG)
- `*-START-STATE-DELETING` — STATE,NEG/P1 — Start on DELETING → FailedPrecondition
- `*-STOP-STATE-ALREADY-STOPPED` — STATE,NEG/P1 — Stop on STOPPED → FailedPrecondition
- `*-STOP-STATE-DELETING` — STATE,NEG/P1 — Stop on DELETING → FailedPrecondition
- `*-ATT-STATE-REGION-MISMATCH` — STATE,NEG/P0 — TG in different region → FailedPrecondition (Verifies REQ-NLB-SAME-REGION)
- `*-ATT-STATE-TG-DELETING` — STATE,NEG/P1 — TG in DELETING → FailedPrecondition
- `*-ATT-VAL-PRIORITY-OVER` — VAL,BVA/P1 — priority out of [0,1000]
- `*-ATT-NEG-TG-UNKNOWN` — NEG/P1 — unknown TG id → NotFound
- `*-ATT-IDEM-REPEAT-OK` — IDEM/P1 — repeat Attach with same priority → OK (no duplicate row)
- `*-ATT-IDEM-PRIORITY-UPDATE` — IDEM,STATE/P1 — Attach with new priority → ON CONFLICT DO UPDATE
- `*-DET-NEG-NOT-ATTACHED` — NEG,STATE/P1 — Detach when no attach exists → FailedPrecondition
- `*-MV-NEG-ATTACHED-TG` — NEG,STATE/P0 — Move with attached TG → FailedPrecondition (Verifies REQ-NLB-MV-NEG)
- `*-MV-VAL-MISSING-DEST` — VAL/P1 — destinationProjectId required
- `*-MV-NEG-NF-UNKNOWN` — NEG/P1 — Move unknown id → 404
- `*-MV-IDM-SAME-PROJECT` — IDEM,NEG/P2 — Move to current project → InvalidArgument verbatim
- `*-DEL-STATE-PROTECTION` — STATE,NEG/P0 — deletion_protection=true → FailedPrecondition (Verifies REQ-NLB-DEL-PROT)
- `*-DEL-STATE-HAS-LISTENER` — STATE,NEG/P0 — Delete with listeners → FailedPrecondition (Verifies REQ-NLB-DEL-LISTENERS)
- `*-DEL-STATE-HAS-ATTACHED` — STATE,NEG/P0 — Delete with attached TG → FailedPrecondition

### HTTP method semantics
- `*-METHOD-PUT-NOT-ALLOWED` — VAL,NEG/P3 — PUT on collection → 403/404/405/501
- `*-METHOD-DELETE-LIST` — VAL,NEG/P3 — DELETE on collection → 403/404/405/501

### Lifecycle conformance
- `*-LIFECYCLE-CONF` — CRUD,CONF,STATE/P1 — full Create→Get→List-includes→Update→Get-updated→Delete→List-excludes→Get-404

---

## 2. Listener (LST-*) — 6 RPC × ~6 classes

### CRUD
- `*-CR-CRUD-AUTO-VIP` — CRUD/P0 — Create EXTERNAL with auto VIP allocation (Verifies REQ-LST-CR-AUTO-VIP)
- `*-CR-CRUD-BYO` — CRUD/P0 — Create with BYO address_id (Verifies REQ-LST-CR-BYO)
- `*-CR-CRUD-INTERNAL` — CRUD/P1 — Create INTERNAL with subnet_id (Verifies REQ-LST-CR-INTERNAL)
- `*-GET-CRUD-OK` — CRUD/P0 — Get existing listener
- `*-LST-CRUD-OK` — CRUD,LSG/P1 — List by load_balancer_id
- `*-UPD-CRUD-OK` — CRUD/P1 — Update mutable (name, proxy_protocol_v2, default_target_group_id)
- `*-DEL-CRUD-AUTO-VIP-FREE` — CRUD,STATE/P1 — Delete auto-VIP listener frees IP back to pool (Verifies REQ-LST-DEL-AUTO-FREE)
- `*-DEL-CRUD-BYO-CLEAR-REF` — CRUD,STATE/P1 — Delete BYO listener clears used_by, does NOT free
- `*-LOPS-CRUD-OK` — CRUD,LSG/P2 — ListOperations

### Validation
- `*-CR-VAL-PORT-ZERO` — VAL,BVA/P1 — port=0 → InvalidArgument
- `*-CR-VAL-PORT-OVER` — VAL,BVA/P1 — port=65536 → InvalidArgument
- `*-CR-VAL-PORT-NEGATIVE` — VAL,BVA/P2 — port=-1 → InvalidArgument
- `*-CR-VAL-UNSUPPORTED-PROTOCOL` — VAL/P1 — protocol="HTTP" → InvalidArgument (only TCP/UDP)
- `*-CR-VAL-INTERNAL-NO-SUBNET` — VAL/P0 — INTERNAL without subnet_id → InvalidArgument (Verifies REQ-LST-VAL-INTERNAL-SUBNET)
- `*-CR-VAL-NAME-REGEX` — VAL/P1 — invalid name regex
- `*-CR-BVA-PORT-MIN-1` — BVA/P2 — port=1 → OK
- `*-CR-BVA-PORT-MAX-65535` — BVA/P2 — port=65535 → OK

### Cross-service / NEG / STATE
- `*-CR-STATE-BYO-USED` — STATE,NEG/P0 — BYO address already used by another listener → FailedPrecondition (Verifies REQ-LST-BYO-USED)
- `*-CR-VAL-BYO-IP-VERSION-MISMATCH` — VAL,NEG/P1 — address ip_version mismatches listener (Verifies REQ-LST-BYO-IPV)
- `*-CR-VAL-BYO-CROSS-PROJECT` — VAL,NEG/P1 — BYO address in different project → InvalidArgument
- `*-CR-NEG-LB-UNKNOWN` — NEG/P0 — unknown load_balancer_id → NotFound
- `*-CR-CONF-DUP-PORT-PROTO` — CONF,NEG/P0 — duplicate (lb_id, port, protocol) → ALREADY_EXISTS (Verifies REQ-LST-UNIQ-PORT-PROTO)
- `*-CR-CONF-VIP-COMPENSATION` — CONF,NEG/P1 — VIP-alloc OK + INSERT fails → compensation FreeIP runs (Verifies REQ-LST-COMP-FREEIP)
- `*-UPD-STATE-IMMUTABLE-LB-ID` — STATE,VAL/P0 — load_balancer_id immutable
- `*-UPD-STATE-IMMUTABLE-PROTOCOL` — STATE,VAL/P0 — protocol immutable
- `*-UPD-STATE-IMMUTABLE-PORT` — STATE,VAL/P0 — port immutable
- `*-UPD-STATE-IMMUTABLE-IP-VERSION` — STATE,VAL/P1 — ip_version immutable
- `*-UPD-STATE-IMMUTABLE-ADDRESS-ID` — STATE,VAL/P1 — address_id immutable
- `*-UPD-STATE-DEFAULT-TG-REGION-MISMATCH` — STATE,NEG/P1 — default_target_group_id in different region → FailedPrecondition

### HTTP method semantics
- `*-METHOD-PUT-NOT-ALLOWED` — VAL,NEG/P3 — see NLB block
- `*-METHOD-DELETE-LIST` — VAL,NEG/P3 — see NLB block

---

## 3. TargetGroup (TGR-*) — 9 RPC × ~6 classes

### CRUD
- `*-CR-CRUD-OK` — CRUD/P0 — Create TG with inline targets (Verifies REQ-TGR-CR-01)
- `*-CR-CRUD-EMPTY-TARGETS` — CRUD/P2 — Create TG without targets → OK (Verifies REQ-TGR-CR-EMPTY)
- `*-GET-CRUD-OK` — CRUD/P0 — Get TG returns embedded targets[]
- `*-LST-CRUD-OK` — CRUD,LSG/P1 — List TG by project (Verifies REQ-TGR-LST-01)
- `*-LST-FILTER-REGION` — LSG/P2 — List filtered by region
- `*-UPD-CRUD-OK` — CRUD/P1 — Update mutable (name/desc/labels/health_check/dereg/slow_start)
- `*-DEL-CRUD-OK` — CRUD/P1 — Delete clean TG (Verifies REQ-TGR-DEL-01)
- `*-MV-CRUD-OK` — CRUD,STATE/P1 — Move TG cross-project
- `*-LOPS-CRUD-OK` — CRUD,LSG/P2 — ListOperations history

### Validation — health_check semantics
- `*-CR-VAL-HC-MULTIPLE-PROBES` — VAL/P0 — multiple of tcp/http/https/grpc → InvalidArgument (Verifies REQ-TGR-VAL-HC)
- `*-CR-VAL-HC-NONE-SET` — VAL/P0 — no probe type set → InvalidArgument
- `*-CR-VAL-HC-INTERVAL-ZERO` — VAL,BVA/P1 — interval="0s" → out-of-range
- `*-CR-VAL-HC-INTERVAL-OVER` — VAL,BVA/P1 — interval="601s" → out-of-range
- `*-CR-VAL-HC-THRESHOLD-LOW` — VAL,BVA/P1 — unhealthy_threshold=1 → out-of-range
- `*-CR-VAL-HC-THRESHOLD-HIGH` — VAL,BVA/P1 — unhealthy_threshold=11 → out-of-range
- `*-CR-VAL-DEREG-NEGATIVE` — VAL,BVA/P1 — deregistration_delay_seconds=-1
- `*-CR-VAL-DEREG-OVER` — VAL,BVA/P1 — deregistration_delay_seconds=3601
- `*-CR-VAL-SLOW-START-NEGATIVE` — VAL,BVA/P2 — slow_start_seconds=-1
- `*-CR-VAL-SLOW-START-OVER` — VAL,BVA/P2 — slow_start_seconds=901

### Validation — targets inline
- `*-CR-VAL-TARGET-NO-IDENTITY` — VAL/P0 — target without any oneof identity → InvalidArgument (Verifies REQ-TGT-4WAY-EXACTLY-ONE)
- `*-CR-VAL-TARGET-MULTIPLE-IDENTITY` — VAL/P0 — target with multiple oneof identities → InvalidArgument
- `*-CR-VAL-TARGET-BOGON-LOOPBACK` — VAL/P0 — external_ip=127.0.0.1 → bogon rejected (Verifies REQ-TGT-BOGON)
- `*-CR-VAL-TARGET-BOGON-UNSPEC` — VAL/P0 — external_ip=0.0.0.0 → bogon rejected
- `*-CR-VAL-TARGET-BOGON-LINKLOCAL` — VAL/P1 — external_ip=169.254.x.x → bogon rejected
- `*-CR-VAL-TARGET-BOGON-MULTICAST` — VAL/P1 — external_ip=224.0.0.0 → bogon rejected
- `*-CR-VAL-TARGET-BOGON-BROADCAST` — VAL/P1 — external_ip=255.255.255.255 → bogon rejected
- `*-CR-NEG-REGION-UNKNOWN` — NEG/P0 — unknown region_id (cross-service)

### CONF / STATE / NEG
- `*-CR-CONF-ALREADY-EXISTS` — CONF,IDEM,NEG/P1 — duplicate (project_id,name) → 409 ALREADY_EXISTS (Verifies REQ-DB-TGR-NAME-UNIQ)
- `*-UPD-STATE-IMMUTABLE-PROJECT` — STATE,VAL/P0 — project_id immutable
- `*-UPD-STATE-IMMUTABLE-REGION` — STATE,VAL/P0 — region_id immutable
- `*-UPD-VAL-TARGETS-VIA-MASK` — VAL/P0 — update_mask=["targets"] rejected → use AddTargets/RemoveTargets
- `*-DEL-NEG-HAS-ATTACHED-LB` — NEG,STATE/P0 — Delete with attached LB → FailedPrecondition (Verifies REQ-TGR-DEL-ATTACHED)
- `*-DEL-NEG-HAS-TARGETS` — NEG,STATE/P0 — Delete with targets → FailedPrecondition (Verifies REQ-TGR-DEL-TARGETS)
- `*-DEL-CONF-FK-RACE` — CONF/P1 — concurrent AddTargets during Delete → FK 23503 → FailedPrecondition
- `*-MV-NEG-ATTACHED-LB` — NEG,STATE/P0 — Move with attached LB → FailedPrecondition
- `*-MV-VAL-MISSING-DEST` — VAL/P1 — destinationProjectId required
- `*-MV-NEG-NF-UNKNOWN` — NEG/P1 — Move unknown id → 404
- `*-GET-NEG-NF-UNKNOWN` — NEG/P0 — Get unknown id → 404

### HTTP method semantics
- `*-METHOD-PUT-NOT-ALLOWED` — VAL,NEG/P3
- `*-METHOD-DELETE-LIST` — VAL,NEG/P3

---

## 4. Targets (TGT-*) — 2 RPC (AddTargets/RemoveTargets) × ~5 classes

### AddTargets — 4-way identity matrix
- `*-ADD-CRUD-INSTANCE-ID` — CRUD/P0 — variant 1: instance_id (Verifies REQ-TGT-4WAY-INSTANCE)
- `*-ADD-CRUD-NIC-ID` — CRUD/P0 — variant 2: nic_id
- `*-ADD-CRUD-IP-REF` — CRUD/P0 — variant 3: ip_ref{subnet_id, address}
- `*-ADD-CRUD-EXTERNAL-IP` — CRUD/P0 — variant 4: external_ip{address}
- `*-ADD-CRUD-MIXED-IDENTITIES` — CRUD/P1 — 4 variants in one AddTargets call

### Validation
- `*-ADD-VAL-EMPTY-LIST` — VAL/P1 — targets=[] → InvalidArgument
- `*-ADD-VAL-WEIGHT-NEGATIVE` — VAL,BVA/P1 — weight=-1 → InvalidArgument
- `*-ADD-VAL-WEIGHT-OVER` — VAL,BVA/P1 — weight=1001 → InvalidArgument
- `*-ADD-BVA-WEIGHT-MIN-0` — BVA/P2 — weight=0 → OK (drain semantics)
- `*-ADD-BVA-WEIGHT-MAX-1000` — BVA/P2 — weight=1000 → OK
- `*-ADD-VAL-BOGON-LOOPBACK` — VAL/P0 — external_ip=127.0.0.1 → bogon rejected
- `*-ADD-VAL-IP-REF-NOT-IN-SUBNET` — VAL/P0 — ip_ref outside subnet CIDR (Verifies REQ-TGT-IPREF-CIDR)

### Peer validation
- `*-ADD-NEG-INSTANCE-UNKNOWN` — NEG/P1 — unknown instance_id → InvalidArgument (Verifies REQ-TGT-PEER-INSTANCE)
- `*-ADD-NEG-NIC-UNKNOWN` — NEG/P1 — unknown nic_id → InvalidArgument
- `*-ADD-NEG-SUBNET-UNKNOWN` — NEG/P1 — unknown subnet_id → InvalidArgument
- `*-ADD-NEG-INSTANCE-REGION-MISMATCH` — NEG/P0 — instance in different region (Verifies REQ-TGT-PEER-REGION)
- `*-ADD-NEG-NIC-REGION-MISMATCH` — NEG/P1 — NIC in different region
- `*-ADD-NEG-SUBNET-REGION-MISMATCH` — NEG/P1 — subnet in different region

### IDEM / STATE
- `*-ADD-IDEM-DUP-INSTANCE` — IDEM/P1 — same instance_id twice → ON CONFLICT DO NOTHING (Verifies REQ-TGT-IDEM-ID)
- `*-ADD-IDEM-DUP-IP-REF` — IDEM/P1 — same ip_ref twice → no duplicate row
- `*-ADD-IDEM-DUP-EXTERNAL-IP` — IDEM/P2 — same external_ip twice → no duplicate
- `*-ADD-IDEM-PROMOTE-DRAINING` — IDEM,STATE/P1 — re-add DRAINING target → re-promoted ACTIVE
- `*-ADD-STATE-TG-DELETING` — STATE,NEG/P1 — TG in DELETING → FailedPrecondition

### RemoveTargets — 2-phase drain
- `*-RM-STATE-PHASE-A-DRAINING` — STATE/P0 — Phase A DRAINING-mark + drain_started_at set (Verifies REQ-TGT-RM-PHASE-A)
- `*-RM-IDEM-NOT-PRESENT` — IDEM/P1 — RemoveTargets for absent identity → no-op (Verifies REQ-TGT-RM-IDEM)
- `*-RM-STATE-PHASE-B-RUNNER` — STATE/P1 — after dereg_delay, runner DELETEs row (Verifies REQ-TGT-RM-PHASE-B)

### HTTP method semantics
- `*-METHOD-PUT-NOT-ALLOWED` — VAL,NEG/P3
- `*-METHOD-DELETE-LIST` — VAL,NEG/P3 (Targets has no collection DELETE)

---

## 5. Operation (OP-*) — 3 RPC

- `*-GET-CRUD-IN-FLIGHT` — CRUD/P0 — Get in-flight op returns done=false (Verifies REQ-OP-GET-INFLIGHT)
- `*-GET-CRUD-COMPLETED` — CRUD/P0 — Get completed op returns done=true + response
- `*-GET-NEG-NF-INVALID-PREFIX` — NEG/P0 — malformed opId → InvalidArgument (Verifies REQ-OP-GET-NEG-PREFIX)
- `*-GET-NEG-NF-VALID-PREFIX` — NEG/P1 — well-formed but missing → NotFound
- `*-LST-CRUD-OK` — CRUD,LSG/P1 — List ops in project (Verifies REQ-OP-LST-01)
- `*-CANCEL-STATE-ALREADY-DONE` — STATE,NEG/P1 — Cancel already-done → FailedPrecondition (Verifies REQ-OP-CANCEL-DONE)

---

## 6. Authz (AZD-*) — every public RPC × {deny, grant, lifecycle}

### Per-RPC deny matrix (30 public RPC × representative deny case)
- `*-NLB-CR-VIEWER-DENIED` — AZD/P0 — viewer on project cannot Create LB (Verifies REQ-AZD-NLB-CR)
- `*-NLB-GET-STRANGER-DENIED` — AZD/P0 — subject without any tuple → PermissionDenied
- `*-NLB-GET-VIEWER-OK` — AZD/P1 — viewer OK on Get
- `*-NLB-UPD-VIEWER-DENIED` — AZD/P1 — viewer cannot Update
- `*-NLB-DEL-VIEWER-DENIED` — AZD/P1 — viewer cannot Delete
- `*-NLB-START-VIEWER-DENIED` — AZD/P1 — viewer cannot Start
- `*-NLB-STOP-VIEWER-DENIED` — AZD/P1 — viewer cannot Stop
- `*-NLB-MV-SCOPE-DST-DENIED` — AZD/P0 — editor on src + viewer on dst → PermissionDenied (Verifies REQ-AZD-NLB-MV-SCOPE)
- `*-NLB-ATT-NEEDS-VIEWER-ON-TG` — AZD/P1 — editor on LB but no tuple on TG → PermissionDenied
- `*-NLB-DET-VIEWER-DENIED` — AZD/P1 — viewer cannot Detach
- `*-NLB-GTS-STRANGER-DENIED` — AZD/P1 — stranger cannot read target states
- `*-NLB-LST-STRANGER-DENIED` — AZD/P1 — stranger cannot List
- `*-NLB-LOPS-STRANGER-DENIED` — AZD/P2 — stranger cannot ListOperations

- `*-LST-CR-VIEWER-DENIED` — AZD/P0 — viewer on LB cannot Create Listener (Verifies REQ-AZD-LST-CR)
- `*-LST-UPD-VIEWER-DENIED` — AZD/P1
- `*-LST-DEL-VIEWER-DENIED` — AZD/P1
- `*-LST-GET-STRANGER-DENIED` — AZD/P1
- `*-LST-LST-STRANGER-DENIED` — AZD/P2
- `*-LST-LOPS-STRANGER-DENIED` — AZD/P2

- `*-TGR-CR-VIEWER-DENIED` — AZD/P0 — viewer on project cannot Create TG
- `*-TGR-UPD-VIEWER-DENIED` — AZD/P1
- `*-TGR-DEL-VIEWER-DENIED` — AZD/P1
- `*-TGR-MV-SCOPE-DST-DENIED` — AZD/P0
- `*-TGR-ADD-VIEWER-DENIED` — AZD/P0 — viewer cannot AddTargets (Verifies REQ-AZD-TGR-ADD)
- `*-TGR-RM-VIEWER-DENIED` — AZD/P0 — viewer cannot RemoveTargets
- `*-TGR-GET-STRANGER-DENIED` — AZD/P1
- `*-TGR-LST-STRANGER-DENIED` — AZD/P2
- `*-TGR-LOPS-STRANGER-DENIED` — AZD/P2

- `*-OP-GET-OUTSIDE-SCOPE-DENIED` — AZD/P1 — viewer on parent OK; outside-scope → denied
- `*-OP-CANCEL-NON-CREATOR-DENIED` — AZD/P0 — only operation creator can Cancel (Verifies REQ-AZD-OP-CANCEL)

### Special / cross-cutting AZD
- `*-FGA-UNAVAILABLE-FAIL-CLOSED` — AZD/P0 — FGA service unavailable → PermissionDenied (fail-closed) (Verifies REQ-AZD-FAIL-CLOSED)
- `*-NLB-CR-ANONYMOUS-UNAUTH` — AZD/P0 — no Authorization header → UNAUTHENTICATED 401 (Verifies REQ-AZD-ANON)
- `*-PERMISSION-CATALOG-COMPLETE` — AZD/P0 — full enumeration of 30 loadbalancer.* permissions present (Verifies REQ-AZD-CATALOG)
- `*-CUSTOM-ROLE-OPERATOR-START` — AZD/P1 — custom role granting only start/stop resolves to editor relation (Verifies REQ-AZD-CUSTOM-ROLE)
- `*-CUSTOM-ROLE-TARGET-MANAGER` — AZD/P1 — targetManager role can AddTargets but not Update TG metadata
- `*-CUSTOM-ROLE-UNKNOWN-PERMISSION` — AZD/P1 — role with unknown permission rejected at create
- `*-BREAKGLASS-DEV-BYPASS` — AZD/P2 — KACHO_NLB_AUTHZ__BREAKGLASS=true bypasses (dev-only)
- `*-LIFECYCLE-DELETED-TUPLE-CLEANUP` — AZD/P1 — D-13 DELETED event → openfga.DeleteByObject ≤10s (Verifies REQ-AZD-LIFECYCLE-DEL)
- `*-CACHE-INVALIDATION-REVOKE` — AZD/P1 — revoke binding → ≤10s subject denied (Verifies REQ-AZD-CACHE-INVAL)
- `*-OWNER-RELATION-CREATOR` — AZD/P1 — creator has owner relation on created LB (Verifies REQ-AZD-OWNER)
- `*-SERVICE-ACCOUNT-SUBJECT` — AZD/P1 — service account editor on project can Create
- `*-GROUP-MEMBERSHIP-CASCADE` — AZD/P1 — group editor cascades to members
- `*-LIFECYCLE-INTERNAL-MTLS-ONLY` — AZD/P0 — InternalResourceLifecycleService restricted to mTLS (Verifies REQ-AZD-INTERNAL-MTLS)
- `*-NLB-UPD-STRANGER-NF` — AZD/P1 — Stranger Update on missing id → 403/404 (fail-closed passthrough)
- `*-LST-CR-STRANGER-NF` — AZD/P1 — Stranger Create on missing parent LB → 403/404
- `*-TGR-CR-STRANGER-DENIED` — AZD/P1 — Stranger Create TG → PERMISSION_DENIED
- `*-NLB-CR-ANONYMOUS-LST-UNAUTH` — AZD/P0 — Listener.Create anonymous → 401
- `*-TGR-CR-ANONYMOUS-UNAUTH` — AZD/P0 — TG.Create anonymous → 401
- `*-OP-LIST-STRANGER-FILTERS-SCOPE` — AZD/P1 — Op.List by stranger returns empty (scope-filter)

---

### Extended VAL/NEG/BVA per-RPC matrix (production saturation)

These extended patterns saturate the RPC × class matrix to ≥320 total cases for D-4:

- `*-CR-VAL-NAME-NUMERIC-START` — VAL/P1 — name starts with a digit → InvalidArgument
- `*-CR-VAL-NAME-HYPHEN-START` — VAL/P1 — name starts with `-` → InvalidArgument
- `*-CR-VAL-NAME-HYPHEN-END` — VAL/P1 — name ends with `-` → InvalidArgument
- `*-CR-VAL-NAME-SPECIAL-CHARS` — VAL/P1 — `!`/`@`/space in name → InvalidArgument
- `*-CR-VAL-DESC-NULL` — VAL/P2 — description=null → handled
- `*-CR-VAL-DESC-INT-TYPE` — VAL/P3 — description=number → 400 transcode
- `*-CR-VAL-LABELS-STRING-TYPE` — VAL/P2 — labels=string instead of object → 400
- `*-CR-VAL-LABELS-VALUE-OVER-63` — VAL,BVA/P2 — label value >63 chars → InvalidArgument
- `*-CR-VAL-LABELS-EMPTY-VALUE` — VAL/P2 — label value="" → handled
- `*-CR-VAL-WRONG-CT` — VAL,NEG/P3 — POST without Content-Type → 415/400/200
- `*-GET-NEG-INVALID-ID-PREFIX` — NEG,VAL/P0 — Get with malformed id prefix → InvalidArgument
- `*-UPD-NEG-INVALID-ID-PREFIX` — NEG,VAL/P0 — Update with malformed id prefix → InvalidArgument
- `*-DEL-NEG-INVALID-ID-PREFIX` — NEG,VAL/P0 — Delete with malformed id prefix → InvalidArgument
- `*-LST-NEG-LB-UNKNOWN` — NEG,LSG/P1 — List by unknown parent id → handled (200 empty or 404)
- `*-LST-CRUD-EMPTY-FILTER` — LSG/P2 — empty filter param → 200
- `*-LST-PAGE-TOKEN-EMPTY` — LSG,BVA/P2 — pageToken="" → 200 (default behaviour)
- `*-LST-BVA-PAGESIZE-1000` — BVA,LSG/P2 — pageSize=1000 (max) → 200
- `*-LST-BVA-PAGESIZE-1001` — BVA,VAL,LSG/P2 — pageSize=1001 (off-by-one over max) → InvalidArgument
- `*-LST-BVA-PAGESIZE-NEGATIVE` — BVA,VAL,LSG/P2 — pageSize=-1 → InvalidArgument
- `*-UPD-STATE-NO-CHANGE` — STATE,IDEM/P2 — Update with same value → no-op success
- `*-START-NEG-NF-UNKNOWN` — NEG/P1 — Start on unknown id → 404
- `*-STOP-NEG-NF-UNKNOWN` — NEG/P1 — Stop on unknown id → 404
- `*-ATT-NEG-LB-UNKNOWN` — NEG/P1 — Attach to unknown LB id → 404
- `*-DET-NEG-LB-UNKNOWN` — NEG/P1 — Detach from unknown LB id → 404
- `*-DET-NEG-TG-UNKNOWN` — NEG/P1 — Detach unknown TG id → 404
- `*-GTS-NEG-NF-UNKNOWN` — NEG/P1 — GetTargetStates of unknown LB → 404
- `*-LOPS-NEG-NF-UNKNOWN` — NEG/P1 — ListOperations of unknown id → 404
- `*-ATT-BVA-PRIORITY-MIN-0` — BVA/P2 — priority=0 (lower bound) → OK
- `*-ATT-BVA-PRIORITY-MAX-1000` — BVA/P2 — priority=1000 (upper) → OK
- `*-ATT-BVA-PRIORITY-NEGATIVE` — VAL,BVA/P1 — priority=-1 → InvalidArgument
- `*-CR-BVA-LABELS-MAX-64` — BVA/P2 — exactly 64 labels (upper bound) → OK
- `*-CR-CRUD-NO-OPTIONAL-FIELDS` — CRUD/P2 — Create with only required fields → OK
- `*-CR-CRUD-WITH-DESCRIPTION` — CRUD/P2 — Create with non-empty description → OK
- `*-CR-CRUD-AFFINITY-CLIENT-IP` — CRUD/P2 — Create with sessionAffinity=CLIENT_IP_ONLY → OK
- `*-CR-CRUD-CROSS-ZONE-FALSE` — CRUD/P2 — Create with crossZoneEnabled=false → OK
- `*-CR-VAL-IPV-UNKNOWN` — VAL/P1 — ip_version=IPV9 → InvalidArgument
- `*-CR-VAL-TARGET-PORT-ZERO` — VAL,BVA/P1 — target_port=0 → InvalidArgument
- `*-CR-VAL-TARGET-PORT-OVER` — VAL,BVA/P1 — target_port=65536 → InvalidArgument
- `*-CR-CRUD-IPV6` — CRUD/P1 — Create with ip_version=IPV6 → OK
- `*-CR-CRUD-PROXY-PROTO-V2` — CRUD/P2 — Create with proxy_protocol_v2=true → OK
- `*-UPD-CRUD-DEFAULT-TG-CLEAR` — CRUD,STATE/P2 — Update default_target_group_id=null → cleared
- `*-CR-VAL-TG-NAME-COLLISION-CROSS-REGION` — VAL/P2 — same name in different region → allowed (Verifies REQ-DB-TGR-NAME-UNIQ)
- `*-RM-VAL-EMPTY-LIST` — VAL/P1 — RemoveTargets with empty list → InvalidArgument
- `*-LST-FILTER-LABELS` — LSG/P2 — List with filter labels.X="..." → 200
- `*-LST-FILTER-COMBINED` — LSG/P2 — List with combined filter (name + labels) → 200/400
- `*-CR-CRUD-DELETION-PROTECTION-TRUE` — CRUD,STATE/P2 — Create with deletion_protection=true → persisted
- `*-UPD-CRUD-DELETION-PROTECTION-TOGGLE` — CRUD,STATE/P2 — Update toggles deletion_protection round-trip
- `*-CR-NEG-EMPTY-NAME-EMPTY-REGION` — VAL,NEG/P2 — multi-field violation
- `*-GTS-CRUD-EMPTY-LB-ACTIVE` — CRUD,STATE/P2 — GetTargetStates on ACTIVE LB → []
- `*-UPD-VAL-LABELS-OVER-64` — VAL,BVA/P1 — Update labels >64 entries → InvalidArgument
- `*-MV-NEG-DEST-UNKNOWN-PROJECT` — NEG/P1 — Move to unknown dst project → NotFound
- `*-LST-FILTER-NAME` — LSG/P2 — List with filter name="X" → handled
- `*-LST-PAGE-ROUNDTRIP` — CRUD,LSG,BVA/P2 — pagination round-trip on listeners
- `*-CR-CRUD-UDP-PROTOCOL` — CRUD/P1 — Create Listener protocol=UDP → OK
- `*-CR-CRUD-HTTPS-PROBE` — CRUD/P1 — Create TG with https probe → OK
- `*-CR-CRUD-GRPC-PROBE` — CRUD/P1 — Create TG with grpc probe → OK
- `*-CR-CRUD-DEREG-MIN-0` — BVA,CRUD/P2 — deregistration_delay_seconds=0 → OK
- `*-CR-CRUD-DEREG-MAX-3600` — BVA,CRUD/P2 — deregistration_delay_seconds=3600 → OK
- `*-CR-CRUD-SLOW-START-MIN-0` — BVA,CRUD/P2 — slow_start_seconds=0 → OK
- `*-CR-CRUD-SLOW-START-MAX-900` — BVA,CRUD/P2 — slow_start_seconds=900 → OK

## 7. Helper-generated patterns (cannot be tagged in case files)

These ids come from gen.py helper blocks and pass validation via the
`*-<SUFFIX>` patterns above:

- `http_method_not_allowed_block` → `*-METHOD-PUT-NOT-ALLOWED`, `*-METHOD-DELETE-LIST`
- `conf_alreadyexists_block` → `*-CR-CONF-ALREADY-EXISTS`

---

## 8. Module catalogue summary

| Module | Domain prefix | Pattern count | Approx cases |
|---|---|---|---|
| `load-balancer.py` | `NLB-*` | ~55 | 60-70 |
| `listener.py` | `LST-*` | ~28 | 30-35 |
| `target-group.py` | `TGR-*` | ~30 | 35-40 |
| `targets.py` | `TGT-*` | ~22 | 22-28 |
| `operation.py` | `OP-*` | 6 | 6 |
| `authz-deny.py` | `AZD-*` | ~42 | 42-50 |

Total ≥320 unique catalogued cases (production-readiness target per acceptance §12.1).
