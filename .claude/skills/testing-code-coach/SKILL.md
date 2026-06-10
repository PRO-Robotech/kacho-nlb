---
name: testing-code-coach
description: Use when designing, writing, or reviewing tests for production code (unit, integration, contract, e2e through bufconn). Applies to Go services in Kachō (kacho-vpc, kacho-api-gateway, kacho-corelib). Knows Clean Architecture layering — which layer gets which test type, what mocks are allowed where, and how to detect adapter leakage into use-cases. Owns test pyramid, time budgets, naming conventions, AAA structure, and 13 anti-patterns. Defers product-level QA (Newman, conformance, exploratory) to testing-product-coach.
---

# Skill: testing-code-coach

## 1. Когда меня вызывают

- Разработчик пишет новый сервис / RPC / use-case и нужен план unit-тестов.
- Code review теста: оценить структуру, моки, naming, скоуп.
- Сломалось — нужно понять, какой уровень теста должен был это поймать.
- Обсуждение test pyramid и time budgets для нового модуля.
- Подбор техники (table-driven / property-based / mutation / fuzz) под задачу.
- Распознать утечку адаптера в use-case (unit-тесту нужна реальная БД).
- Объяснить отличие fake / stub / mock / spy и почему fake обычно предпочтительнее.

## 2. Когда меня НЕ вызывать

- Расширение Newman regression suite — это `testing-product-coach` или `qa-test-engineer`.
- Conformance с каноническими Kachō текстами/контрактом ошибок — `vpc-conventions-auditor`.
- Спорные кейсы в CIDR / EXCLUDE — `vpc-cidr-specialist`.
- Outbox / Watch testing с реальной БД — `vpc-outbox-watch-engineer` + я.
- Acceptance-spec — `acceptance-author`.

## 3. Что я отдаю на выходе

- План тестов для нового модуля (какие классы кейсов, какой уровень, какие моки).
- Review теста с конкретными замечаниями (хрупкое место → конкретный рефакторинг).
- Чек-лист "что недотестировано" в патче.
- Кандидаты в quarantine / deletion (flaky / тавтологичные / тестирующие фреймворк).

## 4. Что я НЕ делаю

- Не пишу код тестов сам — выдаю план + критерии. Реализация — у разработчика.
- Не оцениваю функциональную правильность продукта — для этого спека.
- Не утверждаю Newman-suite — это QA-уровень.

## 5. Входные сигналы для review

| Сигнал в патче | Реакция |
|---|---|
| Unit-тесту нужен testcontainer | Утечка адаптера в use-case — рефактор за порт |
| `time.Sleep(N ms)` в тесте | Replace на `assert.Eventually` или signal-channel |
| Mock с `.Times(1)` / `.InOrder(...)` | Interaction-based: переписать через fake + state-check |
| 100% совпадение mock.expect и реализации | Тавтологичный тест — удалить |
| Test name `TestFoo1`/`TestFoo_OK` | Перенейминг по шаблону `Test<Subject>_<Action>_<Outcome>` |
| Кейсы копи-пастой 20 раз | Перепаковать в table-driven |
| Repo-тест на мокнутом pgxpool | Перевести на testcontainers |
| Падает 1 раз в неделю | Quarantine + root-cause issue, не retry |

## 6. Ссылки в knowledge base ниже

Полное knowledge-body начинается ниже с раздела «Эталонные практики».
Структура: 10 частей + 3 приложения. Я обращаюсь к ним при ответе на
вопросы о конкретных техниках.

---

# Эталонные практики тестирования прикладного кода

Документ описывает, как тестировать прикладной код в Kachō: какие
уровни тестов выделять, какие техники применять, что считать
анти-паттернами и как это вяжется со стеком Go + pgx + gRPC +
Postman/Newman.

Документ описывает принципы и техники. Конкретные примеры команд
запуска тестов — в `CLAUDE.md` и `README.md` каждого сервиса.

---

## Часть I. Базовые принципы

### 1.1 Тест проверяет наблюдаемое поведение, а не реализацию

Тест должен быть chreased в терминах "при входе X и состоянии S система
возвращает Y и переводит S в S'". Внутренние шаги, имена приватных
функций, порядок вызовов соседних методов — это детали реализации.
Меняешь реализацию, сохраняя контракт — тест должен продолжать работать.

Сигнал что тест слишком привязан к реализации: для безобидного рефакторинга
приходится править десятки тестов. Это shared-mutable-state на уровне
test suite.

### 1.2 Независимость

Каждый тест — самостоятельный сценарий:

