# `cases/` — declarative newman case modules

Source of truth for the regression suite. Each `*.py` file is a Python module that:

1. Imports nothing — `gen.py` injects shared helpers (`Step`, `Case`,
   `assert_status`, `poll_operation_until_done`, ...) into the module
   namespace at load time.
2. Defines a top-level `CASES: list[Case]` list of case definitions.
3. Files prefixed with `_` (e.g. `_helpers.py`) are skipped by `gen.py`.

| File | Domain | Approx cases |
|---|---|---|
| `load-balancer.py` | `NLB-*` | ~120 |
| `listener.py` | `LST-*` | ~49 |
| `target-group.py` | `TGR-*` | ~67 |
| `targets.py` | `TGT-*` | ~29 |
| `operation.py` | `OP-*` | 6 |
| `authz-deny.py` | `AZD-*` | ~49 |
| `_helpers.py` | — | reserved (not generated) |

Add a new case → run `python3 scripts/validate-cases.py` → if it passes,
the case is either catalogued in `docs/CASES-INDEX.md` or tagged with
`# index: <pattern-ref>` (see TAXONOMY.md / CASES-INDEX.md).
