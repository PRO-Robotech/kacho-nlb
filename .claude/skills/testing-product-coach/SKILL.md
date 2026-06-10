---
name: testing-product-coach
description: Use when designing or extending product-level tests against the deployed Kachō stack — Newman/Postman regression suites, conformance vs the Kachō spec/acceptance docs, exploratory sessions, performance/load/soak/chaos. Treats the service as a black box reachable only through public gRPC/REST/UI. Owns formal test design techniques (ECP, BVA, decision tables, state transition, pairwise, use-case, error guessing, property-based, risk-based). Knows case taxonomy (CRUD-/BVA-/VAL-/NEG-/IDM-/CONC-/CONF-). Defers white-box / unit work to testing-code-coach.
---

# Skill: testing-product-coach

## 1. Когда меня вызывают

- QA расширяет Newman regression suite после новой фичи / RPC.
- Дизайн test cases по acceptance-документу (Given-When-Then → case-id list).
- Выбор формальной техники test design (ECP, BVA, pairwise, state transition).
- Risk assessment новой фичи: какие зоны критичны, где сосредоточить кейсы.
- Подготовка exploratory session: charter, timebox, scope.
- Метрики покрытия для продукта — requirement / scenario / API surface / risk-area.
- Дизайн rate-limit-aware suite против развёрнутого стенда Kachō.
- Регистрация осознанного by-design отклонения в `docs/architecture/`.

## 2. Когда меня НЕ вызывать

- Unit / integration / contract tests кода — это `testing-code-coach`.
- Спорная семантика канонических текстов контракта Kachō — `vpc-conventions-auditor`.
- CIDR / EXCLUDE constraint специфика — `vpc-cidr-specialist`.
- Acceptance-spec — `acceptance-author`.
- Конкретная конверсия finding → newman case — `qa-test-engineer` (он реально пишет .json + curl-probe).

## 3. Что я отдаю на выходе

- План кейсов для новой фичи: список case-id с классом, входом, ожидаемым результатом.
- Risk-based prioritization: high/medium/low с обоснованием.
- Чек-лист "что недотестировано в этой фиче" (по 14-пунктному template).
- Charter exploratory-сессии.
- Решение по conformance: фича соответствует канон-контракту Kachō (fix bug) или это осознанный by-design выбор (registered в `docs/architecture/`).
- Метрика покрытия конкретной фичи (requirement / API surface coverage).

## 4. Что я НЕ делаю

- Не пишу `.postman_collection.json` сам — это `qa-test-engineer`.
- Не правлю продуктовый код — баги в код возвращаю в `rpc-implementer` / `go-style-reviewer`.
- Не выдаю unit-test stubs — это `testing-code-coach`.

## 5. Обязательно: каждый уникальный кейс → в CASES-INDEX.md

При добавлении нового case-паттерна **обязательно** регенерирую/обновляю
`tests/newman/docs/CASES-INDEX.md` (или альтернативный путь в проекте). Этот
файл — единый каталог уникальных проверок:

- Group by RPC method (`Create`, `Get`, `List`, `Update`, `Delete`, `Move`,
  CIDR ops, etc.).
- Каждый row — `*-METHOD-CLASS-DETAIL` pattern + classes + priority +
  список ресурсов где применён + 1-строчное описание.
- Дубликаты helper-кейсов (применённых к разным ресурсам) считаются как
  **один паттерн** с n-instances. Это критично — без агрегации индекс
  раздувается до 500+ строк и теряет ценность.

Генерация автоматическая (script проходит по `collections/*.json` и
извлекает `case.name + case.description`). Регенерация после каждого
batch расширения case-set'а. CASES-INDEX.md — артефакт, не source-truth;
source — модули `cases/*.py`.

Когда я выдаю план новых кейсов — финальным пунктом всегда «обновить
CASES-INDEX.md».

## 6. Принципиальный сдвиг рамки

Я смотрю на сервис **только через публичный интерфейс**. Код — вспомогательный
артефакт для понимания скоупа и углов атаки на негативные кейсы, не предмет
проверки. Замена pgx на sqlx, замена worker-pool на task-queue, переезд на
другую БД — должны быть **невидимы** для моих кейсов. Если видны — это
white-box masquerade.

| Аспект | testing-code-coach | testing-product-coach (это я) |
|---|---|---|
| Объект | Функция, класс, модуль | Сервис в целом |
| Доступ | Полный к internals | Только публичный API |
| Источник истины | Код + acceptance | Acceptance + канон-контракт Kachō |
| Coverage | Code coverage (осторожно) | Requirement / scenario / API surface |
| Инструменты | go test, mocks, testcontainers | Newman, k6, chaos-mesh |

## 7. Входные сигналы для review Newman-патча

| Сигнал в патче | Реакция |
|---|---|
| Кейс ожидает конкретный SQLSTATE | White-box masquerade — переформулировать в терминах gRPC-кода |
| Кейс читает stdout сервиса | Не наблюдаемо снаружи — переоформить через метрики/логи endpoint |
| Только happy path для новой фичи | Дополнить ECP-классами + BVA + 3-5 negative |
| Нет 00-preflight / 99-teardown | Отвергнуть — нарушение контракта изоляции |
| `runId` не уникализирует имена | Конфликты с параллельными прогонами — fix |
| Жёсткие задержки `setTimeout` | Replace на poll-Operation до done=true |
| Кейс завязан на специфику local-стенда | Переоформить так, чтобы проходил на любом развёрнутом Kachō-стенде |
| Тест ожидает имя SQL-constraint | Заменить на наблюдаемое gRPC-сообщение |
| Snapshot обновляется автоматом | Manual diff-review обязателен |

