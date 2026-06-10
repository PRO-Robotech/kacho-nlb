# kacho-nlb — CLAUDE.md

NLB-специфичный CLAUDE.md. Базовые правила Kachō (`.claude/rules/*`) — локальная
копия, синхронизируемая из workspace (`./sync-tooling.sh`; источник истины —
`kacho-workspace/.claude/rules/`, копию здесь не редактировать). `@import` ниже делает
репо самодостаточным и при standalone-клоне.

## Базовые правила Kachō (@import — синканная копия из workspace)

@.claude/rules/00-kacho-core.md
@.claude/rules/api-conventions.md
@.claude/rules/polyrepo.md
@.claude/rules/architecture.md
@.claude/rules/data-integrity.md
@.claude/rules/security.md
@.claude/rules/git-youtrack.md
@.claude/rules/testing.md
@.claude/rules/vault.md
@.claude/rules/ai-tooling.md

> **Происхождение:** новый сервис, написан с нуля на проверенных паттернах
> `kacho-compute` / `kacho-vpc` + evgeniy regulation v2 (UseCase / CQRS /
> self-validating domain / viper YAML config / separate cmd/migrator).
> Где видишь «как в compute» — буквально смотри одноимённый файл в `../kacho-compute/`.

## 0. Базовые принципы (обязательно)

1. **acceptance gate**: `docs/specs/sub-phase-4.0-nlb-acceptance.md` (APPROVED) — единственный источник GWT-критериев; новые RPC / поля без acceptance-обновления не реализуются (запрет #1).
2. **`kacho-corelib`** — общие компоненты (`ids`, `operations`, `db`, `grpcsrv`, `outbox`, `authz`, `resourcelifecycle`, ...). Не дублируй per-service.
3. **`kacho-proto`** — все `.proto` для loadbalancer уже vendored: `proto/kacho/cloud/loadbalancer/v1/`. Изменения proto — только в kacho-proto.
4. **Test-first** — RED → GREEN, integration + newman в том же PR (запрет #11).
5. **FGA REBAC** для authz (KAC-108) — per-RPC `iam.InternalIAMService.Check` через `internal/check` interceptor + permission map.
6. **DB-уровень refs** (запрет #10): FK / partial UNIQUE / EXCLUDE / atomic-CAS; software-only refcheck запрещён.

## 1. Идентификаторы и REST

| Ресурс | ID prefix | `kacho-corelib/ids` const | REST namespace |
|---|---|---|---|
| NetworkLoadBalancer | `nlb` | `PrefixLoadBalancer` | `/nlb/v1/networkLoadBalancers` |
| Listener | `lst` | `PrefixListener` | `/nlb/v1/listeners` |
| TargetGroup | `tgr` | `PrefixTargetGroup` | `/nlb/v1/targetGroups` |
| Operation | `nlb` | `PrefixOperationNLB` | `/nlb/v1/operations/{id}` |
| GlobalLoadBalancer | `glb` | reserved | (out-of-scope) |

## 2. Cross-service runtime edges

- `nlb → vpc.{InternalAddressService, AddressService, SubnetService, NetworkInterfaceService}`
- `nlb → compute.{InstanceService, RegionService}`
- `nlb → iam.{ProjectService, InternalIAMService.Check, InternalIAMService.WriteCreatorTuple}`
- `iam → nlb.InternalResourceLifecycleService.Subscribe` (D-13 lifecycle stream)

Циклы запрещены (обратные направления — только iam-driven subscribe).

## 3. Permission catalog

Все RPC требуют `loadbalancer.*` permission (~30 строк). Source of truth —
`kacho-iam/internal/authzmap/permission_catalog.go` (namespace `loadbalancer.`).
Каждый handler-pkg регистрирует mapping в `internal/check/permission_map.go`.

## 4. Конвенции

- **Status enum-колонка** (не JSONB envelope), writable only by worker / DB trigger.
- **Operation LRO** на каждую мутацию; worker-side через `corelib/operations.Run`.
- **Outbox+NOTIFY**: `nlb_outbox` table, trigger → `pg_notify('nlb_outbox', ...)`.
- **2-phase RemoveTargets drain**: Phase A immediate `DRAINING`-mark, Phase B background `DELETE`.
- **same-region constraint**: LB + все его TG в одном `region_id` (DB CHECK).
- **FK RESTRICT** на каждом ребре same-DB; cross-service refs — soft sync precheck.
- **Newman case-id prefixes**: `NLB-*` / `LST-*` / `TGR-*` / `TGT-*` / `OP-*` / `AZD-*`.
- **ENV prefix**: `KACHO_NLB_*` (viper delimiter `__`, e.g. `KACHO_NLB_REPOSITORY__POSTGRES__URL`).

## 5. Запреты (workspace + сервис-специфика)

- Workspace CLAUDE.md запреты #1-#11 — все применяются.
- **НЕ `Upsert`-семантика** — explicit `Create/Update/Delete` (acceptance §0.2).
- **НЕ full-replace `attached_target_groups[]`** — отдельные `AttachTargetGroup` / `DetachTargetGroup` RPC (`INSERT ... ON CONFLICT DO NOTHING` idempotent).
- **НЕ упоминать `yandex`** (workspace CLAUDE.md запрет #2).
- **НЕ редактировать миграции после merge** — новая миграция = новый файл с инкрементным номером.

## 6. Ссылки

- Workspace: [`../../CLAUDE.md`](../../CLAUDE.md)
- Design: [`../../docs/superpowers/specs/2026-05-23-kacho-nlb-design.md`](../../docs/superpowers/specs/2026-05-23-kacho-nlb-design.md)
- Acceptance: [`../../docs/specs/sub-phase-4.0-nlb-acceptance.md`](../../docs/specs/sub-phase-4.0-nlb-acceptance.md)
- evgeniy skill: [`../../.claude/skills/evgeniy/SKILL.md`](../../.claude/skills/evgeniy/SKILL.md)
- godzila skill: [`../../.claude/skills/godzila/SKILL.md`](../../.claude/skills/godzila/SKILL.md)
- Reference services: `../kacho-compute/`, `../kacho-vpc/`
- Proto: `../kacho-proto/proto/kacho/cloud/loadbalancer/v1/`
