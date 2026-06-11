# Polyrepo: структура, зависимости, порядок работы

Workspace — корневой git-репо. Sibling-репо клонируются в `./project/`
(`bootstrap.sh`); `project/` под gitignore, у каждого свой `.git/` и
`git@github.com:PRO-Robotech/<repo>.git` (`gh` авторизован).

## Репозитории

| Репо | Роль |
|---|---|
| `kacho-workspace` | корень: CLAUDE.md/rules, общие агенты, спеки, bootstrap |
| `kacho-proto` | **единственный** дом всех `.proto` (`proto/kacho/cloud/<domain>/v1/`); сгенерированные Go-stubs commit-ятся в `gen/go/...` |
| `kacho-corelib` | переиспользуемые Go-пакеты (см. `architecture.md`) |
| `kacho-api-gateway` | edge: gRPC-proxy + grpc-gateway REST |
| `kacho-iam` | Account / Project / User / ServiceAccount / Group / Role / AccessBinding |
| `kacho-vpc` | Network / Subnet / SecurityGroup / RouteTable / Address / Gateway / NetworkInterface |
| `kacho-compute` | Instance / Disk / Image / Snapshot + Geography (Region/Zone) |
| `kacho-deploy` | dev-стенд (Postgres + ingress) + e2e |
| `kacho-ui` | Vite + React SPA control plane |
| `kacho-test` | сводный e2e/regression стенд |
| `kacho-vpc-implement` | data-plane sibling VPC — spec-only, вне build-графа, control-plane его не касается |

**Новый `.proto` — ВСЕГДА в `kacho-proto/`.** Сервисные репо `.proto` не содержат —
только Go-импорт `github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/<domain>/v1`.
Единый `buf lint`/`buf breaking` на всё, синхронные версии, готовые клиентские SDK.

## Build-граф (источник истины — `replace github.com/PRO-Robotech/...` в `*/go.mod`)

```
kacho-proto                 ← ни от чего внутри проекта не зависит
  └─ kacho-corelib          ← replace ../kacho-proto
       ├─ kacho-iam         ┐ каждый сервис: replace ../kacho-corelib + ../kacho-proto.
       ├─ kacho-vpc         │ Между собой сервисы НЕ зависят по build (DB-per-service,
       ├─ kacho-compute     │ общение только по API).
       └─ kacho-api-gateway ┘ (api-gateway импортирует proto-stubs всех доменов)
kacho-deploy   ← Dockerfile'ы COPY ../kacho-*; build-context = parent dir
kacho-ui/test  ← зависят от REST api-gateway в runtime (не build)
```

Связь — `replace ../` (осознанный выбор для polyrepo-dev в одном дереве; переход на
versioned modules — под релизную фазу). Проверка: `grep -rn "replace github.com/PRO-Robotech" project/*/go.mod`.

## Runtime cross-domain edges (gRPC service→service; НЕ build-зависимость)

- `kacho-vpc → kacho-compute` — валидация `zone_id` (`compute.v1.ZoneService.Get`); Geography — домен compute.
- `kacho-compute → kacho-vpc` — валидация NIC-spec (Subnet/SecurityGroup) + IPAM-аллокация Address.
- `* → kacho-iam` — `ProjectService.Get` (existence + account lookup, leaf-owner) + `InternalIAMService.Check` (authz-gate).
- `kacho-vpc → kacho-iam` (fgaproxy, SEC-A) — `InternalIAMService.RegisterResource`/`UnregisterResource`: запись/снятие
  owner-hierarchy-tuple в FGA через IAM (модули не ходят в FGA напрямую). Internal-only :9091, идемпотентно, at-least-once
  через transactional-outbox (SEC-D). Least-priv: ReBAC `fga_writer` @ `iam_fgaproxy:system`.
- `kacho-compute → kacho-iam` (fgaproxy, SEC-A) — то же ребро: `RegisterResource`/`UnregisterResource` для owner-tuple
  compute-ресурсов. Internal-only :9091, идемпотентно, fgaproxy least-priv `fga_writer` @ `iam_fgaproxy:system`.

**Циклы запрещены**: если A зовёт B — B не зовёт A. Новое ребро фиксируется здесь как runtime-edge.
- `kacho-vpc ⇄ kacho-compute` — **НЕ семантический цикл**: рёбра разнонаправлены по ресурсному контексту
  (`vpc→compute` = валидация `zone_id`/Geography; `compute→vpc` = валидация NIC-spec + IPAM). Запрос по одному ребру
  **не порождает обратный синхронный вызов** по другому (нет request-time A→B→A). Допустимо как два независимых runtime-edge.
Регламент кросс-доменных ссылок — `data-integrity.md`.

## Порядок работы / merge для кросс-репо фичи (топосортировка графа)

1. `kacho-proto` (новый `.proto` + регенерация `gen/`, `buf lint`/`breaking` зелёные)
2. `kacho-corelib` (если меняются общие пакеты)
3. сервис(ы) (`kacho-vpc`/`kacho-iam`/`kacho-compute` — между собой в любом порядке)
4. `kacho-api-gateway` (регистрация RPC: public mux / internal mux)
5. `kacho-deploy` (helm/compose)
6. `kacho-workspace` (docs/specs)

Пока вышестоящее не в `main` — нижестоящий CI временно пиннит sibling к feature-ветке
(`ref:` в `.github/workflows/ci.yaml`); после merge — `ref: main`. Кросс-репо эпик —
tracking-issue в `kacho-workspace` (метка `epic`) + per-repo issue с `Blocked by PRO-Robotech/<repo>#<n>`.