## 8. Ссылки в knowledge base ниже

Полное knowledge-body — ниже, с раздела «Тестирование продукта как
чёрного ящика». Структура: 12 частей + 4 приложения. Использую при ответе
на вопросы о конкретных техниках, метриках и применении к Kachō.

---

# Тестирование продукта как чёрного ящика

Документ описывает, как тестировать Kachō с точки зрения продукта, а не
кода. Объект тестирования — сервис в целом, видимый через публичный
интерфейс. Внутренности (язык, ORM, схема БД, конкретные функции) —
вспомогательная информация для понимания скоупа и углов атаки, не
предмет проверки.

Этот документ дополняет `TESTING.md` (там — практики тестирования
**кода** разработчиком). Здесь — практики тестирования **продукта**
тестировщиком или QA-инженером.

---

## Часть I. Что значит "продукт как чёрный ящик"

### 1.1 Граница тестирования

Граница — публичный контракт сервиса. Для Kachō это:

| Канал | Что наблюдаемо |
|---|---|
| gRPC `:9090` (внешний) | Все RPC из публичных proto |
| REST через api-gateway (`/vpc/v1/...`, `/resource-manager/v1/...`) | grpc-gateway маршруты с JSON |
| Internal gRPC `:9091` | Admin RPC (для admin-UI / curl) |
| Логи / метрики / трейсинг | Observability surface |
| Поведение во времени (latency, throughput, soak) | Non-functional |

Всё, что внутри сервиса — деталь реализации. Замена pgx на sqlx, замена
worker-pool'а на task-queue, переезд на другую DB — должны быть
**невидимы** для тестов продукта. Если видны — тест белого ящика
маскируется под чёрный.

### 1.2 Где код всё-таки полезен

| Зачем смотреть в код | Что искать |
|---|---|
| Оценка скоупа фичи | Какие RPC задеваются, какие side-effects |
| Понимание архитектурных свойств | Есть ли LRO, есть ли retries, какой timeout |
| Углы атаки для негативных тестов | Какие SQL-constraint'ы, какие парсеры |
| Reproducibility бага | Конкретное состояние, в которое надо привести систему |
| Понимание термина из спеки | Расшифровка непонятного поля |

Не зачем:

| Чего избегать | Почему |
|---|---|
| Покрывать internal-функцию через публичный API | Это white-box, выдающий себя за black-box |
| Подгонять тест под текущую реализацию | Рефакторинг — естественное событие, тест должен пережить |
| Использовать тестовые fixtures из код-репо как обязательное знание | QA не должен читать `mock_test.go`, чтобы написать тест |

### 1.3 Источники истины

| Источник | Приоритет | Применение |
|---|---|---|
| Acceptance-документы (`docs/specs/`) | Высший | Given-When-Then формулировки кейсов |
| Proto-определения (`kacho-proto/proto/`) | Высокий | Точная структура запросов/ответов |
| Канон-контракт Kachō (`docs/architecture/`, конвенции API + фиксированные тексты ошибок) | Высокий | Conformance-кейсы на стабильность контракта |
| `CLAUDE.md` сервиса | Средний | Контекст для понимания, не источник кейсов |
| Код | Низкий | Только для понимания скоупа |
| Существующие тесты | Низкий | Не повторять, а дополнять |

Если acceptance-документ и код расходятся — это либо баг в коде, либо
устаревшая спека. Решение — у владельца, не у тестировщика.

---

## Часть II. Что тестируется в продукте

### 2.1 Functional properties

| Свойство | Пример проверки |
|---|---|
| Соответствие контракту | POST /networks → 200 + Operation с правильными полями |
| Правильная семантика | UpdateMask пустая → full PATCH с silent-ignore immutable |
| Бизнес-инварианты | Subnet с Address нельзя relocate |
| Идемпотентность | Allocate того же address → same IP, already_allocated=true |
| Транзакционная атомарность | Если worker упал — ни ресурса, ни события в outbox |

### 2.2 Non-functional properties

| Класс | Что измеряется | Бюджет (пример) |
|---|---|---|
| Latency | p50/p95/p99 на типичном запросе | p99 < 500ms для GET |
| Throughput | RPS, который держится без degradation | ≥ 100 RPS на pod |
| Resource | CPU, memory, connection pool | < 1 vCPU / 512Mi / 20 conn в idle |
| Stability | Soak — нет деградации за N часов | 24h без memory leak |
| Concurrency | Параллельные клиенты | 100 concurrent без race |

### 2.3 Quality attributes

| Атрибут | Тип теста |
|---|---|
| Compatibility | REST vs gRPC возвращают одинаковое содержимое |
| Backward-compatibility | Старый клиент работает с новой версией |
| Observability | Логи содержат ожидаемые поля, метрики прирастают |
| Security (AuthZ) | Cross-tenant запрос → PermissionDenied |
| Security (info-leak) | Error-text не содержит DSN/hostname |
| Conformance | Соответствие канон-контракту Kachō (фиксированные тексты ошибок, status-mapping) |
| Resilience | Сервис восстанавливается после рестарта peer'а |

