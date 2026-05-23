# kacho-nlb newman regression — TODO

Декларативный генератор: `cases/*.py` (источник истины) → `scripts/gen.py` →
`collections/*.postman_collection.json` (не править руками) → `scripts/run.sh`
выполняет newman.

Case-id prefixes: `NLB-*` / `LST-*` / `TGR-*` / `TGT-*` / `OP-*` / `AZD-*`.

TODO(KAC-167): копировать инфраструктуру из `../../kacho-compute/tests/newman/`
(scripts/{gen.py,run.sh}, environments/local.postman_environment.json, docs/...)
и адаптировать под NLB resource-set.