- готовит своё состояние,
- не полагается на порядок,
- не оставляет следов (cleanup в `defer`/`AfterEach`),
- не использует общее состояние между тестами (включая глобальные
  переменные, singleton'ы, persistent connections).

Параллельный прогон не должен ломать ничего. Если ломается — тесты
зависят друг от друга, а это запах.

### 1.3 Детерминизм

Тесты не падают случайно. Никакой зависимости от:

- системного времени без freezer'а,
- random'а без seed,
- сетевых таймаутов "обычно достаточно",
- TLS/DNS-зависимостей в unit-тестах,
- порядка items в map,
- race conditions, которые "обычно не воспроизводятся".

Flaky-тест → quarantine + root-cause investigation. Никаких retry-плагинов
"чтобы зелёный CI" — это легализация бага.

### 1.4 AAA / Given-When-Then

Структура каждого теста:

```
Arrange  (Given)  — подготовка входа и состояния
Act      (When)   — единичное действие, проверяемое тестом
Assert   (Then)   — проверка результата + side-effects
```

Это не догма, а форма для читабельности. Видишь `if/else` внутри теста —
скорее всего, это два теста, перепиши таблицей.

### 1.5 Coverage — симптом, не цель

Покрытие 100% не означает работает; 60% по критическим веткам лучше
95% по геттерам. Coverage полезен как **detector mead code**: если
строка не покрыта ни одним тестом — спроси, зачем она в коде.

### 1.6 Тест — это документация

Имя теста читается как утверждение: `TestNetwork_Create_ReturnsAlreadyExists_OnDuplicateName`.
Чтение списка тестов файла должно объяснить, что делает модуль, без
открытия исходника.

### 1.7 Стоимость теста

У каждого теста есть цена:

- **сборки** (CI время),
- **запуска** (на каждом PR),
- **поддержки** (синхронизация при рефакторинге),
- **диагностики** (когда падает — насколько просто понять причину).

Сравнивай эти издержки с выгодой. Дешёвый unit-тест на boundary —
почти бесплатный. Дорогой e2e на хеппи-пас, который покрыт unit-тестом
— часто чистый минус.

### 1.8 Симметрия "что покрываем"

Для каждой ветки кода — соответствующий тест. Для каждого инварианта
(идемпотентность, race-free, FK consistency) — тест-инвариант.
Симметрия не означает "тест на каждый файл", означает "ни одна важная
ветка не остаётся без проверки".

---

## Часть II. Пирамида тестов

```
                  ┌─────────┐
                  │   E2E   │   единицы / десятки     медленно
                  └─────────┘
                ┌─────────────┐
                │ Integration │   десятки / сотни     средне
                └─────────────┘
              ┌──────────────────┐
              │       Unit       │   сотни / тысячи   быстро
              └──────────────────┘
```

### 2.1 Уровни

| Уровень | Что проверяет | Деpencies | Скорость | Доля от общего |
|---|---|---|---|---|
| Unit | Чистая логика одной функции/use-case с моками портов | Только in-process | ~ms | 70-80% |
| Integration | Адаптер (repo, gRPC client) против реальной зависимости | Testcontainers / spawned servers | ~ds-секунды | 15-25% |
| Contract | Соблюдение proto-контракта между сервисами | Stubs/spec | ~ms | 5-10% |
| E2E | Полный путь через api-gateway | Весь dev-стенд | ~секунды-минуты | 5-10% |

### 2.2 Time budget

| Уровень | Допустимое время одного теста | Допустимое время всего suite |
|---|---|---|
| Unit | < 100ms | < 30s |
| Integration | < 5s | < 5min |
| E2E | < 30s | < 30min (с распараллеливанием) |

Если unit-тест длится 2 секунды — это сигнал об утечке адаптера
(I/O просочился в use-case) или о sleep'ах. Чини источник, а не время.

### 2.3 Антиперевёрнутые пирамиды

- **Cup-cake** (e2e на всё): медленно, флакает, фиксы дорогие.
- **Hourglass** (много unit + много e2e, тонкий integration): дыра
  на адаптерах, integration-баги ловятся только в e2e.
- **Iceberg** (всё в unit, нет integration): SQL/proto не тестируется,
  всплывает в проде.

Целевая форма — классическая пирамида.

---

## Часть III. Уровни тестирования по слоям Clean Architecture

```
handler ─────►  HandlerService unit + grpc contract
  │
service ─────►  Service unit с моками портов
  │
domain  ─────►  Pure unit (если есть поведение)
  │
repo    ─────►  Integration с реальной Postgres
  │
clients ─────►  Contract test против fake gRPC server
```

### 3.1 `domain/` — pure unit

Если у entity есть метод с логикой (например `Subnet.ContainsIP(ip)`),
он тестируется чистым unit'ом без моков. Если entity — только данные,
тестов не нужно.

Сигнал: тестам `domain/` для запуска нужен `pgx` — это утечка адаптера.

### 3.2 `service/` — unit с моками портов

Use-cases тестируются через моки `*Repo`, `ProjectClient` и других портов.
Это единственный слой, где допустимы mocks/stubs. Тесты этого слоя
доминируют по количеству.

Сигнал: тестам `service/` нужен Postgres — это нарушение dependency
rule (адаптер просочился в use-case).

### 3.3 `repo/` — integration с реальной БД

Testcontainers + Postgres 16. Покрываем:

- happy-path CRUD,
- UNIQUE/EXCLUDE/FK constraint поведение (SQLSTATE → sentinel),
- generated columns / triggers (outbox emit),
- query semantics (фильтры, пагинация, ORDER BY).

Сигнал: тесты repo гоняются через моки `pgxpool` — это бесполезно,
потому что моки не воспроизводят SQL.

### 3.4 `handler/` — thin transport unit

Тонкий слой: parse-request → service → format-response. Тестируем:

- маппинг proto-поля → service-вызов,
- маппинг service-ошибки → gRPC-код,
- AuthZ-check (`AssertProjectOwnership`),
- pagination/page_size limits.

Mock — это сам сервис (его port-интерфейс или конкретный fake).

### 3.5 `clients/` — contract test

gRPC-клиенты к соседним сервисам тестируются против **fake gRPC server**,
который реализует тот же proto-контракт. Цель — гарантировать, что наш
клиент правильно сериализует/десериализует request/response и обрабатывает
все коды соседа. Это **contract test со стороны consumer'а**.

### 3.6 E2E через api-gateway

Полные сценарии: создал Network → дождался Operation → создал Subnet →
Allocate IP. Newman-suite или аналог. Покрывает интеграцию между
сервисами + REST-маппинг grpc-gateway.

---

## Часть IV. Техники тестирования

### 4.1 Test Doubles

| Тип | Назначение | Использовать когда |
|---|---|---|
| Dummy | Заполняет аргумент, не вызывается | Параметр обязательный, но не нужен в сценарии |
| Stub | Возвращает захардкоженный ответ | Нужна управляемая reaction зависимости |
| Fake | Упрощённая, но рабочая реализация | In-memory repo для unit-тестов use-case |
| Spy | Stub + запись вызовов | Проверка факта вызова с конкретными аргументами |
| Mock | Stub + проверка ожидаемых вызовов | Interaction-based testing (редко нужно) |

Правило: **предпочитай Fake перед Mock**. Fake тестирует поведение,
mock тестирует реализацию (порядок вызовов, конкретный аргумент).
Mock хрупок к рефакторингу.

### 4.2 Test Data Builders

Вместо повторения подготовки данных в каждом тесте — builder с
дефолтами, который можно tunить под кейс:

```
builder.NewSubnet().
    WithNetwork(net.ID).
    WithCIDR("10.0.0.0/24").
    Build()
```

Дефолты валидные → тест читается короче, изменяемое поле подсвечивается.

### 4.3 Object Mother

Альтернатива builder'у для часто используемых сущностей: статические
методы возвращают "канонические" объекты (`mother.AdminUser()`,
`mother.NetworkInProject(projectID)`). Хорошо для shared fixtures.

### 4.4 Property-Based Testing

Вместо ручных кейсов — генерация большого количества случайных входов
с проверкой инварианта.

Пример: для `pickRandomIPv4(cidr) → ip` инвариант "IP всегда внутри
CIDR и не network/broadcast". Property-test генерирует 1000 случайных
CIDR, проверяет инвариант. Ловит off-by-one на boundary.

Go: `testing/quick`, `gopter`, `rapid`.

### 4.5 Boundary Value Analysis (BVA)

Тесты на границах эквивалентных классов:

| Параметр | Границы для проверки |
|---|---|
| page_size | 0, 1, defaultPageSize-1, defaultPageSize, defaultPageSize+1, max, max+1 |
| length name | 0, 1, MaxNameLen-1, MaxNameLen, MaxNameLen+1 |
| CIDR prefix | /0, /1, /31, /32 (v4), /128 (v6) |
| number of labels | 0, 1, MaxLabels-1, MaxLabels, MaxLabels+1 |

В Newman есть отдельный класс `BVA-*` именно для этого.

### 4.6 Equivalence Partitioning

Разбиение входа на эквивалентные классы → один тест на класс. Не нужно
писать 100 unit-тестов на каждый IP — достаточно по одному на:
ipv4-loopback, ipv4-rfc1918, ipv4-public, ipv6-link-local, garbage,
empty.

### 4.7 Combinatorial Testing

Для функций с N параметрами и K значениями — `pairwise` (all-pairs).
Покрывает 90% багов взаимодействия N параметров, имея ~K² тестов
вместо K^N.

Например для `Create` с (project_id ∈ {valid, missing, garbage}) ×
(name ∈ {empty, valid, too_long}) × (labels ∈ {nil, empty, valid,
invalid}) → не 27 тестов, а ~9 pairwise-комбинаций + критические
N-way (например, все три невалидные сразу).

### 4.8 State-based vs Interaction-based

- **State-based** (предпочтительно): после действия проверяем итоговое
  состояние системы. Устойчиво к рефакторингу.
- **Interaction-based**: проверяем что вызвался конкретный mock с
  конкретными аргументами. Хрупко.

Mocks тянут к interaction-based, fakes — к state-based. Выбирай fake.

### 4.9 Golden Master / Snapshot

Для сложных выходных артефактов (большой JSON-объект, generated SQL,
HTML-fragment) сравнение с эталонным snapshot'ом. Полезно для
proto-форм и regex-проверок канонического контракта ошибок Kachō.

Анти-паттерн: snapshot обновляется автоматически при изменении output —
тест становится no-op. Snapshot всегда обновляется руками с
рассмотрением diff'а.

### 4.10 Mutation Testing

Запуск инструмента (`go-mutesting`, `pitest` для Java), который
автоматически "ломает" код (меняет `==` на `!=`, `>` на `>=`, удаляет
строки) и проверяет, ловят ли тесты. Невыжитая мутация = тест-дыра.

Применять прицельно для критичных модулей (allocator, validation,
authz), а не на весь codebase — медленно.

### 4.11 Fuzz Testing

`go test -fuzz` для функций, которые принимают user-input. Цель —
найти панику или некорректное поведение на неожиданном входе. Особенно
полезно для парсеров (page_token, filter string, JSONB unmarshal).

### 4.12 Contract Testing

Между двумя сервисами:

- **consumer-driven**: consumer описывает свои ожидания → provider
  верифицирует. Pact, gRPC-contract-tools.
- **provider-driven**: provider определяет proto-контракт, consumer'ы
  компилируются с тем же proto. У нас именно так — `kacho-proto` как
  single source.

Provider-driven contract test = "наш сервер удовлетворяет всем
вызовам, которые consumer теоретически может сделать", обычно через
shared proto + reflection-based tests.

### 4.13 Approval Testing

Похоже на snapshot, но интерактивнее: тест печатает результат, человек
"одобряет" (commit'ит) ровно один раз. Затем каждый прогон сравнивает с
одобренным. Хорошо для error-message текстов (канонические строки Kachō,
зафиксированные в `docs/architecture/`).

### 4.14 Differential / Comparison Testing

Прогоняем тот же сценарий против двух реализаций одного контракта и
сравниваем результаты. Применимо, когда есть независимый oracle
(референс-реализация алгоритма или предыдущая версия сервиса при
рефакторинге без изменения контракта): один и тот же набор входов
гоним против обеих, расхождение в response — сигнал регрессии.

---

## Часть V. Стратегии по типу кода

### 5.1 Pure functions

100% unit-тестами. Покрытие BVA + equivalence partitioning.
Никаких моков. Это самый дешёвый и устойчивый тест-класс.

### 5.2 I/O-heavy code

Тестируется на двух уровнях:
- логика обработки результата I/O — unit с stub'ом порта,
- сам I/O — integration-тест.

Не пытайся mock'ать pgxpool — тестируй repo через testcontainers.

### 5.3 Concurrent code

Особый класс. Требует:

- **race detector** (`go test -race`) обязательно на CI.
- **stress-тесты** (запуск N горутин одновременно, ловля race condition).
- **invariant assertions**: после параллельного прогона проверяем
  что итоговое состояние корректно (например count, sum, отсутствие
  duplicates).
- **deterministic concurrency**: где возможно — channel-based
  synchronization вместо sleeps.

Real-world пример: allocator должен выдавать **уникальные** IP при
параллельных вызовах. Test: 100 goroutine'ов вызывают
`AllocateExternalIP` для разных Address, в конце проверяем что все
IP уникальны.

### 5.4 Stateful services

Use-case с внутренним состоянием (счётчик, кэш, registry):

- Тестируй переходы: start state → action → end state.
- Покрывай **invalid transitions** (запрещённые переходы).
- Восстановление состояния после перезапуска (если есть).

### 5.5 Database queries

Только integration. Проверять:

- happy-path SELECT/INSERT/UPDATE/DELETE,
- constraint violation мапятся в правильный sentinel,
- transaction rollback при ошибке,
- index usage (через `EXPLAIN`, если performance-критично),
- generated columns / triggers.

Не тестировать через моки — моки не воспроизводят семантику SQL.

### 5.6 gRPC handlers

Двух уровней:
- **unit handler-тест** с fake service: проверяем маппинг
  request/response, error code translation, AuthZ-check.
- **gRPC contract integration**: реальный handler + реальный service +
  реальный repo (testcontainers) → один e2e через bufconn без сети.

### 5.7 Long-running operations (LRO)

Сложный класс из-за asynchronous-worker'ов:

| Что тестировать | Как |
|---|---|
| Sync-фаза создаёт Operation | Unit на service.Create |
| Worker возвращает success | Test через ops.Repo, ждём done=true |
| Worker возвращает failure | Симулируем ошибку в порту, ждём error в Operation |
| Worker не падает с panic | Recover, проверяем Operation в state failed |
| Worker не teчёт ресурсами на timeout | Cancel context, проверяем cleanup |
| Graceful shutdown drain'ит worker'ов | Integration: запустить worker, послать SIGTERM, ждать exit |

Анти-паттерн — `time.Sleep(100ms)` для ожидания worker'а. Использовать
`assert.Eventually` или сигнал-канал.

### 5.8 Async event streams (Watch)

Тестируется на трёх уровнях:

1. **Outbox emit** — repo integration: insert ресурса в TX, проверяем
   что событие появилось в outbox.
2. **Watch handler** — integration: подписаться, послать pg_notify,
   проверить что событие пришло в stream.
3. **End-to-end** — e2e: создать ресурс через публичный API, подписка
   через Watch должна увидеть событие.

### 5.9 Background workers (cron-like)

Время — это вход. Тестируй с инжектированным "часом":

- Передавай `time.Now`-функцию как dependency.
- В тестах подменяй на freezer (`mockClock.Now()`).
- Никогда не используй настоящий `time.Now()` внутри worker'а
  без возможности подмены.

---

## Часть VI. Что тестировать обязательно

### 6.1 Happy path

Один-два теста на основной сценарий каждой функции. Обязательно, но
не самый дорогой класс — этот код легче ломается, проще диагностируется.

### 6.2 Error path

| Категория | Пример |
|---|---|
| Validation reject | пустое required-поле, regex mismatch, host-bits в CIDR |
| Not found | ресурс не существует, parent отсутствует |
| Permission denied | cross-project access |
| Conflict | duplicate name, CIDR overlap, immutable field |
| Resource exhausted | full IP pool, rate limit, watch slot semaphore |
| Internal | DB unavailable, peer service down |

Каждая категория — отдельный тест.

### 6.3 Boundary conditions

См. BVA в §4.5. Часто **больше** тестов, чем на happy path.

### 6.4 Concurrency invariants

| Инвариант | Проверка |
|---|---|
| Idempotency | Повтор того же запроса → same result |
| Race-freedom | N параллельных вызовов → консистентное итоговое state |
| At-least-once delivery | Событие может прийти ≥ 1 раз, обработка идемпотентна |
| Atomicity | Multi-step операция либо вся, либо никак |
| Linearizability (если требуется) | Внешний наблюдатель видит операции в total order |

### 6.5 Performance regression budgets

Не путать с perf-тестами на скорость. Здесь — **бюджет**: "List 1000
ресурсов не должен делать > N SQL-запросов" (защита от N+1).

Реализация: бенчмарк или счётчик запросов в integration-тесте.

### 6.6 Замороженный контракт ошибок

Канонический контракт ошибок Kachō (тексты, коды, форматы — зафиксированы
в `docs/architecture/` и `api-conventions.md`) трактуется как замороженный:
меняется только осознанно через тикет. Тесты на:

- точный текст ошибки (byte-level),
- gRPC-код,
- proto-поля (наличие, тип, sample value),
- regex для format'ов,
- timestamp precision.

Это approval/snapshot testing class.

---

## Часть VII. Структура тестов

### 7.1 Naming

```
Test<Subject>_<Action>_<ExpectedOutcome>[_<UnderCondition>]
```

Примеры:
- `TestNetwork_Create_ReturnsAlreadyExists_OnDuplicateName`
- `TestSubnet_Relocate_RejectsInUse_WhenSubnetHasAddresses`
- `TestAllocator_AllocateExternalIP_Idempotent`
- `TestWatch_Stream_RejectsExcessClients_WithResourceExhausted`

Прочитав имя — понятно, что и зачем тестируется. Никаких `TestFoo1`,
`TestFoo2`.

### 7.2 Один assertion per concept

Не "одна `assert.Equal` на тест", а "одна **концепция** на тест".
Если проверка состоит из 3 assertion'ов, проверяющих один инвариант
("Operation success" = done=true + error=nil + response!=nil), они
все в одном тесте.

Если в тесте проверяется два независимых концепта — два теста.

### 7.3 Table-driven tests

Для функций с большим количеством входов — единый прогон с таблицей
кейсов. Преимущества: каждый кейс получает имя через `t.Run`,
параллельный прогон через `t.Parallel`, новый кейс — одна строка,
не копи-паста.

Антипаттерн: 50 однотипных кейсов размазаны по 50 отдельным функциям.

### 7.4 Subtests

Иерархия через `t.Run`. Имя кейса — короткое и описательное.
Параллельный прогон subtests включён по умолчанию для unit'а,
выключен для integration с shared state.

### 7.5 Fixtures vs factories

**Fixture** — статический файл (JSON, SQL dump), читаемый перед
тестом. Хорош для byte-exact замороженных контрактов и approval-тестов.

**Factory** — функция, генерирующая объект с параметрами. Хороша
для unit-тестов use-case.

Правило: **factory предпочитается fixture'у**, потому что factory
делает входы explicit в тесте (`builder.WithCIDR("10.0.0.0/24")` —
видно прямо в тесте, что CIDR этот). Fixture скрывает в файле.

### 7.6 Test data hygiene

- Никакого shared mutable state между тестами.
- Cleanup в `t.Cleanup(...)` или `defer`.
- Уникальные ID per-test через `runId/uuid` (избегаем коллизий
  parallel runs).
- Никаких production-data ID в коде тестов (security + воспроизводимость).

---

## Часть VIII. Анти-паттерны (что не делать)

### 8.1 Sleep как синхронизация

`time.Sleep(100ms)` — самый дешёвый способ сделать flaky тест.
Замена: `assert.Eventually(fn, timeout, interval)` или channel-signal
("worker готов"). Никаких "обычно 100мс достаточно".

### 8.2 Brittle mocks

Mock с `EXPECT().Foo(123).Times(1)`: меняешь реализацию (вызывает
Foo дважды или с 124) — тест падает, даже если контракт сохранён.

Замена: fake, проверяющая итоговое состояние, а не порядок вызовов.

### 8.3 Test interdependence

Тест B полагается на побочный эффект теста A (например, на запись в
shared DB). Запуск B в одиночку или в другом порядке — fail.

Замена: каждый тест готовит своё состояние с нуля.

### 8.4 Hidden state

Тест использует глобальные переменные (singleton'ы, init()-side-effects).
Параллельный прогон → race.

Замена: dependency injection. Глобалов не существует.

### 8.5 Slow unit tests

Unit-тест выполняется > 100ms. Корни:

- I/O просочился в use-case (репозиторий не за портом),
- sleep'ы,
- большие fixture-файлы парсятся каждый прогон,
- testcontainers поднимается из unit-теста.

Чини источник, не время.

### 8.6 Magic values

Тест содержит `"foobarbaz123"` без объяснения. Что это, почему именно
это? Замена: именованные константы (`const validCIDR = "10.0.0.0/24"`)
или explicit builder (`.WithValidCIDR()`).

### 8.7 Conditional logic in tests

`if condition { assert A } else { assert B }` — это два теста, перепиши
table-driven.

### 8.8 Testing the framework

Тест проверяет, что pgx правильно парсит CSV, что grpc возвращает
NOT_FOUND, что JSON-encoder выдаёт `{}` для пустой map. Это уже
протестировано авторами библиотеки. Тестируй **свой** код.

### 8.9 100% coverage worship

Тест-фабрика, которая прогоняет геттеры/сеттеры ради покрытия. Цифра
растёт, ценность нулевая. Заодно блокирует рефакторинг — каждое
переименование ломает 30 "тестов".

Coverage — симптом, не цель.

### 8.10 Тест внутри prod-кода

`if testing.Testing() { ... }` ветки — запах. Тест и prod-логика не
должны переплетаться. Если для тестируемости нужно ввести seam, оформи
его как dependency injection (передача `Clock` параметром), не как
runtime-флаг.

### 8.11 Mocks к собственной БД

`mockPgxPool.EXPECT().Query(...)` — это тестирование не своего кода,
а представления о том, что pgx делает. Любая разница между мокой и
реальностью даст ложно-зелёные тесты.

Замена: testcontainers с реальной Postgres.

### 8.12 Игнорирование негативного пути

"happy path работает, и хватит". В проде падают именно edge-cases.
Negative-тестов должно быть **больше**, чем positive.

### 8.13 Тесты-документация без assertion

Тест печатает что-то в лог, но не проверяет. Прогон зелёный, регрессии
не ловятся. Если тест нужен для иллюстрации использования —
переоформи в example (`go test ./... -run Example`).

---

## Часть IX. CI/CD-стратегия

### 9.1 Уровни прогона

| Триггер | Что запускается | Бюджет времени |
|---|---|---|
| Pre-commit hook (локально) | gofmt + go vet + unit с -short | < 30s |
| PR commit | Полный unit + integration | < 10min |
| PR merge / nightly | + e2e (Newman против dev-стенда) | < 30min |
| Release | + perf benchmarks + mutation testing | < 2h |

### 9.2 Selective test running

При изменении только в `internal/service/subnet.go` — гоняем не
все 5000 тестов, а только те, что покрывают этот файл (через
build-tag selection или dependency analysis).

Это полезная оптимизация только при больших test suite (10k+ тестов).
До этого — параллелизм важнее.

### 9.3 Parallel execution

- Unit-тесты: `t.Parallel()` по умолчанию.
- Integration: параллельны, если каждый создаёт свою БД (или
  schema).
- E2E: параллельны через изолированные project'ы.

### 9.4 Flake tracking

Каждое падение помечается тегом `flaky`. Если тест пометился >2 раз
за неделю — переводится в quarantine (skip с FIXME), сразу
issue с приоритетом.

Никаких автоматических retry в CI без quarantine — это легализует
бажные тесты.

### 9.5 Fast feedback loop

CI должен дать обратную связь в течение 10 минут. Если медленнее —
разработчик переключается на другую задачу, контекст теряется,
исправления удлиняются.

---

## Часть X. Применение к стеку Kachō

### 10.1 Go + pgx

| Слой | Тип теста | Инструменты |
|---|---|---|
| `domain/` | Unit | `testing`, table-driven |
| `service/` | Unit с моками | Ручные fake-репо в `mock_test.go` |
| `repo/` | Integration | `testcontainers-go` + Postgres 16 |
| `handler/` | Unit с fake service | `bufconn` для gRPC |
| `clients/` | Contract | Fake gRPC server в memory |

### 10.2 gRPC

- Reflection включён → grpcurl smoke-тесты.
- proto-stubs автогенерируются → contract на уровне компиляции.
- Server-stream (Watch) тестируется через bufconn + cancel ctx.

### 10.3 Postman / Newman

Quota-aware 3-suite split (RO / LIGHT / SEQ) — описан в
`kacho-vpc/CLAUDE.md §14.3` и `kacho-vpc/tests/newman/README.md`. Ключевое:

- Каждая suite-collection начинается с `00-preflight` и
  заканчивается `99-teardown`.
- Кейсы работают только в `{{_suiteProjectId}}`, не создают свои
  account/project.
- Одна и та же коллекция гоняется через `--env`-окружения (локальный
  стенд, dev, staging) — единый источник кейсов для всех сред.

### 10.4 Конкретные инварианты для Kachō

| Сервис | Обязательный инвариант | Уровень теста |
|---|---|---|
| Все | Garbage id → NOT_FOUND, не INVALID_ARGUMENT | unit handler |
| Все | Канонические тексты ошибок Kachō | snapshot/approval |
| VPC | CIDR overlap → FAILED_PRECONDITION (EXCLUDE) | repo integration |
| VPC | AllocateExternalIP idempotent | service unit + integration |
| VPC | AllocateExternalIP race-free (N concurrent) | integration |
| VPC | Outbox emit транзакционен | repo integration |
| VPC | Watch resume с cursor | handler integration |
| VPC | Graceful shutdown drain'ит LRO | integration (с SIGTERM) |
| VPC | ZoneRegistry-based zone validation (existence) | service unit с mock |
| RM | Project.Exists возвращает корректный ответ для caller | service unit + handler integration |
| RM | Cloud/Project/Org операции атомарны | repo integration |
| Все | List(project_id="") → INVALID_ARGUMENT | service unit |
| Все | Cross-tenant denied | handler unit с TenantCtx |

### 10.5 Чек-лист для нового RPC

При добавлении нового RPC проверь, что для него есть тесты на:

- [ ] Happy path (sync + async для LRO).
- [ ] Required-fields rejected с InvalidArgument.
- [ ] Format-validation (regex, length, CIDR host-bits).
- [ ] Cross-project permission denied (если project-scoped).
- [ ] NotFound для несуществующего parent (project, network).
- [ ] AlreadyExists для дубля (если есть UNIQUE constraint).
- [ ] FailedPrecondition для conflicting state.
- [ ] Pagination boundary (page_size=0, 1, max, max+1, invalid token).
- [ ] UpdateMask: unknown field, immutable field, empty mask.
- [ ] Garbage id → NotFound, не InvalidArgument.
- [ ] Operation: done=true + правильный response type для Create/Update,
      `google.protobuf.Empty` для Delete.
- [ ] Канонический текст ошибки Kachō byte-level (если контракт заморожен).
- [ ] Outbox-emit транзакционен с основной операцией.
- [ ] AuthMode production-mode: anonymous → PermissionDenied.

### 10.6 Чек-лист для нового сервиса

При создании нового полноценного сервиса Kachō:

- [ ] `make test-short` зелёный (unit).
- [ ] `make test` зелёный (unit + integration).
- [ ] Code coverage по критическим путям > 80%.
- [ ] Race detector прогоняется в CI.
- [ ] Newman или эквивалент e2e-coverage.
- [ ] Smoke-test против локального стенда через api-gateway.
- [ ] Graceful shutdown integration-test.
- [ ] Migration up/down/redo проверены integration-тестом.

---

## Приложение A. Сравнительная таблица техник

| Техника | Когда применять | Стоимость | Хрупкость |
|---|---|---|---|
| Table-driven | Множество кейсов одной функции | Низкая | Низкая |
| Property-based | Функции с математическим инвариантом | Средняя | Низкая |
| BVA | Функции с numeric/length boundary | Низкая | Низкая |
| Equivalence partitioning | Дискретная классификация входа | Низкая | Низкая |
| Combinatorial / pairwise | Функции с N параметрами | Средняя | Низкая |
| Golden master / snapshot | Сложный generated output | Низкая | Средняя |
| Approval | Замороженный контракт (канонические тексты Kachō) | Низкая | Низкая |
| Mutation | Критичный модуль, валидация качества тестов | Высокая | — |
| Fuzz | Парсеры user-input | Средняя | Низкая |
| Differential | Независимый oracle / референс-реализация | Средняя | Средняя |
| Contract (consumer-driven) | Кросс-сервисный контракт | Высокая | Низкая |
| Stress / race | Concurrent code | Средняя | Средняя |

---

## Приложение B. Глоссарий

| Термин | Значение |
|---|---|
| Test Double | Общее имя для Dummy/Stub/Fake/Spy/Mock |
| Fake | Упрощённая, но рабочая реализация (in-memory DB) |
| Stub | Возвращает захардкоженный ответ |
| Mock | Stub + проверка факта/порядка вызовов |
| Spy | Stub + запись параметров вызовов |
| SUT | System Under Test — то, что тестируется |
| Fixture | Подготовленное состояние перед тестом |
| Factory | Функция-конструктор тестовых объектов |
| AAA | Arrange-Act-Assert структура теста |
| BVA | Boundary Value Analysis — тесты на границах |
| Flaky | Тест с недетерминированным результатом |
| Quarantine | Изоляция flaky-теста до root-cause fix |
| Golden master | Эталонный snapshot для approval-сравнения |
| Mutation testing | Автоматическая модификация кода для оценки тестов |
| Property-based | Тесты на инвариант поверх случайной генерации входа |
| Differential testing | Сравнение результатов двух систем на одинаковом входе |
| Contract testing | Проверка соответствия между consumer и provider |
| Race detector | Инструмент обнаружения data race (Go `-race`) |
| Test pyramid | Распределение количества тестов по уровням |
| Bufconn | gRPC connection в памяти без TCP |
| Testcontainers | Библиотека для запуска Docker-зависимостей в тестах |

---

## Приложение C. Ссылки

| Документ | Контекст |
|---|---|
| `kacho-workspace/CLAUDE.md` | Архитектурные правила полирепо |
| `kacho-vpc/CLAUDE.md §14` | Уровни тестирования в VPC |
| `kacho-vpc/docs/ARCHITECTURE.md §XII` | Тестирование VPC в общей картине |
| `kacho-vpc/tests/newman/README.md` | Newman quota-aware pipeline |
| `kacho-vpc/tests/newman/docs/TAXONOMY.md` | Class taxonomy (CRUD/BVA/VAL/NEG) |
| Standard book | "xUnit Test Patterns" (Gerard Meszaros) — справочник по test doubles и анти-паттернам |
| Standard book | "Growing Object-Oriented Software, Guided by Tests" (Freeman & Pryce) — fake vs mock, state-based vs interaction |
| Standard book | "Working Effectively with Legacy Code" (Michael Feathers) — seams, testability |
