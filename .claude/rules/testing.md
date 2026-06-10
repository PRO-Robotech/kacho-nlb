# Тестирование (строгий TDD)

## Test-first — обязательно (ban #12)

**Сначала тест, потом код.** Падающий тест (RED) пишется и **прогоняется ДО** кода
фикса/фичи — подтверждается, что падает по нужной причине (фича/фикс отсутствует, не
опечатка). Затем код → GREEN. Касается **всех** уровней — Go unit/integration И
newman-кейсов. Newman/integration-тест, написанный уже ПОСЛЕ кода, — нарушение (даже если зелёный).

Чанк из нескольких изменений: написать ВСЕ падающие тесты первыми → RED по всем → чинить
по одному в GREEN. В PR/отчёте показывать пару «RED → GREEN»; заявлять о готовности без неё нельзя.

Каждый PR с новым RPC / новым полем / новым oneof-case / новой публичной функцией обязан содержать **в том же PR**:
- **Integration-тест** (`internal/repo/*integration_test.go`) — testcontainers Postgres, SQL-сторона,
  включая concurrent-race-сценарии для CAS/UNIQUE/EXCLUDE.
- **Newman-кейс** (`tests/newman/cases/*.py` → `gen.py`) — black-box через api-gateway, ≥1 happy + ≥1 negative.

«out of scope / follow-up / TBD» как обоснование отсутствия тестов — запрещено. Единственное
исключение: PR ссылается на **уже открытый** KAC-тикет под эти тесты (`Tests-followup: KAC-N`),
заведённый и привязанный к эпику ДО merge. Reviewer/агент reject'ит PR без тестов.

## Test-only PR (ban #13)

Задача «дописать тесты под существующий функционал»:
- **Прод-код НЕ трогаем** — только `tests/`/`docs/`. Любой `internal/`/`cmd/`/`migrations/`-фикс → отдельный PR со своим KAC.
- **TODO/FIXME/`pm.test.skip`/закомментированный assert — запрещены** в тестах так же строго, как в проде.
- **TDD-red против реального бага прода** = finding, не tech-debt: (a) GitHub Issue (`bug` + `verified-by:test`);
  (b) в кейсе `# verifies <issue-url>` (без skip); (c) кейс остаётся красным до фикса прода —
  допустимое исключение из «100% pass» с декларацией в `RESULTS.md` «Known failing — product bugs» + KAC-trail.

## Пирамида и инфраструктура

- **unit** (`apps/kacho/api/<resource>/usecase_test.go`, `internal/handler/*_test.go`) — mock port-интерфейсов
  из `internal/repo/repomock`/`kachomock`; LRO дожидаются детерминированно (`AwaitOpDone`), не `time.Sleep`.
  Если service-тест требует Postgres → утечка adapter в use-case.
- **integration** (`internal/repo/*integration_test.go`) — testcontainers Postgres 16; CRUD, EXCLUDE/FK/UNIQUE,
  outbox-транзакционность, CAS/OCC/SKIP-LOCKED races. Под нагрузкой Docker может таймаутить — гонять `-p 1` при contention.
- **e2e/newman** (`tests/newman/`) — главная regression-инфра; декларативные `cases/*.py` → `gen.py` → Postman-коллекции;
  только HTTP через api-gateway. Workflow нового кейса: `validate-cases.py` (уникальность + CASES-INDEX) → `gen.py`.
- **fuzz** (`internal/fuzz/`) и **k6/ghz** (нагрузка) — где применимо.

Методология: skills `testing-code-coach` (unit/integration), `testing-product-coach` (black-box техники),
`load-testing-coach` / `<svc>-load-testing` (нагрузка). Финальная верификация перед merge:
`go test ./... -race` + `golangci-lint run` + `govulncheck` + newman зелёные.
