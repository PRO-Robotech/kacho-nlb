# `scripts/` — newman generator + runner

| Script | Purpose |
|---|---|
| `gen.py` | Loads `cases/*.py` → generates `collections/<svc>.postman_collection.json`. Hard-fails on duplicate case-ids. |
| `validate-cases.py` | Pure-Python pre-CI gate: duplicate-id check + catalogue coverage (CASES-INDEX.md + `# index:` tags). |
| `run.sh` | newman runner — all services in parallel by default (`--jobs 4`); per-service mode via `--service`. |
| `run-incremental.sh` | Quota-safe per-folder runner with `--resume`; useful when AddressPool capacity is tight. |

Conventions:
- `gen.py` is invoked via `python3 scripts/gen.py [service]` (no args = regenerate all).
- `gen.py --validate` delegates to `validate-cases.py` for convenience.
- `run.sh` reads `environments/local.postman_environment.json` by default; override with `--env`.
- All scripts are POSIX-clean; no Node-only dependencies beyond `newman` itself.
