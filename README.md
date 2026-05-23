# kacho-nlb

L4 Network Load Balancer control-plane сервис Kachō (sub-phase 4.0).

**Статус**: scaffold (KAC-146) — пустые stubs, реализация в KAC-147..KAC-167.
**Acceptance**: [`sub-phase-4.0-nlb-acceptance.md`](../../kacho-workspace/docs/specs/sub-phase-4.0-nlb-acceptance.md) (APPROVED).
**Design**: [`2026-05-23-kacho-nlb-design.md`](../../kacho-workspace/docs/superpowers/specs/2026-05-23-kacho-nlb-design.md).

## Что это

Control-plane (без data-plane sibling) для трёх публичных ресурсов:

| Resource              | ID prefix | REST namespace              |
|-----------------------|-----------|-----------------------------|
| NetworkLoadBalancer   | `nlb`     | `/nlb/v1/networkLoadBalancers` |
| Listener              | `lst`     | `/nlb/v1/listeners`            |
| TargetGroup           | `tgr`     | `/nlb/v1/targetGroups`         |

- **Async LRO**: каждая мутация возвращает `operation.Operation`.
- **FGA REBAC** (KAC-108): per-RPC `iam.InternalIAMService.Check`.
- **Outbox + LISTEN/NOTIFY** на канал `nlb_outbox` (D-13 lifecycle stream).
- **Cross-service refs** (vpc / compute / iam) — soft sync precheck + graceful dangling.
- **DB-уровень инварианты** (FK / partial UNIQUE / atomic CAS) — workspace CLAUDE.md §10.

`GlobalLoadBalancer` (cross-region) — зарезервированный slot (`glb` prefix), реализация out-of-scope.

## Layout

См. `docs/architecture/01-layout.md` и evgeniy skill §1.A:

```
cmd/{kacho-loadbalancer,migrator}/main.go
internal/apps/kacho/{api,jobs,config,utils}
internal/{domain,repo,dto,clients,check,fgawrite,migrations}
deploy/                # Helm chart
docs/architecture/     # 14 docs (ER, lifecycle, FGA, outbox, ...)
tests/{newman,k6}      # E2E + load
```

## Build

```bash
go build ./...                          # требует sibling kacho-corelib, kacho-proto
go vet ./...
go test ./... -race -short
```

## Docker

```bash
make docker                             # build context = parent dir (sibling repos)
```

## Conventions

- **Conventional Commits** + `KAC-<N>` в commit-message.
- **Branch per ticket** — `git checkout -b KAC-<N>` от `main`.
- **No `Co-Authored-By: Claude*`** trailers (workspace CLAUDE.md).
- **No `yandex`** в коде / комментариях (workspace CLAUDE.md §запрет 2).
- **Test-first** — RED (падающий тест) → код фикса (GREEN). Newman-кейс / integration-тест
  до кода фичи; PR без тестов в том же PR не мерж'ат (запрет #11).

## Links

- Workspace правила: [`../../kacho-workspace/CLAUDE.md`](../../kacho-workspace/CLAUDE.md)
- Service-specific Claude rules: [`CLAUDE.md`](./CLAUDE.md)
- Permission catalog source: `kacho-iam/internal/authzmap/permission_catalog.go` namespace `loadbalancer.*`
- Proto: `kacho-proto/proto/kacho/cloud/loadbalancer/v1/*.proto` (vendored)
