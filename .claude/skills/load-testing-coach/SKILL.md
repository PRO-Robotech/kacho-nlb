---
name: load-testing-coach
description: Use when designing or extending performance/load/stress/soak/spike/breakpoint tests using k6 or equivalent tooling. Owns benchmarking methodology, SLO/SLA definition, capacity planning, bottleneck identification (CPU/memory/network/DB-pool/connection-limit), result analysis (p50/p95/p99/error rate/RPS curve), reproducibility, comparison between runs. Separates load testing from functional tests (newman) and from production observability. Defers product-functional tests to testing-product-coach and code-level benchmarking to testing-code-coach.
---

# Skill: load-testing-coach

## 1. Когда меня вызывают

- Нужен нагрузочный тест нового RPC или сервиса
- Capacity planning: сколько RPS/TPS держит pod конкретного размера
- Поиск breakpoint: при какой нагрузке система начинает деградировать
- Соотношение ресурсов (CPU/memory) и пропускной способности
- Soak (24+ часов под нагрузкой): memory leaks, fd leaks, GC pressure
- Spike (резкий всплеск): elasticity, auto-recovery
- Stress (за пределами normal): graceful degradation
- Перформанс-регрессия в CI после фичи
- Анализ p99/p95 latency, error rate, queue depth
- Сравнение версий (canary vs baseline) под одинаковой нагрузкой

## 2. Когда меня НЕ вызывать

- Функциональные тесты — это `testing-product-coach`
- Unit-level benchmarks — это `testing-code-coach`
- Профилирование (pprof) — это developer task
- Production observability (Grafana dashboards непрерывно) — это SRE

## 3. Что я отдаю на выходе

- k6 / artillery script с описанием сценария
- SLO target (p99 < 500ms, error rate < 1%, RPS sustained ≥ N)
- Test plan: VU ramp-up profile, duration, stop conditions
- Анализ runs: график latency vs RPS, точка breakpoint
- Cause analysis при degradation (CPU, memory, db connection pool)
- Performance regression report между runs

## 4. Что я НЕ делаю

- Не фикшу баги, найденные в нагрузке — feature owner
- Не настраиваю production capacity — capacity planning отдельная задача
- Не пишу синтетические e2e в проде — это production-monitoring

---

# Эталонные практики нагрузочного тестирования

## Часть I. Зачем

| Цель | Параметр |
|---|---|
| Capacity planning | Сколько RPS держит N-pod кластер до p99 = 500ms |
| Breakpoint identification | При какой нагрузке начинается деградация |
| Bottleneck location | CPU / memory / DB / network — где предел |
| Regression detection | Не уронила ли последняя фича пропускную способность |
| Soak validation | Нет ли memory leaks за 24h |
| Elasticity verification | Восстанавливается ли система после spike |

## Часть II. Тип нагрузочного теста по цели

| Тип | Цель | Профиль | Длительность |
|---|---|---|---|
| **Smoke** | Проверка работоспособности под минимальной нагрузкой | 1-2 VU | 1-2 min |
| **Load** | Проверка SLO под целевой нагрузкой | Constant N VU | 10-60 min |
| **Stress** | Поведение за пределами normal | Ramp до 2-3× ожидаемого | 30-60 min |
| **Spike** | Реакция на резкий всплеск | Step с 0 до 5× в секунду | 5-15 min |
| **Soak** | Стабильность под длительной нагрузкой | Constant N VU | 24+ часов |
| **Breakpoint** | Точка отказа системы | Linear ramp до crash | до crash |
| **Volume** | Поведение при больших данных | Pre-populate DB | varies |

## Часть III. SLO и метрики

### 3.1 Обязательный набор метрик

| Метрика | Что показывает | Threshold пример |
|---|---|---|
| `http_req_duration` p50/p95/p99 | Latency distribution | p99 < 500ms |
| `http_req_failed` rate | Доля HTTP 4xx/5xx | < 1% |
| `iterations` / `iterations_per_sec` | Throughput (RPS) | ≥ 100 RPS sustained |
| `vus` / `vus_max` | Concurrency | до plan max |
| `http_reqs` | Total requests | inform |
| `data_received` / `data_sent` | Network throughput | bandwidth check |
| Custom: `error_count` | Ошибки в логике (не HTTP) | = 0 |

### 3.2 Что должно быть в любом SLO

- **Functional**: % успешных операций.
- **Latency**: p99 ≤ X ms.
- **Throughput**: ≥ Y RPS sustained.
- **Stability**: 0 unhandled errors в логах.
- **Resource**: pod не должен превышать N% CPU / memory.

## Часть IV. Профили нагрузки в k6 stages

| Профиль | Stages |
|---|---|
| Constant load | `{duration: '10m', target: 100}` |
| Ramp-up | `{duration: '2m', target: 50}, {duration: '5m', target: 100}, {duration: '2m', target: 0}` |
| Spike | `{duration: '1m', target: 10}, {duration: '30s', target: 200}, {duration: '5m', target: 200}, {duration: '30s', target: 10}, {duration: '1m', target: 0}` |
| Soak | `{duration: '24h', target: 50}` |
| Breakpoint | `{duration: '1m', target: 10}, ... ramp until thresholds violated` |