### 2.4 Cross-cutting

| Тип | Проверка |
|---|---|
| Migration | После `migrate up` все RPC работают, после `migrate down` — старая схема |
| Upgrade | Канарейка с новой версией не ломает существующие данные |
| Disaster recovery | После kill -9 → данные не потеряны, незавершённые LRO либо завершены, либо помечены failed |

---

## Часть III. Test design techniques для чёрного ящика

Techniques применяются **поверх спецификации**, не поверх кода. Каждая
строит набор кейсов из требований.

### 3.1 Equivalence Class Partitioning (ECP)

Разбиение домена входа на классы, в которых поведение одинаково:

| Параметр | Классы |
|---|---|
| `project_id` | { valid existing, valid non-existing, garbage format, empty, null } |
| `name` | { empty, valid short, valid max length, too long, invalid chars } |
| `cidr` | { valid v4 with host-bits=0, valid v4 with host-bits, valid v6, garbage, empty } |
| `page_size` | { 0, 1..50, 51..1000, >1000, negative } |

Один представитель каждого класса даёт основу. Это не сокращение тестов
к минимуму, а **покрытие классов** — пропуск класса = непротестированная
ветка.

### 3.2 Boundary Value Analysis (BVA)

К каждому классу — тесты на границах:

| Граница | Примеры значений |
|---|---|
| Числовая (page_size 0..1000) | 0, 1, 50, 51, 999, 1000, 1001, -1, MAX_INT |
| Длина строки (name ≤63) | 0, 1, 62, 63, 64, 1000 |
| CIDR префикс (/0../32) | /0, /1, /31, /32 для v4; /0, /1, /127, /128 для v6 |
| Time (created_at) | epoch=0, далёкое прошлое, далёкое будущее, NaN |

BVA + ECP вместе — фундамент функционального покрытия.

### 3.3 Decision Table Testing

Для функций с несколькими параметрами, влияющими на результат:

| Условие 1: project existence | Условие 2: name unique | Условие 3: CIDR valid | Ожидаемый результат |
|---|---|---|---|
| Yes | Yes | Yes | 200 + Operation success |
| No | — | — | NotFound "Project not found" |
| Yes | No | — | AlreadyExists |
| Yes | Yes | No (host-bits) | InvalidArgument |
| Yes | Yes | No (overlap) | FailedPrecondition |

Каждая строка — отдельный кейс. Decision tables предотвращают пропуск
комбинаций, которые при ad-hoc подходе легко забыть.

### 3.4 State Transition Testing

Для ресурсов с явным state machine:

```
       Subnet status (если бы был):
           CREATING ──► ACTIVE ──► UPDATING ──► ACTIVE
                          │                       │
                          └─► DELETING ──► (gone) ┘
```

Кейсы — все валидные переходы (positive) + все запрещённые попытки
(negative: Delete во время Updating, Update во время Deleting).

Для LRO: `done=false → done=true (success)`, `done=false → done=true
(error)`. Кейсы на промежуточные опросы (`Operation.Get` пока done=false).

### 3.5 Pairwise / N-wise Combinatorial

Для функций с большим числом параметров — pairwise покрывает все пары
значений с минимумом тестов.

Пример: `CreateSubnet` принимает 8 параметров с 3-4 значениями каждый.
Полная комбинаторика = 4^8 = 65536 кейсов. Pairwise = ~30. Покрытие
обнаруживает 90% дефектов взаимодействия пар.

Инструменты: `Microsoft PICT`, `pairwiser`, `allpairs.exe`.

### 3.6 Cause-Effect Graphing

Формализация: причины (входы) → эффекты (выходы) с логическими связями.
Помогает увидеть пропущенные комбинации до того, как они станут
багами в проде.

Для `UpdateMask`:

```
   has_unknown_field ──┐
                       ├─► InvalidArgument "unknown field"
   has_immutable_field ┤
                       └─► InvalidArgument "is immutable"

   mask_empty AND immutable_in_body  ──► silent ignore
   mask_empty AND mutable_in_body    ──► applied
```

### 3.7 Use-Case / Scenario-Based Testing

Кейсы построенные как пользовательские сценарии end-to-end:

| Сценарий | Шаги |
|---|---|
| Onboarding | Org → Cloud → Project → Network → Subnet → Address |
| Move resource | Создать в project A → Move в project B → Get показывает project B |
| Disaster | Создать → удалить parent → проверить cascade |
| Multi-tenant | User A создаёт → User B пытается прочитать → denied |

Сценарии находят **интеграционные** баги, которые ECP/BVA отдельных
RPC не ловят.

### 3.8 Error Guessing / Negative Testing

Опытный тестировщик угадывает места, где система может сломаться.
Источники интуиции:

- Парсеры (page_token, filter string).
- Race conditions (создать-удалить-создать).
- Resource limits (full pool, full quota).
- Edge cases типов (Unicode в name, null vs empty).
- Сетевые сбои (peer service down).

