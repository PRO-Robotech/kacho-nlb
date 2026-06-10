---
name: evgeniy
description: Архитектурный регламент для kacho-vpc (и других kacho-* go-сервисов) на основе ревью @EvgenyGRI / @pointpu (PR PRO-Robotech/kacho-vpc#52, 2026-05-14). Применять при ЛЮБОМ рефакторинге, новом сервисе, новом ресурсе, новом domain-типе. Запрещает «толстые сервисы» / голые string-типы / inline-validation / envconfig в struct tags / smashed cmd-binary. Требует UseCase pattern, CQRS-разделённые порты, self-validating domain, DTO-таблицы, YAML-config через viper/koanf, отдельный cmd/migrator. Содержит 48 правил из ревью + step-by-step migration plan для kacho-vpc.
model: opus
---

# Skill: evgeniy — архитектурный регламент @EvgenyGRI

Источник: ревью архитектора @EvgenyGRI (commit `9d865df`, PR PRO-Robotech/kacho-vpc#52 от 2026-05-14).

> *«Я связался посмотреть ресурс Network и попытался его привести "в норму"… Лично от меня есть только субъективные замечания / пожелания»* — @EvgenyGRI

Замечания **не субъективные**, а нормативные. Все 48 правил ниже **обязательны** при любом рефакторинге `kacho-vpc`, `kacho-compute`, `kacho-resource-manager`, новых ресурсах, новых domain-типах. Этот skill **дополняет** workspace-`CLAUDE.md` (он не отменяет ни одного запрета, только усиливает code-style).

## Когда применять

- Любая работа в `kacho-vpc/internal/` или `kacho-compute/internal/` или `kacho-resource-manager/internal/`.
- Создание нового сервиса (`service-scaffolder` → дополнить этим regulation).
- Создание нового ресурса / domain-типа.
- Рефакторинг существующего service / repo / handler / config.
- Создание новой CLI-команды (мигратор, утилита, и т.п.).

Если работа касается `kacho-ui`, `kacho-deploy`, `kacho-api-gateway` (не Go-backend-domain-логика) — этот skill **неприменим**, действуют общие правила сервисов.

## Структура skill

- §1 Структура проекта (правила A.1-A.7)
- §2 UseCases vs Services (правила B.1-B.4)
- §3 DTO — table-driven, generic-based (правила C.1-C.6)
- §4 Domain types — self-validating + newtypes (правила D.1-D.10)
- §5 БД-уровень валидации (правила E.1-E.4) — синхронизировано с workspace `CLAUDE.md` §«Within-service refs»
- §6 Ports / Repository — CQRS (правила G.1-G.7)
- §7 Service — TimeStamps, race-prone checks, magic constants (правила H.1, I.1-I.10)
- §8 Config — YAML + viper/koanf (правила J.1-J.5)
- §9 cmd — отдельные binary + cobra/kong (правила K.1-K.9)
- §10 Async / Operations — анти-замечание L (фиксация архитектурного решения)
- §11 Anti-patterns каталог (с конкретным кодом из review)
- §12 Step-by-step migration plan для kacho-vpc
- §13 Review checklist (для применения при PR-review)

---

## §1. Структура проекта (A.1–A.7)

**A.1 — `cmd/` имеет отдельную точку сборки для каждого binary.**

```
cmd/
├── kacho-vpc/      # API-сервер (или kacho-compute, kacho-resource-manager)
│   └── main.go
└── migrator/       # отдельная CLI миграций (НЕ subcommand vpc binary)
    └── main.go
```

**Запрещено**: subcommand-mux в `main.go` вида `switch os.Args[1] { case "serve": ...; case "migrate": ... }`. Если есть второй CLI use-case — это отдельный binary, у него собственный `cmd/<name>/`.

**A.2 — `pkg/` для публичных API/DTO/domains.**

```
pkg/
├── domains/kacho/                  # доменные типы для внешнего потребления (SDK)
├── api/kacho/grpc/clients/         # сгенерированные proto-stubs + клиентский SDK
├── api/kacho/http/clients/         # HTTP-обёртки если нужны
└── dto/kacho/                      # публичные DTO для интеграций
```

⚠️ В монорепо Kachō `pkg/` живёт на верхнем уровне сервиса, не в корне workspace. Для сгенерированных stubs всё ещё центральный `kacho-proto/` репо.

**A.3 — `internal/` структура:**

```
internal/
├── apps/
│   ├── kacho/                     # реализация бизнес-логики API-сервера
│   │   ├── api/
│   │   │   ├── handlers/          # тонкий gRPC-transport
│   │   │   └── use-cases/         # use-cases (бывшие services)
│   │   ├── jobs/                  # workers (orchestration / outbox-drain / etc.)
│   │   ├── config/                # типы config + загрузчик (см. §8)
│   │   └── utils/
│   └── migrator/                  # бизнес-логика мигратора (если отдельный binary)
└── repo/
    └── kacho/                     # формальная модель репозитория (см. §6 CQRS)
        ├── pg/                    # реализация для PostgreSQL
        │   └── dto/               # DTO[kacho/pg ↔ domains/kacho]
        └── <other-impls>/         # если нужно несколько BD
```

**A.4** — `Makefile` + `Deploy/` на верхнем уровне (как сейчас).

**A.5** — Доменные модели изолированы в `pkg/domains/kacho/` или `internal/domain/`. **Никаких** ссылок на `pgx`, `proto`, `grpc` из domain пакета (workspace `CLAUDE.md` §«Чистая архитектура» — это уже на месте, фиксируем).

**A.6** — `internal/dto/` (или `internal/repo/kacho/pg/dto/`) — мост между слоями. Не вызывать его из domain (domain не должен знать про DTO).

**A.7** — Текущая структура `kacho-vpc/internal/{repo,service,handler,clients,domain,migrations,ports,protoconv}` — устаревшая. Миграция — §12 plan.

---

## §2. UseCases vs Services (B.1–B.4)

**B.1 — UseCase предпочтительнее Service.**

@EvgenyGRI: *«я склоняюсь к использованию UseCase(s)»*. Причины:
- **B.2 Локальны.** Не требуют лишнего слоя; в простых случаях (CRUD без бизнес-логики) их можно вообще не писать (handler → repo напрямую если нет правил).
- **B.3 Не требуют дополнительной модели.** Сервисы часто заводят `XxxReq`/`XxxResp` параллельно domain — это дубль. UseCase принимает domain-тип на входе.
- **B.4 Локализованы рядом с использованием.** Каждый use-case — в своём файле/пакете рядом с handler, который его вызывает.

```
internal/apps/kacho/api/
├── network/
│   ├── handler.go              # gRPC handler — тонкий
│   ├── create.go               # CreateNetworkUseCase
│   ├── update.go               # UpdateNetworkUseCase
│   └── delete.go               # DeleteNetworkUseCase
```

**Запрещено**:
- Один большой `NetworkService` со всеми методами в одном файле (`internal/service/network.go` — сейчас 250+ строк).
- Параллельные `CreateNetworkReq`, `UpdateNetworkReq` структуры, дублирующие `domain.Network`. Передавать `domain.Network` (или его подмножество через newtype) напрямую в use-case.

**Текущее состояние kacho-vpc**: всё на Services. Миграция — §12 шаг 3.

---

## §3. DTO — table-driven, generic-based (C.1–C.6)

Источник: `internal/dto/base.go` + `internal/dto/type2pb/dtos.go` из PR #52 + REVIEW.txt.

**C.1 — Generic DTO interface:**

```go
package dto

type Interface[FromType any, ToType any] interface {
    Transfer(FromType) (ToType, error)
}
```

**C.2 — Реестр трансферов** (`RegTransfer` / `FindTransfer`) на основе `reflect.TypeFor[tag[F, T]]`:

```go
type tag[_ any, _ any] struct{}
var transfersReg dict.HDict[reflect.Type, any]

func RegTransfer[F, T any](impl Interface[F, T]) { ... }
func FindTransfer[F, T any](tg tag[F, T]) Interface[F, T] { ... }
```

**C.3 — Использование на caller-site**:

```go
var src domain.Network
var dst *vpcv1.Network
err := dto.Transfer(dto.FromTo(src, &dst))   // type-checked compile-time
```

**C.4 — `init()` регистрирует все трансферы пакета**:

```go
func init() {
    dto.RegTransfer(dto.Fn2Face(network{}.toPb))
    dto.RegTransfer(dto.Fn2Face(timeObj{}.toPb))
}
```

**C.5 — `Transfer[v types2ProtoVariants]` ограничивает компилятором допустимые пары** — type-set generic constraint:

```go
type types2ProtoVariants interface {
    Perform() error
    *dto.DTO[domain.Network, *vpcv1.Network] |
        *dto.DTO[time.Time, *timestamppb.Timestamp]
}
```

Каждый новый домен → новый файл `internal/dto/type2pb/<resource>.go` с трансфером, добавляется в type-set + регистрируется в `init()`.

**C.6 — `protoconv` переименовать в `dto/to-proto`**.

Текущий `internal/protoconv/protoconv.go` (250+ строк свитчей `Network/Subnet/Address/...` → `*vpcv1.*`) переписать на табличную DTO-схему. Миграция — §12 шаг 4.

**Запрещено**:
- Прямые маппинг-функции вроде `func Network(d domain.Network) *vpcv1.Network { ... }` без регистрации в DTO-реестре.
- Использование `protoconv.Network(...)` (старое API) — после миграции запретить.

---

## §4. Domain types — self-validating + newtypes (D.1–D.10)

**D.1 — Domain тип хранит ТОЛЬКО ID + data-поля.** `CreatedAt` (DB-managed) — **НЕ** в domain (см. H.1).

**D.2 — Голые `string` запрещены для полей с семантикой.** Newtypes:

```go
type (
    LabelKey      string
    LabelVal      string
    RcLabels      = dict.HDict[LabelKey, LabelVal]
    RcName        string
    RcDescription string
    RcNameOpt     = option.ValueOf[RcName]   // optional newtype через H-BF/corlib/option
)
```

**D.3 — Каждый newtype имеет `Validate() error`:**

```go
func (LabelKey) Validate() error { /* regex проверка */ }
func (LabelVal) Validate() error { /* regex */ }
func (RcName) Validate() error   { /* regex+length */ }
func (RcDescription) Validate() error { /* length ≤256 */ }
```

**D.4 — Сам domain тип имеет `Validate() error`** который вызывает каждый поле:

```go
func (n Network) Validate() error {
    return multierr.Combine(
        n.Name.Validate(),
        n.Description.Validate(),
        n.Labels.Validate(),   // RcLabels.Validate() итерирует пары
    )
}
```

**D.5 — Validate-логика живёт В domain-пакете, НЕ в `service/validate.go`.** Текущий `internal/service/validate.go` (или `corevalidate.NameVPC`/`Description`/`Labels`) — выносится в `internal/domain/network.go` и `domain/types.go`.

**D.6 — `Validate()` вызывается use-case'ом ПЕРЕД repo.Insert/Update**:

```go
func (u CreateNetworkUseCase) Execute(ctx context.Context, n domain.Network) (...) {
    if err := n.Validate(); err != nil { return ..., status.Error(codes.InvalidArgument, err.Error()) }
    return u.repo.Insert(ctx, n)
}
```

**D.7 — Builders / factory-функции для domain-типов**:

Запрещено inline-литерал domain-сущности с magic константами. Builders в domain-пакете:

```go
// domain/security_group_builders.go
func NewDefaultSecurityGroup(net Network) SecurityGroup {
    return SecurityGroup{
        ID:        ids.NewID(ids.PrefixSecurityGroup),
        FolderID:  net.FolderID,
        NetworkID: net.ID,
        Name:      "default-sg-" + truncateID(net.ID),   // helper в domain
        Status:    SecurityGroupStatusActive,            // константа
        Rules:     NewDefaultSecurityGroupRules(),       // builder
    }
}

func NewDefaultSecurityGroupRules() []SecurityGroupRule { ... }
```

**D.8 — Все статусы / enum-значения — константы в domain-пакете:**

```go
type SecurityGroupStatus string

const (
    SecurityGroupStatusActive    SecurityGroupStatus = "ACTIVE"
    SecurityGroupStatusCreating  SecurityGroupStatus = "CREATING"
    SecurityGroupStatusUpdating  SecurityGroupStatus = "UPDATING"
    SecurityGroupStatusDeleting  SecurityGroupStatus = "DELETING"
)
```

**Запрещено**: `Status: "ACTIVE"` инлайн (как сейчас в `service/network.go::doCreate`).

**D.9 — Magic number'ы — константы:**

`shortNet := created.ID; if len(shortNet) > 8 { shortNet = shortNet[:8] }` (review pointed «почему 8?») → константа `domain.shortIDLen = 8` + helper `TruncateID(id string) string`.

**D.10 — Сравнение domain-сущностей** — метод `Equal(other) bool` если применимо (для diff в Update).

---

## §5. БД-уровень валидации (E.1–E.4)

@EvgenyGRI NOTES.txt + workspace `CLAUDE.md` §«Within-service refs — DB-уровень обязателен».

**E.1 — Схема и данные в БД ДОЛЖНЫ иметь самовалидацию.** Domain.Validate — fast-fail для users, **но БД — последний рубеж** от:
- внешних writers (admin SQL-консоль, миграции, аварийные восстановления).
- bugs в app-коде, которые пропустят validate.

**E.2 — Каждый ограничение из domain (regex, length, enum) — дублируется DB-CHECK constraint** где это поддерживаемо PG (короткие regex — `CHECK (name ~ '^[a-z]([-_a-z0-9]{0,61}[a-z0-9])?$')`; enum — `CHECK (status IN ('ACTIVE', 'CREATING', ...))`).

**E.3 — Согласованность (FK/UNIQUE/EXCLUDE)** — синхронизировано с workspace `CLAUDE.md` (запрет #10). Все ссылки в пределах one-DB — DB-level.

**E.4 — Schema = НЕ `public`, а `kacho_<svc>` (или `какаши`).** Это уже на месте, фиксируем как нормативное.

**E.5 — constraint/index/FK — близко к declaration таблицы** (в той же миграции, не в следующей). Если таблица создаётся в миграции N — все её constraints, indexes, FK — в той же миграции N.

**E.6 — ER-диаграмма в `docs/architecture/`** — обязательна для каждого сервиса. Сейчас её нет ни в одном.

**E.7 — `id` как `TEXT` vs `UUID`** — обсуждение оставлено открытым в NOTES.txt; **текущее решение зафиксировано** в `kacho-vpc/CLAUDE.md` §3 (Resource ID format): TEXT с 3-char crockford-base32 префиксом для оперативной маршрутизации (api-gateway по prefix-у `enp/e9b/...` направляет в нужный backend). Это **архитектурное решение, не bug** — TEXT остаётся.

---

## §6. Ports / Repository — CQRS (G.1–G.7)

Источник: `internal/ports/ports.go` diff из review.

**G.1 — Имя `ports` плохо.** @EvgenyGRI: «когнитивный диссонанс — здесь только абстракции для repository». Переименовать в `internal/repo/<resource>/iface.go` или общий `internal/repo/iface.go`.

**G.2 — CQRS-разделение Reader/Writer:**

```go
// internal/repo/network/iface.go

// Реальная репо-сущность с DB-managed полями
type Network struct {
    domain.Network
    CreatedAt time.Time   // ← здесь, не в domain
}

type NetworkReaderIface interface {
    Get(ctx context.Context, id string) (Network, error)
    List(ctx context.Context, f Filter, p Pagination) ([]Network, string, error)
}

type NetworkWriterIface interface {
    NetworkReaderIface   // writer extends reader (write-txn видит свои writes)
    Insert(ctx context.Context, n domain.Network) (Network, error)
    Update(ctx context.Context, n domain.Network) (Network, error)
    Delete(ctx context.Context, id string) error
    Move2Folder(ctx context.Context, id, folderID string) (Network, error)
}
```

**G.3 — Корневой Repository с разделением TX:**

```go
type RepositoryReader interface {
    Networks() NetworkReaderIface
    Subnets()  SubnetReaderIface
    // ...
    Close() error
}

type RepositoryWriter interface {
    Networks() NetworkWriterIface
    Subnets()  SubnetWriterIface
    // ...
    Commit() error
    Abort()
}

type Repository interface {
    Reader(ctx context.Context) RepositoryReader   // открывает read-TX
    Writer(ctx context.Context) RepositoryWriter   // открывает write-TX
    Close() error
}
```

**G.4 — Reader-TX идут на slave-реплику, Writer — на master.** В коде это видно по уровню `Repository.Reader()` vs `Repository.Writer()`. Применимо когда появится read replica.

**G.5 — UseCase открывает TX явно:**

```go
func (u CreateNetworkUseCase) Execute(ctx context.Context, n domain.Network) (Network, error) {
    w := u.repo.Writer(ctx)
    defer w.Abort()   // no-op если Commit вызван

    if err := n.Validate(); err != nil { return Network{}, err }
    created, err := w.Networks().Insert(ctx, n)
    if err != nil { return Network{}, err }
    // ... outbox-write в той же TX через w.Outbox().Emit() ...
    if err := w.Commit(); err != nil { return Network{}, err }
    return created, nil
}
```

**G.6 — Старые отдельные `XxxRepo` интерфейсы** (`NetworkRepo`, `SubnetRepo`, ...) — рефакторинг в `XxxReader/Writer` + общий `Repository` (см. §12 шаг 5).

**G.7 — `mock` реализации** (`internal/ports/portmock/`) — пересоздаются под CQRS-интерфейсы. У каждого `XxxWriter/Reader` — свой mock с in-memory state.

---

## §7. Service — TimeStamps, race-prone checks, magic constants

**H.1 — `CreatedAt` НЕ в domain.** Это DB-managed (`DEFAULT now()`), domain про CRUD-намерение, а не про runtime-state. Перенос в `repo.Network = struct { domain.Network; CreatedAt time.Time }` (G.2 выше).

**I.1 — `XxxReq`/`XxxResp` структуры — анти-pattern**, если они зеркалят domain. Передавать `domain.Network` напрямую в UseCase. Если нужны доп поля (например `update_mask`) — `UpdateNetworkInput { Network domain.Network; Mask []string }` явно, и не дублировать domain-поля.

**I.2 — Validate-вызовы в service-слое — анти-pattern.** Domain.Validate() сам по себе (D.3-D.6).

**I.3 — `operations.Run(ctx, ..., func(ctx context.Context) {...})` с оторванным контекстом — ЗАПРЕЩЕНО.** @EvgenyGRI: *«ты запускаешь doCreate на ОТОРВАННОМ контексте — это значит что ты обрываешь все метаданные из вызывающего контекста. ЭТОГО БЫТЬ НЕ ДОПУСТИМО»*.

Текущая реализация `kacho-corelib/operations.Run` создаёт `context.Background()` для worker'а. Это ломает trace-id propagation, request-id, slog-attrs. Fix: передавать caller-context как minimum baggage (trace, request-id), создавать new ctx с **уже скопированными values + новый deadline**.

**I.4 — `folderClient.Exists` precheck в `doCreate` — race-prone и бессмысленный.** @EvgenyGRI: *«сейчас этот folder есть а через мгновение его нет»*. Полагаться на FK (если cross-DB FK был бы можно) → для нашего случая — на ошибку peer-сервиса в момент actual insert (она и так возвращается), а sync-precheck — only soft-hint.

**I.5 — Magic numbers ВНЕ domain-builders запрещены.** `shortNet[:8]` → константа `domain.ShortIDLen` + helper `domain.TruncateID(id)`.

**I.6 — Status enums инлайн запрещены.** `"ACTIVE"` → `domain.SecurityGroupStatusActive`.

**I.7 — Имена ресурсов через builders.** `"default-sg-" + shortNet` → `domain.DefaultSGName(networkID)`.

**I.8 — SecurityGroupRule literal в Network.Create — анти-pattern.** `domain.NewDefaultSecurityGroupRules()` builder.

**I.9 — Inline default-SG creation в `NetworkService.Create` — fat service.** Должна быть отдельная `CreateDefaultSGUseCase`, вызываемая из `CreateNetworkUseCase` (или композиция через job). Сейчас это inline через `KACHO_VPC_DEFAULT_SG_INLINE` флаг — рефакторинг через explicit composition.

**I.10 — «admin зачистит orphan SG» комментарии — анти-pattern.** Orphan-resources = баг. Использовать saga-pattern (compensating transaction) или TX-обёртку для atomic create Network + create default SG (выпуск UNIQUE / FK помогает в одной БД).

---

## §8. Config — YAML + viper/koanf (J.1–J.5)

@EvgenyGRI: *«конфиг должен быть наглядным а не просто в виде структуры»*.

**J.1 — YAML config-файл, иерархия секций:**

```yaml
logger:
  level: oneof<FATAL|ERROR|WARN|INFO|DEBUG>   # optional; default=DEBUG

api-server:
  endpoint: tcp://0.0.0.0:9090
  graceful-shutdown: 10s
  grpc-gw-enable: false

metrics:
  enable: true
healthcheck:
  enable: true

repository:
  type: POSTGRES
  postgres:
    url: postgres://un:psw@host/db

authn:
  type: oneof<none|tls>
  tls:
    key-file: filename.pem
    cert-file: filename.pem
    client:
      verify: oneof<skip|certs-required|verify>
      ca-files: ["file1.pem", ...]

extapi:
  def-dial-duration: 10s
  agents:
    dial-duration: 3s
    authn:
      type: oneof<none|tls>
      tls:
        key-file: priv.pem
        cert-file: cert.pem
        server:
          verify: true
          name: server-name
          ca-files: [...]
```

**J.2 — Библиотека:** `github.com/spf13/viper` (предпочтительно) или `github.com/knadh/koanf`. **Запрещено**: `kelseyhightower/envconfig` для нового кода.

**J.3 — Default'ы в одном месте — `internal/apps/<svc>/config/defaults.go`**, не в struct-tags.

**J.4 — ENV variable binding** — гибкий (viper-paths `repository.postgres.url` → `KACHO_VPC_REPOSITORY__POSTGRES__URL` через delimiter `__`), не hardcoded.

**J.5 — `validateAuthMode` — часть config-пакета**, не main. Не передавать logger как параметр (J.6 — anti-pattern). Если config невалиден — log на main-уровне после загрузки.

**J.6 — `bool productionMode` → ENUM `Mode { ModeDev, ModeProduction }`.** Семантичные имена.

**J.7 — `cfg.AuthMode` — плохое название.** Это `cfg.Mode` (общий режим работы), а не «auth mode» (там TLS/none — отдельная подсекция).

---

## §9. cmd — отдельные binary + cobra/kong (K.1–K.9)

**K.1 — Каждый CLI use-case — отдельный binary**, отдельный `cmd/<name>/main.go`. Migrator — `cmd/migrator/`, не subcommand основного.

**K.2 — CLI: `github.com/spf13/cobra`** (предпочтительно) или `github.com/alecthomas/kong`. **Запрещено**: ручной `switch os.Args[1]`.

**K.3 — Migrator должен поддерживать разные БД.** Spec не postgres-only:

```go
cmd/migrator/main.go
  --dialect oneof<postgres|cockroach|...>
  --dsn ...
  up|down|status|create
```

Реализация — через интерфейс `migrator.Dialect { Up, Down, Status, Create }`, текущая `postgres` — одна из реализаций.

**K.4 — Параллельные сервера (public+internal gRPC + shutdown-горутина)** — через library `H-BF/corlib/pkg/parallel/exec-in-parallel.go::ExecAbstract`:

```go
err := parallel.ExecAbstract(ctx,
    func(ctx) error { return grpcSrv.Serve(publicListener) },
    func(ctx) error { return internalSrv.Serve(internalListener) },
    func(ctx) error { return waitForShutdown(ctx, ...) },
)
```

**K.5 — Failure-isolation:** Если internal-server упал, public-server **должен тоже остановиться** (или хотя бы пометить unhealthy). Сейчас один внутри `go func() { ... }()` без error-prop — если он умер, public крутится в нерабочем состоянии. ExecAbstract обеспечивает all-or-nothing.

**K.6 — `dialResourceManager`** заменить на `H-BF/corlib/client/grpc/client-builder.go` — единый паттерн для всех gRPC-клиентов (retries, LB, TLS, metrics).

**K.7 — `validateAuthMode(cfg, logger)` — вынести в config-пакет** (J.5).

**K.8 — `productionMode bool` → ENUM.** (J.6)

**K.9 — `AuthMode` → `Mode`.** (J.7)

---

## §10. Async / Operations

@EvgenyGRI: *«не понимаю зачем тут плодить какие-то асинхронные операции, когда тут вся работа ведётся в обслуживании репозитория»*.

**L.1 — Архитектурное решение зафиксировано (workspace `CLAUDE.md` §«API contract — flat resources + Operations»):** все мутирующие RPC возвращают `Operation` (long-running async). Это **proto-контракт верхнего уровня** Kachō, не bug. Менять не предполагается.

**L.2 — Внутри Operation worker'а — НЕ оторванный контекст.** См. I.3. Это исправляется в `kacho-corelib/operations`, не в каждом сервисе.

**L.3 — Если в use-case реально нечего делать асинхронно** (например, чистый CRUD без peer-вызовов): минимизировать work-в-worker (положить ресурс в БД sync, операцию сразу пометить done=true). Не плодить async ради async.

---

## §11. Anti-patterns каталог

Список с конкретными примерами **запрещённого кода** из текущего kacho-vpc (на момент review).

### AP-1: Validate в service-слое

```go
// ❌ ЗАПРЕЩЕНО (internal/service/network.go::Create)
if err := corevalidate.NameVPC("name", req.Name); err != nil { return nil, err }
if err := corevalidate.Description("description", req.Description); err != nil { return nil, err }
if err := corevalidate.Labels("labels", req.Labels); err != nil { return nil, err }
```

```go
// ✅ Domain self-validating
if err := n.Validate(); err != nil { return nil, status.Error(codes.InvalidArgument, err.Error()) }
```

### AP-2: Inline status/name literal

```go
// ❌ (internal/service/network.go::doCreate)
sg := &domain.SecurityGroup{
    Name:   "default-sg-" + shortNet,
    Status: "ACTIVE",
    Rules:  []domain.SecurityGroupRule{
        {Direction: "INGRESS", ProtocolName: "ANY", ProtocolNumber: -1, V4CidrBlocks: []string{"0.0.0.0/0"}},
        {Direction: "EGRESS",  ProtocolName: "ANY", ProtocolNumber: -1, V4CidrBlocks: []string{"0.0.0.0/0"}},
    },
}
```

```go
// ✅ Builder
sg := domain.NewDefaultSecurityGroup(net)   // domain.NewDefaultSGRules() внутри
```

### AP-3: Оторванный context в worker

```go
// ❌ (kacho-corelib/operations.Run)
go func() {
    workerCtx, cancel := context.WithTimeout(context.Background(), opTimeout)
    defer cancel()
    fn(workerCtx)   // trace/request-id/slog-attrs ПОТЕРЯНЫ
}()
```

```go
// ✅ Сохраняем metadata
workerCtx := context.WithValue(context.Background(), trace.ContextKey, trace.FromContext(callerCtx))
workerCtx, cancel := context.WithTimeout(workerCtx, opTimeout)
// + slog.Logger.WithContext(workerCtx), + request-id, + tenant-id
```

### AP-4: Magic numbers

```go
// ❌
if len(shortNet) > 8 { shortNet = shortNet[:8] }
```

```go
// ✅
const ShortIDLen = 8
shortNet := domain.TruncateID(created.ID)
```

### AP-5: Inline race-prone existence check

```go
// ❌ (folder/network existence checks перед Insert — race-prone)
exists, err := s.folderClient.Exists(ctx, req.FolderID)
if !exists { return ErrNotFound }
// ... через мгновение folder удалён ...
created, err := s.repo.Insert(ctx, n)   // упадёт через FK
```

```go
// ✅ Полагаться на FK / peer error (если есть). Sync-precheck — только soft-hint
// для UX (например для быстрого fail без создания Operation).
created, err := s.repo.Insert(ctx, n)
if errors.Is(err, ErrFolderNotFound) { return ... }
```

### AP-6: envconfig в struct tags

```go
// ❌ (internal/config/config.go)
type Config struct {
    DBHost string `envconfig:"KACHO_VPC_DB_HOST" default:"localhost"`
    DBPort string `envconfig:"KACHO_VPC_DB_PORT" default:"5432"`
    ...
}
```

```go
// ✅ viper + YAML
type Config struct {
    Repository struct {
        Postgres struct {
            URL string `mapstructure:"url"`
        } `mapstructure:"postgres"`
    } `mapstructure:"repository"`
}

// internal/apps/kacho/config/defaults.go
viper.SetDefault("repository.postgres.url", "postgres://kacho@localhost/kacho_vpc")
```

### AP-7: Goroutine fire-and-forget без error-prop

```go
// ❌ (cmd/vpc/main.go::runServe)
go func() {
    if err := internalSrv.Serve(internalListener); err != nil { logger.Error(...) }
}()
publicSrv.Serve(publicListener)   // продолжает крутиться даже если internal умер
```

```go
// ✅
parallel.ExecAbstract(ctx,
    func(ctx) error { return publicSrv.Serve(publicListener) },
    func(ctx) error { return internalSrv.Serve(internalListener) },
)
```

### AP-8: Bool вместо ENUM

```go
// ❌
productionMode bool
```

```go
// ✅
type Mode int
const (
    ModeDev Mode = iota
    ModeProduction
)
```

### AP-9: One binary с subcommand-mux

```go
// ❌ (cmd/vpc/main.go)
switch os.Args[1] {
case "serve":   runServe(cfg)
case "migrate": runMigrate(cfg, os.Args[2])
}
```

```go
// ✅
// cmd/kacho-vpc/main.go     → только server
// cmd/migrator/main.go      → только миграции, cobra-based
```

### AP-10: Голый `string` для labels/name/desc

```go
// ❌
type Network struct {
    Name        string
    Description string
    Labels      map[string]string
}
```

```go
// ✅
type Network struct {
    Name        RcName        // newtype с Validate()
    Description RcDescription
    Labels      RcLabels      // dict.HDict[LabelKey, LabelVal]
}
```

### AP-11: protoconv функции без DTO-реестра

```go
// ❌ (internal/protoconv/protoconv.go)
func Network(d domain.Network) *vpcv1.Network {
    return &vpcv1.Network{Id: d.ID, ...}
}
// caller:
return anypb.New(protoconv.Network(created))
```

```go
// ✅
// init() в dto/type2pb/network.go регистрирует трансфер
var dst *vpcv1.Network
if err := dto.Transfer(dto.FromTo(created, &dst)); err != nil { ... }
return anypb.New(dst)
```

### AP-12: TimeStamp в domain

```go
// ❌
type Network struct {
    CreatedAt time.Time   // <- DB-managed, не domain
}
```

```go
// ✅
// internal/domain/network.go
type Network struct { ID, Name, ... /* без CreatedAt */ }

// internal/repo/network/iface.go
type Network struct {
    domain.Network
    CreatedAt time.Time
}
```

### AP-13: per-field `update_*`/`replace_*` флаги вместо `FieldMask update_mask`

**Контракт partial-update — ОДИН на весь проект: `google.protobuf.FieldMask update_mask`.**
Любой `Update`-RPC выражает «какие поля менять» через `update_mask` + дисциплину
§4.4 (known-set / immutable / empty-mask=full-PATCH). Изобретать булевы флаги
вида `update_is_default` / `replace_labels` — **запрещено**, ресурсов. Две конвенции partial-update в одном
API — footgun.

```protobuf
// ❌ per-field флаги (AddressPool до unify) — отдельная конвенция, рассинхрон с
//    остальными Update-RPC, и UI/SDK о ней не знает.
message UpdateAddressPoolRequest {
  string pool_id = 1;
  bool   update_is_default = 7;   bool is_default = 8;
  bool   replace_labels = 4;      map<string,string> labels = 5;
  bool   update_selector_priority = 11; int32 selector_priority = 12;
}
```

```protobuf
// ✅ FieldMask (parity со всеми VPC/Compute/IAM Update-RPC)
message UpdateAddressPoolRequest {
  string pool_id = 1 [(required) = true];
  google.protobuf.FieldMask update_mask = 17;   // набор изменяемых полей
  string name = 2;  string description = 3;  map<string,string> labels = 5;
  bool is_default = 8;  map<string,string> selector_labels = 10;  int32 selector_priority = 12;
}
```

```go
// use-case (parity с SubnetService): immutable в mask → InvalidArgument,
// unknown → InvalidArgument (corevalidate.UpdateMask), пустой mask → full-PATCH.
for _, f := range req.UpdateMask {
    switch f {
    case "kind", "zone_id", "id", "created_at":
        return nil, serviceerr.InvalidArg(f, f+" is immutable after <Res>.Create")
    }
}
if err := corevalidate.UpdateMask("update_mask", req.UpdateMask, knownMutable); err != nil { return nil, err }
updates := req.UpdateMask
if len(updates) == 0 { updates = allMutableFields }  // full-PATCH
for _, f := range updates { /* применить только masked поля */ }
```

> **Реальный инцидент (cautionary tale):** AddressPool жил на per-field флагах,
> пока остальные 7 VPC-ресурсов — на `update_mask`. UI-форма строила `update_mask`
> (как для всех), бэкенд молча отбрасывал неизвестное поле + игнорировал
> `is_default` без `update_is_default` ⇒ `PATCH` отдавал `200`, но переключатель
> «Default» не применялся (silent no-op). Унифицировано на FieldMask;
> per-field флаги удалены из proto (`reserved`).

---

## §12. Step-by-step migration plan для kacho-vpc

Большой рефакторинг. Декомпозирован на 11 фаз. Каждая фаза — отдельный YT-эпик / отдельный PR. Зависимости — строгие (фаза N+1 не стартует до merge N).

### Фаза 1 — Domain types: newtypes + Validate (правила D.2–D.10)

**KAC-N1**:
1. Создать `internal/domain/types.go` с newtypes: `LabelKey`, `LabelVal`, `RcLabels`, `RcName`, `RcNameVPC`, `RcNameCompute`, `RcDescription`, `RcNameOpt`.
2. Добавить `Validate() error` на каждый newtype (переносим логику из `corevalidate.NameVPC`/`Description`/`Labels`).
3. Поменять поля domain-типов (Network, Subnet, Address, SG, RT, Gateway, PE, NIC) с `string`/`map` → newtypes.
4. Добавить `Validate()` на каждый domain-тип через `multierr.Combine`.
5. **CreatedAt вынести в repo-уровень**: `internal/repo/.../entity.go::Entity = struct { domain.X; CreatedAt time.Time }`.
6. Service-слой: убрать вызовы `corevalidate.*` — заменить на `obj.Validate()`. Удалить `service/validate.go` если становится пустым.
7. **DB CHECK constraints** (правило E.2): миграции добавляют CHECK для name (regex), description (length), status (enum-set) per resource.
8. Integration-тесты: для каждого resource — assert DB отбивает invalid INSERT с SQLSTATE 23514.
9. PR per resource (Network → Subnet → Address → SG → RT → Gateway → PE → NIC) — 8 PR'ов.

### Фаза 2 — Domain builders + constants (правила D.7–D.9, AP-2/4)

**KAC-N2**:
1. Создать `internal/domain/security_group_builders.go`:
   - `NewDefaultSecurityGroup(net Network) SecurityGroup`
   - `NewDefaultSecurityGroupRules() []SecurityGroupRule`
   - `DefaultSGName(networkID string) string` (helper `"default-sg-" + TruncateID(id, ShortIDLen)`)
2. Аналогично — builders для других «inline-собираемых» сущностей (если есть): `NewAddress` для allocate-flow, `NewSubnet` для auto-association, etc.
3. Все status enum'ы — в `internal/domain/status.go` со строгими константами и Validate().
4. Magic numbers — в `internal/domain/constants.go`.
5. Service-слой переписать на использование builders.

### Фаза 3 — UseCases вместо Services (правила B.1–B.4)

**KAC-N3** (per resource → 8 субтасков):
1. Создать `internal/apps/kacho/api/<resource>/` директорию.
2. Перенести логику из `internal/service/<resource>.go` в:
   - `internal/apps/kacho/api/<resource>/handler.go` — gRPC transport (тонкий)
   - `internal/apps/kacho/api/<resource>/create.go` — `CreateXxxUseCase`
   - `internal/apps/kacho/api/<resource>/update.go`
   - `internal/apps/kacho/api/<resource>/delete.go`
   - `internal/apps/kacho/api/<resource>/move.go` (где есть)
   - etc.
3. Удалить параллельные `CreateXxxReq`/`UpdateXxxReq` если зеркалят domain — принимать `domain.X` напрямую.
4. После полной миграции — `internal/service/` удалить.

### Фаза 4 — DTO table-driven (правила C.1–C.6)

**KAC-N4**:
1. Создать `internal/dto/base.go` с generic Interface, RegTransfer, FindTransfer (из PR #52).
2. Создать `internal/dto/type2pb/` (или `internal/repo/kacho/pg/dto/` для pg-mapping):
   - `network.go` с `network{}.toPb` (по образцу из PR #52).
   - `subnet.go`, `address.go`, ... для каждого ресурса.
   - `time.go` с `timeObj{}.toPb` (truncate to seconds).
3. Type-set generic `types2ProtoVariants` расширяется для каждого нового ресурса.
4. Use-cases переписать на `dto.Transfer(dto.FromTo(...))`.
5. Удалить `internal/protoconv/protoconv.go`.

### Фаза 5 — Ports → CQRS Repository (правила G.1–G.7)

**KAC-N5**:
1. Создать новые интерфейсы:
   - `internal/repo/kacho/iface.go` с `Repository`, `RepositoryReader`, `RepositoryWriter`.
   - `internal/repo/kacho/<resource>/iface.go` с `XxxReaderIface` / `XxxWriterIface`.
2. Реализация `internal/repo/kacho/pg/` с pgxpool — `Reader(ctx)` открывает TX read-only, `Writer(ctx)` — RW.
3. `pg/dto/` — маппинг `domain.X ↔ pgmodel.X` (если pgmodel отличается от domain, например JSONB-сериализация).
4. Use-cases переписать на `repo.Writer(ctx)` / `repo.Reader(ctx)`.
5. Удалить старые `NetworkRepo`, `SubnetRepo`, ... в `internal/ports/`.
6. Mock'и `portmock` пересоздать под новый Repository.

### Фаза 6 — Config: YAML + viper (правила J.1–J.7)

**KAC-N6**:
1. Создать `internal/apps/kacho/config/`:
   - `config.go` — типы config с `mapstructure`-тегами.
   - `defaults.go` — `func RegisterDefaults(v *viper.Viper)`.
   - `validate.go` — `Config.Validate() + Mode` ENUM (вместо bool).
   - `load.go` — `Load(path string) (Config, error)`.
2. `cmd/kacho-vpc/main.go` загружает config через viper.
3. ENV variables — через `viper.SetEnvPrefix("KACHO_VPC")` + `SetEnvKeyReplacer(strings.NewReplacer(".", "__"))`.
4. Удалить `internal/config/config.go` (envconfig).
5. Helm chart: `values.yaml` → ConfigMap mount `/etc/kacho-vpc/config.yaml`.

### Фаза 7 — Отделить cmd/migrator (правила K.1–K.3)

**KAC-N7**:
1. Создать `cmd/migrator/main.go` с cobra: `migrator up|down|status|create --dialect=postgres --dsn=...`.
2. `internal/apps/migrator/` — бизнес-логика, обёртка над goose.
3. `cmd/kacho-vpc/main.go` оставить **только** `serve`. Subcommand `migrate` удалить.
4. Docker entrypoint init-container — `kacho-migrator up`, main container — `kacho-vpc serve`.

### Фаза 8 — Параллельные серверы через ExecAbstract (правила K.4–K.5)

**KAC-N8**:
1. Импорт `H-BF/corlib/pkg/parallel`.
2. `cmd/kacho-vpc/main.go::runServe`: `parallel.ExecAbstract(ctx, publicServer, internalServer, shutdownWaiter)`.
3. Failure isolation: если internal упал — public останавливается через ctx cancel.

### Фаза 9 — grpc client-builder (правило K.6)

**KAC-N9**:
1. Импорт `H-BF/corlib/client/grpc`.
2. Все clients (`folderClient`, `geographyClient`, ...) через `client-builder.Build(...)` — единый паттерн с retries/LB/TLS/metrics.
3. Удалить `dialResourceManager` / `dialCompute` / ... — заменить.

### Фаза 10 — operations.Run — preserve context metadata (правило I.3)

**KAC-N10** — затрагивает `kacho-corelib`:
1. В `kacho-corelib/operations/run.go`: workerCtx наследует `trace-id`, `request-id`, `slog-attrs` из callerCtx (через явный copy of context.Values).
2. Timeout остаётся независимым от caller-deadline (worker может пережить request).
3. Все use-cases используют новый Run без изменений (signature та же).

### Фаза 11 — ER-diagrams + architecture docs (правило E.6)

**KAC-N11** (для каждого сервиса):
1. `docs/architecture/er-diagram.md` — mermaid ER-diagram таблиц.
2. Обновление `kacho-vpc/CLAUDE.md` §2 (Domain model) с ссылкой на диаграмму.

### Зависимости фаз

```
Фаза 1 (domain types) ──┬──→ Фаза 2 (builders)
                        ├──→ Фаза 4 (DTO) ──→ Фаза 3 (usecases) ──→ Фаза 5 (CQRS repo)
                        └──→ Фаза 11 (ER docs)

Фаза 6 (config) ──→ Фаза 7 (cmd/migrator) ──→ Фаза 8 (ExecAbstract)
                                                    │
                                              Фаза 9 (client-builder)

Фаза 10 (corelib/operations) — независимо, влияет на все фазы 3+.
```

**Оценка**: 11 фаз × ~3 дня каждая = ~33 рабочих дня (~6-7 недель). Декомпозируется в эпик KAC-N с 11 subtask'ами + per-resource sub-subtask'и.

---

## §13. Review checklist

Применять при PR-review любого изменения в kacho-vpc / kacho-compute / kacho-resource-manager:

### Structure
- [ ] Нет нового кода в `internal/service/` — всё в `internal/apps/<svc>/api/<resource>/use-cases/`.
- [ ] Нет нового CLI use-case в `cmd/<svc>/main.go` — отдельный binary.
- [ ] Нет нового `XxxRepo` interface — расширяется `XxxReaderIface` / `XxxWriterIface`.

### Domain
- [ ] Новые поля — newtypes (не голый `string`), у каждого `Validate()`.
- [ ] Domain-тип имеет `Validate()` — multierr из всех полей.
- [ ] Нет `CreatedAt time.Time` в domain — только в repo-сущности.
- [ ] Нет magic numbers / inline status / inline names — все в `domain/constants.go` или через builders.

### Service / UseCase
- [ ] Нет `corevalidate.*` вызовов в service-слое — domain.Validate().
- [ ] Нет inline literal'ов domain-сущностей — builders (`domain.NewXxx(...)`).
- [ ] Нет `XxxReq`/`XxxResp`, дублирующих domain.
- [ ] `operations.Run` worker получает ctx с copied baggage (trace/request-id/slog-attrs).
- [ ] Нет race-prone `Exists`-prechecks перед Insert (полагаемся на FK).
- [ ] `Update`-RPC использует `google.protobuf.FieldMask update_mask` (НЕ per-field `update_*`/`replace_*` флаги — AP-13); known-set/immutable дисциплина §4.4; пустой mask → full-PATCH.

### DTO
- [ ] Нет ручных `func ToPb(d domain.X) *pbX` — через `dto.RegTransfer` + `dto.Transfer(dto.FromTo(...))`.
- [ ] Новый ресурс — добавлен в `types2ProtoVariants` type-set.

### Ports / Repository
- [ ] CQRS-разделение: Reader / Writer interfaces.
- [ ] UseCase открывает TX явно через `repo.Writer(ctx)` / `repo.Reader(ctx)`.

### Config
- [ ] Новые опции — в YAML config (`mapstructure`-теги), не envconfig struct-tags.
- [ ] Default'ы — в `defaults.go`, не в struct-tags.
- [ ] Bool-флаги с «режимами» — заменены на ENUM.

### cmd
- [ ] Не добавлен новый subcommand в основной binary — новая CLI = новый `cmd/<name>/`.
- [ ] Параллельные goroutine'ы через `parallel.ExecAbstract`.
- [ ] gRPC-client через `H-BF/corlib/client/grpc/client-builder`.

### DB
- [ ] CHECK constraints на новых regex/length/enum-полях (parity с domain.Validate).
- [ ] FK / UNIQUE / EXCLUDE / partial UNIQUE — в той же миграции что и таблица.
- [ ] ER-диаграмма обновлена.

---

## §14. Зависимости (libraries из ревью)

- `github.com/H-BF/corlib` — основная общая библиотека (newtypes, option, parallel, grpc client-builder, dict).
- `github.com/H-BF/corlib/pkg/dict` — `HDict[K, V]` (для RcLabels и RegTransfer).
- `github.com/H-BF/corlib/pkg/option` — `ValueOf[T]` (для RcNameOpt).
- `github.com/H-BF/corlib/pkg/parallel` — `ExecAbstract` (K.4).
- `github.com/H-BF/corlib/client/grpc` — `client-builder` (K.6).
- `github.com/pkg/errors` — `errors.WithMessagef` для DTO error-wrap.
- `github.com/spf13/viper` или `github.com/knadh/koanf` — config (J.2).
- `github.com/spf13/cobra` или `github.com/alecthomas/kong` — CLI (K.2).
- `go.uber.org/multierr` — для `Validate() error` композиции.

Добавление зависимости в go.mod — обязательно с pinned-version (не latest), upgrade — отдельным PR.

---

## §15. Ссылки

- PR PRO-Robotech/kacho-vpc#52 (review @EvgenyGRI / @pointpu, 2026-05-14).
- Commit `9d865df` — оригинальный snapshot предложений.
- Workspace `CLAUDE.md` §«Чистая архитектура», §«Within-service refs — DB-уровень обязателен», §«Запреты» — синхронизировано с этим skill.
- kacho-vpc `CLAUDE.md` §«Архитектурные паттерны (VPC-специфичные)» — должен быть синхронизирован с §1-§3 этого skill после Фазы 3-5 migration plan.

---

**Все 48 правил выше — нормативные.** При ревью / при кодинге — sanity-check против §13 checklist. Если правило конфликтует с workspace `CLAUDE.md` или kacho-vpc `CLAUDE.md` — приоритет у workspace (запрет #1: «не начинать без acceptance»; запрет #10: «within-service refs — DB-уровень»; запрет #11: «PR обязан содержать тесты»). evgeniy-skill — code-style на верхнем уровне, не отменяет нижнюю инфраструктуру.
