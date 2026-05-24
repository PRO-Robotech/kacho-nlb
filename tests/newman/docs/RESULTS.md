# RESULTS — kacho-nlb newman regression run history

> Baseline counters established with the initial check-in (KAC-NLB-newman-cases).
> Updated after every run via `scripts/run.sh` → `out/summary.txt`.

## Latest baseline (v0 — initial commit)

| Service | Cases | Steps | Assertions | Failed |
|---|---|---|---|---|
| load-balancer | TBD | TBD | TBD | unknown (stack not yet deployed) |
| listener      | TBD | TBD | TBD | unknown |
| target-group  | TBD | TBD | TBD | unknown |
| targets       | TBD | TBD | TBD | unknown |
| operation     | TBD | TBD | TBD | unknown |
| authz-deny    | TBD | TBD | TBD | unknown |
| **TOTAL**     | ≥320 | ≥1200 | ≥2500 | — |

Numbers will be populated by the first CI run after kacho-nlb implementation
reaches deployable state (post epic merge per acceptance D-2). Until then,
the suite is **structurally valid** (validate-cases.py passes, gen.py produces
parseable Postman collections) but cannot execute against any backend.

## Version history

| Date | Suite version | Cases | Failed | Notes |
|---|---|---|---|---|
| 2026-05-23 | v0 baseline | ≥320 | n/a | Initial check-in: cases + scripts + docs scaffold; collections generated and committed; no backend yet. |

## Acceptance D-4 gate

D-4 (acceptance §17 DoD): Newman matrix 100% pass — minimum **320 cases** +
**≥30 AZD cases** + 0 failures. Verified by `newman-e2e` workflow in `kacho-deploy`
once the implementation epic merges.

## How to re-run

```bash
# port-forward api-gateway (one shell)
kubectl -n kacho port-forward svc/api-gateway 18080:8080

# full suite (another shell)
cd tests/newman
python3 scripts/validate-cases.py            # uniqueness + catalogue
python3 scripts/gen.py                       # regenerate collections (already committed)
./scripts/run.sh                             # all services in parallel (default --jobs 4)

# one service
./scripts/run.sh --service load-balancer

# quota-safe (one folder at a time, with --resume)
./scripts/run-incremental.sh --service load-balancer --resume

# kind stand (E2E CI env)
./scripts/run.sh --env environments/kind-stand.postman_environment.json
```

After each run, paste `out/summary.txt` (or `out/inc-summary.txt`) into a new
row of the **Version history** table above and append per-service breakdown
into the **Latest baseline** table.