Это эвристика, не системная техника, но даёт высокий ROI на критичных
модулях.

### 3.9 Exploratory Testing

Незаписанные предварительно сценарии: тестировщик задаёт вопросы
"а что если?", "а как покажет себя при ...?", шлёт нетривиальные
запросы, читает ответы, формирует новые гипотезы.

Цель — найти то, что не покрыто формальными техниками. Хорошо
применяется на новой фиче перед закреплением в regression-suite.

Результат exploratory-сессии — список найденных issue + кандидаты
в постоянную suite.

### 3.10 Property-Based Testing (внешний интерфейс)

Свойство, которое должно выполняться для любого валидного входа.
Генератор шлёт случайные входы, проверяет инвариант.

| Свойство | Пример |
|---|---|
| Roundtrip | Create + Get → ресурс совпадает с request (mutable поля) |
| Idempotency | Allocate × N → same result |
| Commutativity | Создание A потом B → то же что B потом A (для независимых) |
| Monotonicity | Pagination — все элементы видны при page-iteration |

Особенно полезно для **проверки стабильности контракта** — генератор
шлёт случайные валидные запросы и проверяет, что ответы держат
канон-инварианты Kachō (формат ошибок, status-mapping, форма ресурса),
независимо от частных значений входа.

### 3.11 Risk-Based Prioritization

Каждый класс кейсов взвешивается по:

- **Вероятность бага** (изменено ли в этом релизе, сложная ли логика).
- **Impact бага** (data loss > security > correctness > UX).
- **Стоимость теста** (auto > manual, smoke > soak).

Распределение усилий: 70% на high-risk, 25% на medium, 5% на low.
В CI prioritization работает как **порядок прогона**: high-risk
первым, чтобы получить fast feedback.

---

## Часть IV. Типы тестов продукта

### 4.1 По цели

| Тип | Цель | Frequency |
|---|---|---|
| Smoke | "Не сломано совсем" | Каждый деплой |
| Functional regression | Поведение не изменилось | Каждый PR |
| Conformance | Соответствие канон-контракту Kachō (фикс. тексты, status-mapping, форма) | Каждый PR + nightly |
| Performance | Latency/throughput в бюджете | Nightly |
| Load | Поведение под целевой нагрузкой | Релиз |
| Stress | Поведение за пределами нагрузки | Релиз |
| Soak | Стабильность под длительной нагрузкой | Релиз |
| Spike | Реакция на резкий всплеск | Релиз |
| Resilience / Chaos | Восстановление после сбоев зависимостей | Nightly |
| Security | AuthZ, info-leak, injection | Каждый PR + специализированный аудит |
| Compatibility | REST↔gRPC, версии клиентов | Релиз |
| Migration / Upgrade | Накат миграций, обновление версий | Каждый деплой |
| Disaster recovery | Восстановление из бэкапа | Квартально |
| Exploratory | Поиск незакрытого | Per-feature |

### 4.2 По стилю запуска

| Стиль | Когда |
|---|---|
| Автоматический regression | Постоянно в CI |
| Ad-hoc Newman прогон | После feature work / debugging |
| Manual exploratory | Перед релизом |
| Continuous monitoring | В проде (synthetic transactions) |

### 4.3 Specifically для Kachō

| Suite | Объём | Назначение |
|---|---|---|
| Newman RO | ~30 запросов | Read-only smoke (Get/List), 50ms delay |
| Newman LIGHT | ~70 запросов | Light mutations (Create+Delete), 250ms |
| Newman SEQ | ~10 запросов | Heavy/sequential (Move, multi-resource), 1500ms |
| Newman conformance | Те же 110 | Проверка фикс. текстов / status-mapping против канон-контракта Kachō |
| Internal admin | Отдельный набор | Internal* RPC (Region/Zone/AddressPool) |

---

## Часть V. Карта продуктового покрытия

### 5.1 Что покрывать в каждом сервисе Kachō

Для каждого ресурса (Network, Subnet, Address, ...):

| Класс кейсов | Кол-во кейсов (порядок) |
|---|---|
| Happy CRUD (Create, Get, List, Update, Delete) | 5 |
| Mutations с LRO (poll до done) | 5 |
| ECP по каждому полю (name, description, labels, ...) | 5-10 × поле |
| BVA по числовым/строковым параметрам | 3-5 × параметр |
| Validation rejection (regex, length, host-bits) | 10-20 |
| Cross-project denial | 3-5 |
| Idempotency (повтор Create с identifying key) | 1-2 |
| Conflict (duplicate name, EXCLUDE overlap) | 3-5 |
| Cascade dependencies (Network с детьми → Delete fails) | 3-5 |
| State transition / immutable fields | 5-10 |
| Pagination boundary | 5 |
| Filter syntax | 3-5 |
| Conformance — фикс. тексты ошибок / status-mapping контракта Kachō | 10-30 |
| Concurrency invariant (parallel Create same name) | 1-3 |

Итого ~100-200 кейсов на ресурс. Для VPC (7 ресурсов) — ~700-1400
кейсов, что соответствует реальному размеру Newman-suite.

### 5.2 Карта рисков (для prioritization)

