# `docs/` — newman regression documentation

| File | Purpose |
|---|---|
| `TAXONOMY.md` | Case-id grammar, test classes, priorities, folder layout |
| `CASES-INDEX.md` | Catalogue of every unique case pattern — enforced by `validate-cases.py` |
| `TEST-PLAN.md` | RPC × class coverage matrix; design-invariant coverage table |
| `PRODUCT-REQUIREMENTS.md` | Normative `REQ-*` specifications mapped to cases and GWT scenarios |
| `RESULTS.md` | Latest per-suite pass/fail counters; updated after every CI run |

When adding a new case or changing the contract:

1. If it introduces a **new pattern** → add a row to `CASES-INDEX.md` (and a `REQ-*` in `PRODUCT-REQUIREMENTS.md` if applicable).
2. If it's an **instance of an existing pattern** → tag the `id=...` line in
   the case file with `# index: <pattern-ref>` (no catalogue change needed).
3. `validate-cases.py` enforces both rules and prevents duplicate case-ids.