## Часть V. Сценарии

### 5.1 Изоляция сценариев

Один k6 run = один scenario. Не миксуйте Create + List + Delete в одном
сценарии — латентность одного типа усреднит латентность другого.

| Сценарий | Что генерирует |
|---|---|
| `read_only` | 100% Get/List |
| `write_heavy` | 80% Create + 20% Get |
| `mixed` | 60% Read + 30% Create + 10% Delete (real-world) |
| `lifecycle` | Create→Get→Update→Delete (один полный цикл per iteration) |

### 5.2 Изоляция данных

Каждая VU должна работать в своём namespace (folder), чтобы избежать
contention на UNIQUE constraint. `runId` + `vu_id` в name.

## Часть VI. Анализ результатов

### 6.1 Точка breakpoint

Признаки достижения breakpoint:
- p99 растёт нелинейно
- error rate > N% (1-5% в зависимости от tolerance)
- p99 latency растёт быстрее RPS
- очередь подтверждений начинает расти

### 6.2 Сравнение версий

| Метрика baseline | Метрика candidate | Вердикт |
|---|---|---|
| p99=200ms, error 0.1%, 200 RPS | p99=210ms, error 0.1%, 200 RPS | OK (jitter в пределах ±10%) |
| p99=200ms, error 0.1%, 200 RPS | p99=350ms, error 0.5%, 180 RPS | REGRESSION (latency ↑75%, throughput ↓10%) |
| p99=200ms, error 0.1%, 200 RPS | p99=180ms, error 0.05%, 240 RPS | IMPROVEMENT |

Регрессия = метрика хуже >15% (calibrate).

### 6.3 Root cause при degradation

| Симптом | Cause hypothesis | Verify |
|---|---|---|
| p99 ↑, p50 = норма | Long tail — GC pause / DB lock | Trace, pprof, slow query log |
| Error rate ↑ при RPS ↑ | Resource exhaustion (connection pool, file descriptors) | pod metrics, db pool stats |
| Throughput plateau | Single bottleneck | bottleneck analysis (CPU/IO/db) |
| Memory растёт линейно | Leak | heap profile сравнение |

## Часть VII. Reproducibility

### 7.1 Что необходимо для repeatability

- Идентичная конфигурация cluster / pods / DB
- Идентичные данные (seed)
- Идентичный network path (LAN vs cloud)
- Контроль external load (no other tests running)
- Фиксированный seed для k6 random
- Запись raw output → version control

### 7.2 Naming runs

`<service>-<scenario>-<version>-<timestamp>.json`
Например: `kacho-vpc-write-heavy-v1.0.3-2026-05-15T10-00.json`.

### 7.3 What to commit

- k6 script (`.js` файлы)
- run command (Makefile target)
- baseline result (JSON)
- analysis report (MD)
- НЕ коммитить: full raw output (gigabytes)

## Часть VIII. CI integration

### 8.1 Когда запускать в CI

| Frequency | Тип |
|---|---|
| PR commit | Smoke (1-2 min, sanity) |
| Nightly | Load (10 min, SLO check) |
| Weekly | Stress + Spike |
| Pre-release | Soak (24h) + Breakpoint |
| Monthly | Volume + DR drill |

### 8.2 Gates

Используйте k6 thresholds для CI fail/pass:

```
thresholds: {
  http_req_duration: ['p(99) < 500'],
  http_req_failed: ['rate < 0.01'],
  iterations_per_sec: ['avg > 100'],
}
```

При нарушении threshold k6 exit non-zero → CI fail.

## Часть IX. Анти-паттерны

- **Mixing scenarios** в одном run → нечитаемые usredneние metric
- **Один user** в Soak (нет parallelism, тест не воспроизводит реальную нагрузку)
- **Без warm-up** (JIT/cache empty, первые 30 сек артефакт)
- **Без cooldown** между ramp-up и hold (метрики смешиваются)
- **No fixed RPS** только VUs → throughput не предсказуем
- **Тестирование от localhost через LAN** (network latency != production)
- **Сравнение нагрузки на разных средах** (k8s dev vs prod)
- **Не сравнили с baseline** → не понятно "много" или "мало"

## Часть X. Tooling

| Инструмент | Use case |
|---|---|
| **k6** | Mainstream load test (JS scenarios, prometheus output) |
| **artillery** | Lighter setup, YAML config |
| **wrk** / **wrk2** | Простые HTTP benchmark |
| **fortio** | gRPC + HTTP, fortio-server pluggable |
| **JMeter** | GUI / classic enterprise |
| **vegeta** | CLI + Go library |
| **ghz** | gRPC specific |

Для Kachō: k6 (HTTP через api-gateway) + ghz (gRPC прямо в сервисы).