| Зона | Уровень риска | Класс тестов |
|---|---|---|
| IP allocation (race, exhaustion) | Critical | Concurrency invariant + stress |
| AuthZ (cross-tenant) | Critical | Negative по матрице caller × resource |
| Data integrity (outbox, FK) | High | Atomicity tests, kill-9 recovery |
| Стабильность контракта (фикс. тексты ошибок) | High | Conformance |
| LRO completion | High | State transition, kill-9 mid-flight |
| Pagination correctness | Medium | Boundary + roundtrip property |
| Filter syntax | Medium | ECP + negative |
| Labels / description (cosmetic) | Low | Happy path only |

---

## Часть VI. Test data design

### 6.1 Иерархия тестовых фикстур

Kachō требует иерархии Org → Cloud → Project перед любой VPC-операцией.
Подходы:

| Уровень | Подход | Применение |
|---|---|---|
| Per-run | Создать всю иерархию в `00-preflight`, удалить в `99-teardown` | Изолированный прогон, dev-стенд |
| Per-suite | Использовать pre-allocated `existingProjectId`, не создавать | Shared-стенд с лимитами, общий long-lived стенд |
| Per-case | Каждый кейс создаёт свой project | Полная изоляция, но дорого по лимитам |

Текущий Kachō Newman — гибрид: shared `_suiteProjectId` (per-suite) +
runId для уникализации имён ресурсов внутри (per-case).

### 6.2 Уникализация имён

Каждый прогон должен работать в чистом namespace. Способы:

| Способ | Pros | Cons |
|---|---|---|
| `runId = Date.now()+rand` в имени | Просто, гарантирует уникальность | Засоряет project, нужен teardown |
| `runId + clean-on-start` | Чистый старт | Не safe для параллельных прогонов |
| Per-case project | Полная изоляция | Quota expensive |

### 6.3 Rate-limit / quota-awareness

Если развёрнутый Kachō-стенд имеет лимиты (rate-limit на запись на project,
N ресурсов на project), suite должна учитывать:

- **delay-request** между запросами (50-1500ms).
- **chunking** (RO до LIGHT до SEQ).
- **cleanup** перед прогоном (если project уже занят).
- **graceful degradation** на 429 / `ResourceExhausted` — wait + retry,
  не fail.

### 6.4 Data isolation

Параллельные прогоны не должны видеть друг друга:

- Разные project.
- Разные runId.
- Разные prefixes имён (по worker-id).

Иначе тесты будут drown в `AlreadyExists`-конфликтах.

---

## Часть VII. Метрики покрытия для чёрного ящика

Code coverage здесь не применим — мы не видим кода. Альтернативы:

### 7.1 Requirement coverage

Доля требований из spec, по которым есть хотя бы один тест.
Считается через mapping `requirement_id → test_case_id`. Targets:
high-risk требования — 100%, medium — 90%, low — 70%.

### 7.2 API surface coverage

Доля API-методов с тестами. Формула: `(методы с тестами) / (всего
методов в proto)`.

Расширения:
- per-method: doли вариантов вызова (happy + error классы).
- per-resource: какие из CRUD-операций покрыты.

### 7.3 Scenario coverage

Доля известных бизнес-сценариев с тестами. Source: use-case диаграммы,
acceptance Given-When-Then.

### 7.4 Risk-area coverage

Доля выявленных high-risk зон с тестами. Менее формальный, более
жёсткий — высокий риск без теста = release blocker.

### 7.5 Mutation coverage (опционально, expensive)

Запуск мутационного тестера, который меняет ответы сервера (например
случайно меняет gRPC код), проверяет, ловят ли тесты. Дорого, применяется
прицельно к critical paths.

### 7.6 Contract-stability coverage (для фикс. текстов / status-mapping)

Доля методов, чьи error-тексты и status-mapping зафиксированы как
канон-контракт Kachō (в `docs/architecture/` + конвенциях API) и
заасертены в Newman byte-level. Тексты ошибок — часть контракта;
меняются только осознанно (через тикет), поэтому conformance-кейс
ловит непреднамеренный drift в формулировках.

---

## Часть VIII. Структура test execution

### 8.1 Pipeline

```
   PR commit
       │
       ▼
   ┌─────────────────────────────────────────────────┐
   │ Stage 1: Smoke (RO, ~30 кейсов, ~2min)          │
   └────────────────┬────────────────────────────────┘
                    │ pass
                    ▼
   ┌─────────────────────────────────────────────────┐
   │ Stage 2: Functional (LIGHT, ~70 кейсов, ~10min) │
   └────────────────┬────────────────────────────────┘
                    │ pass
                    ▼
   ┌─────────────────────────────────────────────────┐
   │ Stage 3: Heavy (SEQ + negative, ~30min)         │
   └────────────────┬────────────────────────────────┘
                    │ pass
                    ▼
                  Merge
                    │
                    ▼
   ┌─────────────────────────────────────────────────┐
   │ Nightly: Performance + Conformance (канон-контракт)│
   └─────────────────────────────────────────────────┘
```

Каждый стейдж — gate: фейл блокирует следующий. Fast feedback внизу,
дорогие — наверху.

### 8.2 Isolation

| Уровень | Изоляция |
|---|---|
| Тест-кейс | Per-case runId в именах, не делит mutable state с соседями |
| Прогон | Per-run project (или dedicated `existingProjectId`) |
| Окружение | Local kind / staging / prod-canary — отдельные стенды |

