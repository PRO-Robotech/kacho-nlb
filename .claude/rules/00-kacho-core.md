# Kachō — ядро: продукт, naming, non-negotiables

Базовый, всегда-загруженный модуль. Импортируется как корневым workspace
`CLAUDE.md`, так и `CLAUDE.md` каждого сервисного репо (self-sufficient: репо
работает и при standalone-клоне, без workspace-родителя). Источник истины —
workspace; копии во всех репо синхронизируются `./sync-tooling.sh` — **не
редактировать копию в репо**, правка только в `kacho-workspace/.claude/rules/`.

## Что это за продукт

**Kachō — самостоятельная облачная control-plane платформа** (только control plane,
без data plane). Домены: **IAM** (Account / Project / User / ServiceAccount / Group /
Role / AccessBinding), **VPC** (Network / Subnet / SecurityGroup / RouteTable / Address /
Gateway / NetworkInterface), **Compute** (Instance / Disk / Image / Snapshot + Geography
Region/Zone). Это собственный продукт со своими требованиями — описывай и проектируй
API в терминах **конвенций Kachō** (см. `@.claude/rules/api-conventions.md`), без
сравнений с чужими облаками.

## Naming convention (обязательно)

| Контекст | Значение |
|---|---|
| Бренд / README / UI | **Kachō** |
| Технические идентификаторы (ASCII) | `kacho` |
| Proto package | `kacho.cloud.<domain>.v1` |
| Имена репо | `kacho-<part>` (дефис) |
| Postgres database / schema | `kacho_<domain>` (подчёркивание) |
| Env-переменные | `KACHO_<DOMAIN>_<NAME>` |
| JSON-поля (REST) | camelCase: `<resource>Id`, `projectId`, `labels`, `createdAt` |

## Non-negotiables (детали — в соответствующих rule-модулях)

1. **Не кодить без APPROVED acceptance-дока** Given-When-Then (gate: `acceptance-reviewer`).
2. **Никаких упоминаний чужих облаков** в коде/доках/комментариях/env/именах (`yandex`, `aws`, …).
3. **Без ORM** — только sqlc + handwritten pgx.
4. **Без каскадного удаления через границу сервиса** (только same-DB FK cascade).
5. **Не редактировать применённую миграцию** — только новая.
6. **`Internal.*` методы не публиковать на external endpoint** (только cluster-internal :9091).
7. **Без брокера** (Kafka/NATS), пока справляется in-process.
8. **Без общих БД** — database-per-service.
9. **Мутации возвращают `Operation`** (async), не ресурс синхронно.
10. **Within-service инварианты — на DB-уровне** (FK/UNIQUE/EXCLUDE/CHECK/CAS), не software check-then-act.
11. **Никакого тех-долга / TODO «на потом»** — закрываем в том же PR.
12. **Строгий TDD** — падающий тест ДО кода; новый RPC/поле/ресурс/багфикс не мёржится без тестов в том же PR.
13. **Test-only PR не трогает прод-код** и не содержит TODO/SKIP/FIXME.
