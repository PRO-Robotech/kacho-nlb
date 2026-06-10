# Целостность данных: within-service (DB-уровень) + cross-domain (peer-API)

## Within-service инварианты — ТОЛЬКО на DB-уровне (ban #10)

Внутри одной БД сервиса каждая ссылочная зависимость и инвариант **обязан** быть
выражен DB-конструкцией. Software-side `Get → check → Update` (TOCTOU) запрещён —
он race-prone (реальный инцидент: NIC-attach 2026-05-14, две Create прошли
software-guard и оба сделали безусловный UPDATE → second-writer-wins).

| Инвариант | DB-механизм |
|---|---|
| id обязан существовать в той же БД | `FK REFERENCES <t>(id) ON DELETE {RESTRICT\|CASCADE\|SET NULL}` |
| поле уникально | `UNIQUE` / `CREATE UNIQUE INDEX` |
| уникально только если поле непусто | partial `UNIQUE … WHERE <cond>` |
| range не пересекается | `EXCLUDE USING gist (… WITH &&)` |
| простой предикат | `CHECK (…)` |
| атомарный compare-and-swap | `UPDATE … WHERE <expected-state> RETURNING …` + проверка кардинальности |
| read-modify-write OCC без колонки версии | `xmin::text` snapshot + `UPDATE … WHERE xmin::text=$exp` |
| уникальная аллокация из пула под concurrency | `FOR UPDATE SKIP LOCKED LIMIT 1` + `DELETE … RETURNING` |
| сериализовать read-modify-write набора | `SELECT … FOR UPDATE` перед merge+write |

Service-слой только маппит SQLSTATE → gRPC: `23503`→FailedPrecondition,
`23505`→AlreadyExists/FailedPrecondition (по контексту), `23514`→InvalidArgument,
`23P01`→FailedPrecondition. **Никогда не leak'ай pgx-текст наружу** (→ фикс. INTERNAL).

### Шаблон attach / смена ownership — атомарный CAS (не TOCTOU)

```sql
UPDATE <table>
   SET <owner-col> = $new, <other…>
 WHERE id = $id
   AND (<owner-col> = '' OR <owner-col> = $new)   -- свободно ИЛИ уже наш (идемпотентно)
RETURNING …;
```
0 rows из RETURNING → `pgx.ErrNoRows` → `FailedPrecondition`. Single-statement UPDATE на
одной row защищён row-lock'ом: параллельный writer ждёт commit, видит обновлённый row,
CAS не matches → 0 rows. Доп. UNIQUE-индекс как «backstop» — НЕ нужен (и для
one-resource-per-owner-or-many семантики он ложно ловит нормальный multi-attach).

### Чек-лист нового ссылочного поля / инварианта

1. Ссылка на ресурс в **той же БД** → FK (+ partial UNIQUE/EXCLUDE при необходимости). Никогда software-only.
2. Условная уникальность → partial `UNIQUE … WHERE`.
3. Состояние меняется конкурирующими путями (attach/detach, allocate/free) → атомарный CAS.
4. SQLSTATE→gRPC в `mapRepoErr`/serviceerr.
5. **Integration-тест (testcontainers) с concurrent goroutines** на спорный путь — ровно одна
   транзакция проходит, остальные получают ожидаемый sentinel. Без него не мёржим (race не ловится unit-тестом).

## Cross-domain ссылки (owner-сервис / consumer-сервис)

Через границу сервиса FK невозможен (DB-per-service, ban #4/#8). Регламент:

1. **Один владелец на тип ресурса** — канонический CRUD/read-API. Consumer'ы не держат mirror-строк, нет cross-service FK.
2. **Consumer ссылается по id (TEXT, без FK), валидирует через API владельца** на request-path
   (`Create`/`Update`): типизированный gRPC-клиент `internal/clients/<owner>_client.go` (port в use-case,
   impl в `clients/`). Не найдено/не то состояние → `InvalidArgument`/`FailedPrecondition`; владелец
   недоступен → `Unavailable` (fail-closed для мутаций). Вызовы — service→service напрямую (не через api-gateway).
3. **Денормализованные зеркала** (показать имя/статус чужого ресурса) — output-only, помечены
   «source of truth = `<owner>.<Resource>`», обновляются на чтении, не источник истины, не на вход Create/Update.
4. **Удаление**: владелец не спрашивает consumer'ов (нет cross-service cascade). Consumer обязан
   грациозно переживать dangling-ref (деградированный статус, не паника). Жёсткие гарантии — только same-schema FK.
5. **Карта владельцев**: Geography (Region/Zone) → `kacho-compute`; IAM (Account/Project/User/SA/Group/Role/AccessBinding) → `kacho-iam`;
   Network/Subnet/SG/RouteTable/Address/Gateway/NetworkInterface → `kacho-vpc`; Instance/Disk/Image/Snapshot/DiskType → `kacho-compute`;
   Operation — per-service (общая `operations`-таблица из corelib).
6. Новое cross-domain ребро — фиксируется в `polyrepo.md` (runtime-edge); циклы запрещены.