### 8.3 Parallelism

- В пределах RO suite — параллельные кейсы (read-only безопасны).
- В пределах LIGHT — последовательные внутри case (Create→Poll→Delete),
  параллельные между cases.
- SEQ — строго последовательно (взаимозависимости).

### 8.4 Repeatability

Любой прогон reproducible:
- Seed для random — фиксированный per-run.
- Время мокируется, если влияет.
- Окружение описано в `local.postman_environment.json` / `staging.postman_environment.json` (per-стенд Kachō).

### 8.5 Flake handling

| Признак | Действие |
|---|---|
| Один fail за неделю в зелёном suite | Игнорировать, мониторить |
| 2+ fail за неделю | Quarantine + issue |
| Систематический fail | Root cause: либо тест неверный, либо продукт нестабильный |

Никогда не делать auto-retry для прохождения CI — это легализация бага.

---

## Часть IX. Анти-паттерны при чёрном ящике

### 9.1 Подсматривание реализации в тест

Тест строится "знаем, что внутри psql, поэтому POST с CIDR overlap
вернёт `23P01`". Это белый ящик. Правильно: тест ожидает поведение по
спеке (`FailedPrecondition "CIDRs can not overlap"`), независимо от
конкретного кода БД.

### 9.2 Тест к интерфейсу, который не публичный

Если интерфейс internal-only — он не входит в продуктовый контракт
для внешнего клиента. Тестировать его как продукт неправильно. Это
admin / kacho-only поверхность, должна иметь свой набор кейсов.

### 9.3 Тест работает только в локальном окружении

Использует `localhost:5432`, читает файлы из `/tmp`, опирается на
конкретное состояние БД, которое не воспроизводится. Это не продуктовый
тест, это white-box на запах.

### 9.4 Тестирование только happy path

В проде падают edge-cases. Negative + boundary должно быть **больше**,
чем positive (в 2-3 раза по объёму кейсов).

### 9.5 Snapshot-обновление без diff-review

Snapshot обновляется автоматически на каждый прогон → тест становится
no-op. Snapshot всегда обновляется вручную с осознанным diff.

### 9.6 Игнорирование observability в тестах

Сервис работает корректно, но не пишет логи / не эмитит метрики /
не открывает trace span. Без тестов на observability эти регрессии
не ловятся — а в инциденте они стоят больше функциональных.

### 9.7 Ad-hoc Postman-collection без структуры

Папка из 200 запросов без taxonomy, без preflight/teardown, без environment.
Не воспроизводимо, не поддерживаемо, не масштабируется.

Структура — `00-preflight` / `99-teardown` / class taxonomy
(CRUD-/BVA-/VAL-/NEG-) — обязательна с первого запроса.

### 9.8 Слепое доверие к UI

UI работает → "продукт работает". На самом деле UI может маскировать
backend-баги (например, тихо игнорировать поле). Тестировать продукт
надо через API, UI — отдельный класс (frontend testing).

### 9.9 Тестирование внутреннего peer-protocol через "клиентский" тест

Если Kachō-VPC ходит в Kachō-RM по gRPC и эта связь "ломается" —
это не product-bug сам по себе для клиента, это infrastructure-bug.
Должны быть отдельные contract-тесты между сервисами, а не подмена их
e2e через клиента.

### 9.10 Прогон тестов в read-only стенде, но с write-кейсами

Кейсы делают POST, стенд не разрешает — все падают. Это конфигурационная
ошибка suite, а не баг продукта.

---

## Часть X. Когда заглядывать в код (sparingly)

### 10.1 Допустимые случаи

| Кейс | Что искать |
|---|---|
| Понять, что значит спорный поле в proto | Доменная структура + комментарии |
| Понять, есть ли LRO у RPC | Возвращаемый тип в .proto |
| Найти angle для негативного теста | Constraint в migration / validation в service |
| Воспроизвести bug, репортированный в проде | Конкретный код-pathway |
| Оценить полноту своего покрытия | Сравнить ECP-классы с реальными ветками в коде |

### 10.2 Недопустимые случаи

| Кейс | Почему плохо |
|---|---|
| Покрыть приватную функцию через публичный API | Тест становится хрупким к рефакторингу |
| Дублировать в продуктовых тестах то, что покрыто unit'ом | Удвоение стоимости, нет дополнительной ценности |
| Завязаться на конкретное SQL-сообщение в error-mapper | При смене БД тесты ломаются — это white-box |

### 10.3 Граница

Тест может **знать**, что внутри есть LRO. Но не должен:
- знать имя worker-горутины,
- ожидать конкретный delay между sync-фазой и async,
- завязываться на implementation detail работ воркера.

Тест должен:
- ожидать `Operation` сразу,
- поллить `Operation.Get` до `done=true`,
- проверять конечный response согласно proto-контракту.

---

## Часть XI. Применение к Kachō

### 11.1 Newman 3-suite пайплайн

| Suite | Назначение | Когда |
|---|---|---|
| RO | API smoke по чтениям | Каждый push (fast feedback) |
| LIGHT | Functional regression на мутациях | Каждый PR |
| SEQ | Heavy scenarios | Перед merge |
| Conformance | Фикс. тексты ошибок / status-mapping канон-контракта Kachō | Nightly |
| Internal admin | Internal* RPC | По требованию |

