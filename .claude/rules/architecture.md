# Чистая архитектура + переиспользование

## Clean Architecture (строгое dependency rule)

```
handler ─┐
         ├─→ service/use-case ─→ domain
repo ────┤                       ↑
clients ─┘                       │  (только структуры)
```

`internal/`:
- `domain/` — entities (чистый Go: ТОЛЬКО stdlib + `kacho-proto`). Без pgx/grpc/internal-зависимостей.
- `apps/kacho/api/<resource>/` (use-cases) — бизнес-логика; определяет port-интерфейсы
  (`<Resource>Repo`, `<Peer>Client`); импортирует domain + порты. **Не импортирует transport-слой.**
- `repo/` — adapter: реализует порты, импортирует pgx + domain.
- `clients/` — adapter: реализует порты, импортирует grpc-stubs + domain.
- `handler/` — тонкий transport: parse → use-case → format. **Никакой бизнес-логики.**
- `cmd/<svc>/main.go` — **единственное** место wiring (composition root).
- `internal/tenant/` (или аналог) — нейтральный носитель caller-identity, чтобы use-case
  не зависел от transport (`internal/handler`).

**Запрещено**: `domain`/use-case импортируют pgx/grpc-stubs/sqlc-types; бизнес-логика в
`handler`; глобальные синглтоны (`var globalPool`, `init()`-side-effects) вне `cmd/`.

Тесты по слоям: unit use-case через mock-порты; integration через testcontainers;
e2e через api-gateway. Если service-тест требует Postgres — это утечка adapter в use-case.

Каноничный Go-style ruleset — skill **`evgeniy`** (UseCase pattern, CQRS-порты,
self-validating domain, DTO-таблицы, YAML-config через viper/koanf, отдельный `cmd/migrator`).

## Переиспользование через `kacho-corelib`

Всё горизонтальное (нужное 2+ сервисам) — в `kacho-corelib/<package>/`, не дублировать per-service.
Что там живёт: `ids/`, `errors/`, `config/`, `observability/`, `db/` (pgx pool + transactor),
`grpcsrv/`, `grpcclient/`, `outbox/`, `selector/`, `operations/` (LRO table + Worker + Repo),
`retry/`, `shutdown/`, `backoff/`, `validate/`, `auth/`, `authz/`, `filter/`, `migrations/common/`, `audit/`.

Перед написанием новой утилиты в сервисе — проверь corelib. Будет нужно 2+ сервисам →
сразу в corelib. **Исключение**: доменная бизнес-логика (VPC ref-validation, Compute
reconciler) живёт в сервисном репо.