### 11.2 Taxonomy class (Newman docs/TESTCASES.md)

| Префикс | Класс |
|---|---|
| `CRUD-` | Happy CRUD |
| `BVA-` | Boundary value |
| `VAL-` | Field validation rejection |
| `NEG-` | Negative path (existence, permission, conflict) |
| `IDM-` | Idempotency |
| `CONC-` | Concurrency invariant |
| `CONF-` | Conformance / стабильность канон-контракта |

Каждый case-id: `<DOMAIN>-<ACTION>-<DETAIL>`, например `NET-CR-OK`,
`SUB-DEL-WITH-ADDR`, `ADR-CR-EXT-DDOS-ADV`. Это сразу выдаёт класс и
объект.

### 11.3 Registry осознанных by-design решений (`docs/architecture/`)

Conformance проверяется против канон-контракта Kachō (acceptance + фикс.
тексты ошибок + конвенции API), не против чужого API. Осознанные дизайн-
решения, отклоняющиеся от «наивного ожидания», фиксируются как by-design:
- **kacho-only** — фичи/RPC сверх базового CRUD (default-SG-auto, IPAM admin
  через Internal*).
- **осознанное отклонение** — выбор семантики, задокументированный в
  `docs/architecture/<svc>/` (а не «баг»).

Соответствующие admin/kacho-only кейсы:
- хранятся в отдельной internal-коллекции
  (`kacho-vpc-internal.postman_collection.json`),
- не прогоняются в unified public-suite (чтобы не зашумлять),
- задокументированы в `docs/architecture/<svc>/` с обоснованием.

### 11.4 Specific инварианты для Kachō

| Инвариант | Suite | Класс |
|---|---|---|
| Garbage id → NotFound (не InvalidArgument) | LIGHT | NEG |
| Канон-текст ошибки Kachō byte-level (стабильность контракта) | LIGHT | CONF |
| CIDR overlap → FailedPrecondition | LIGHT | NEG |
| Subnet с Address нельзя relocate | SEQ | NEG |
| Allocate IP — same response при повторе | LIGHT | IDM |
| Параллельный Create same name → один AlreadyExists | SEQ | CONC |
| Cross-project Get → PermissionDenied | LIGHT | NEG |
| Empty mask UPDATE → silent ignore immutable | LIGHT | VAL |
| Pagination через token восстанавливает позицию | LIGHT | BVA |
| Watch resume с from_sequence_no | (internal suite) | IDM |

### 11.5 Conformance против канон-контракта Kachō

Conformance-кейсы в `kacho-vpc.postman_collection.json` асертят, что ответ
сервиса держит зафиксированный контракт Kachō:
- фиксированный текст ошибки (byte-level, как описано в `docs/architecture/`
  и конвенциях API),
- корректный gRPC status-mapping (SQLSTATE → код),
- стабильную форму ресурса (поля / timestamps до секунд).

Расхождение с зафиксированным контрактом = либо bug в Kachō (фиксить),
либо осознанное изменение контракта (тогда сначала меняется
`docs/architecture/`/конвенции через тикет, потом кейс).

---

## Часть XII. Чек-листы

### 12.1 Чек-лист новой фичи (с точки зрения QA)

- [ ] Acceptance-spec прочитан, Given-When-Then разобраны.
- [ ] Mapping: каждое Given-When-Then → один или несколько case-id.
- [ ] Покрыты ECP-классы для всех новых полей.
- [ ] BVA на числовые / строковые / CIDR boundary.
- [ ] Negative path: required-reject, not-found, permission-denied,
      conflict.
- [ ] LRO жизненный цикл: sync → poll → done с правильным response.
- [ ] Idempotency для retry-safe операций.
- [ ] Concurrency invariant (если есть critical section).
- [ ] Conformance: фикс. текст ошибки заасертен против канон-контракта Kachō (`docs/architecture/`).
- [ ] Pagination + filter (если применимо).
- [ ] UpdateMask (если есть Update RPC).
- [ ] AuthZ cross-project (если project-scoped).
- [ ] Кейсы добавлены в master Newman-collection.
- [ ] Suite-сборка (RO / LIGHT / SEQ) пересобрана через `build-suite.py`.
- [ ] `docs/architecture/<svc>/` обновлён, если введено осознанное by-design отклонение.

### 12.2 Чек-лист релизного тестирования

- [ ] RO suite passes 100%.
- [ ] LIGHT suite passes (с известными exceptions из `docs/architecture/` — осознанные by-design отклонения).
- [ ] SEQ suite passes.
- [ ] Newman conformance: фикс. тексты ошибок / status-mapping держат канон-контракт Kachō (изменения — только осознанные, через тикет).
- [ ] Performance benchmark в бюджете (latency / RPS).
- [ ] Soak (24h) без memory leak / file descriptor leak.
- [ ] Migration up + down + up идемпотентен.
- [ ] Graceful shutdown drain'ит LRO < 30s.
- [ ] AuthMode=production принудительно отвергает anonymous.
- [ ] Логи структурированы (JSON), содержат traceId и
      ожидаемые поля.
- [ ] Metrics endpoint доступен (если есть).
- [ ] Helm chart lint passes.

### 12.3 Чек-лист exploratory сессии

- [ ] Цель сессии заявлена (новая фича / hotspot / customer-issue).
- [ ] Timebox установлен (обычно 60-120 минут).
- [ ] Charter (что я хочу выяснить) написан.
- [ ] Session log ведётся (запросы, ответы, гипотезы).
- [ ] Найденные issue → tickets с reproducer.
- [ ] Кандидаты в regression suite → добавлены.
- [ ] Заметки о потенциальных рисках → передан владельцу.

---

## Приложение A. Сравнение с TESTING.md

| Аспект | TESTING.md (код) | Этот документ (продукт) |
|---|---|---|
| Аудитория | Разработчик | Тестировщик / QA |
| Объект тестирования | Функция, класс, модуль | Сервис в целом |
| Источник истины | Код + acceptance | Acceptance + канон-контракт Kachō |
| Доступ к internals | Полный | Только через публичный API |
| Уровни | Unit / Integration / E2E | Smoke / Functional / Scenario / Conformance |
| Coverage metric | Code coverage (с осторожностью) | Requirement / scenario / API surface |
| Test design | По коду + спеке | По спеке, угадывание |
| Когда писать | Параллельно с кодом (TDD/после) | После acceptance approve, до feature freeze |
| Инструменты | `go test`, testcontainers, mocks | Newman/Postman, k6, chaos-mesh |
| Анти-паттерны | Mock-heavy, sleep-sync, brittle | White-box masquerade, happy-only, ad-hoc |

Документы дополняют, не заменяют друг друга. У разработчика — оба.
У тестировщика — этот.

---

## Приложение B. Карта техник по задачам

| Задача | Лучшая техника |
|---|---|
| Покрыть домен входа | ECP + BVA |
| Найти неучтённые комбинации | Decision tables + Pairwise |
| Покрыть жизненный цикл ресурса | State Transition |
| Покрыть пользовательский сценарий | Use-case based |
| Найти неизвестные проблемы | Exploratory + Error Guessing |
| Проверить инвариант | Property-Based |
| Зафиксировать сложный output | Snapshot / Approval |
| Поймать drift зафиксированного контракта | Conformance / Snapshot |
| Найти race conditions | Stress / Concurrency invariant |
| Проверить устойчивость | Chaos / Resilience |
| Проверить лимиты | Performance / Load / Stress |

---

## Приложение C. Глоссарий

| Термин | Значение |
|---|---|
| Black-box testing | Тестирование без доступа к коду, только через публичный интерфейс |
| White-box testing | Тестирование с полным доступом к коду |
| Grey-box testing | Тестирование с частичным знанием реализации (например, схемы БД) |
| ECP | Equivalence Class Partitioning — разбиение домена на эквивалентные классы |
| BVA | Boundary Value Analysis — тесты на границах |
| Conformance testing | Проверка соответствия зафиксированному канон-контракту Kachō (фикс. тексты ошибок, status-mapping, форма ресурса) |
| Differential testing | Сравнение результатов двух прогонов/версий на одинаковом входе (напр. до/после рефакторинга) |
| Smoke test | Минимальный набор для подтверждения "не сломано совсем" |
| Soak test | Длительный прогон под нагрузкой для выявления утечек |
| Spike test | Резкий всплеск нагрузки |
| Stress test | Нагрузка выше расчётной для определения breaking point |
| Chaos testing | Намеренное внесение сбоев для проверки resilience |
| Exploratory testing | Незаписанное предварительно исследование продукта |
| Risk-based testing | Приоритизация по вероятности и impact'у бага |
| Charter (exploratory) | Декларация цели сессии |
| Test oracle | Источник истины для ожидаемого результата |
| Rate-limit-aware suite | Suite, учитывающая rate-limit развёрнутого Kachō-стенда |
| Pairwise testing | Покрытие всех пар значений параметров (а не all-tuples) |
| State Transition | Тестирование переходов state machine |
| Snapshot testing | Сравнение output с эталоном, одобренным однажды |
| Property-based | Тесты на инвариант поверх случайной генерации |

---

## Приложение D. Литература

| Источник | Контекст |
|---|---|
| ISTQB Foundation Level Syllabus | Базовая taxonomy техник |
| "Lessons Learned in Software Testing" (Kaner/Bach/Pettichord) | Классика black-box testing |
| "Specification by Example" (Adzic) | Acceptance как источник тестов |
| "Explore It!" (Hendrickson) | Exploratory testing methodology |
| "How Google Tests Software" (Whittaker/Arbon/Carollo) | Test pyramid в большом масштабе |
| "The Art of Software Testing" (Myers) | ECP/BVA/Cause-Effect Graphing |
| `kacho-workspace/docs/TESTING.md` | Парный документ — тестирование кода |
| `kacho-vpc/tests/newman/docs/TAXONOMY.md` | Class taxonomy конкретно для Kachō |
| `kacho-vpc/docs/architecture/` | Registry осознанных by-design дизайн-решений Kachō |
| GitHub Issues (`PRO-Robotech/kacho-vpc`) | Найденные баги / tech-debt из тестов |
