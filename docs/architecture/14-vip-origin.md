# kacho-nlb — 14-vip-origin (release-дискриминатор VIP листенера)

## Задача

При `Listener.Delete` нужно выбрать корректную ветку освобождения VIP:

- **auto-alloc** — Address создан самим NLB под этот листенер; при удалении
  листенера Address освобождается целиком (`vpc.InternalAddressService` →
  `FreeIP`, фактически `AddressService.Delete`).
- **BYO** (bring-your-own) — Address заранее создан tenant'ом и передан в
  `ListenerService.Create(address_id=…)`; при удалении листенера снимается лишь
  ссылка (`ClearReference`), сам Address **не удаляется**.

Неверный выбор ветки для BYO (FreeIP вместо ClearReference) — **потеря данных**:
удаляется чужой статический адрес tenant'а.

## Решение: колонка `listeners.vip_origin`

Источник истины — DB-колонка `vip_origin TEXT NOT NULL DEFAULT 'auto'
CHECK (vip_origin IN ('byo','auto'))` (миграция `0005_listener_vip_origin.sql`):

- `Create`: auto-alloc → `vip_origin='auto'`; передан `address_id` → `'byo'`.
- `Delete`: ветка выбирается чтением `vip_origin` (`auto → FreeIP`,
  `byo → ClearReference`). Имя Address для решения **не используется**.

Колонка — внутренний дискриминатор реализации; в публичную proto-проекцию
листенера она не входит (это не tenant-facing поле).

### Прежняя реализация и почему она опасна

Раньше ветка выбиралась эвристикой `detectBYO` по **префиксу имени** Address
(`nlb-listener-`). Tenant, назвавший свой BYO-Address так же (например
`nlb-listener-edge`), при удалении листенера терял этот адрес — FreeIP удалял
его. Дискриминатор-колонка устраняет зависимость release-ветки от имени.

## Backfill существующих строк (boot-reconcile)

В таблице `listeners` нет origin-сигнала помимо `address_id`, поэтому чистая
SQL-миграция «по факту» невозможна. Миграция лишь добавляет колонку с
`DEFAULT 'auto'`; реальный backfill выполняет **idempotent Go-reconcile на boot**
(`internal/apps/kacho/jobs.VIPOriginReconciler`, запускается из
`cmd/kacho-loadbalancer`):

1. Листинг листенеров с непустым `address_id`.
2. Для каждого — `vpc.AddressService.Get(address_id)`.
3. Origin: `auto`, если имя Address равно детерминированному auto-alloc-имени
   **именно этого** листенера (`nlb-listener-<short-id>`,
   `domain.ListenerAutoAddressName`); иначе `byo`. Привязка к конкретному
   listener-id (а не к loose-префиксу) устойчива к BYO-адресу с похожим именем.
4. Idempotent per-row `UPDATE` (детерминированное значение → запуск на
   нескольких репликах безопасен).

### Риск переходного окна (явно зафиксирован)

Между применением SQL-миграции и завершением Go-reconcile уже существующие
BYO-листенеры временно несут `vip_origin='auto'` — их `Delete` в этом окне ушёл
бы по FreeIP-ветке (потеря адреса).

**Митигировано:**

- Reconcile запускается **до приёма трафика**; пока он не завершён успешно,
  сервис держит readiness **not-ready** (`/readyz` 503, readiness-checker
  `vip-origin-reconcile`) — fail-closed, ни один Delete не обслуживается.
- `vpc` недоступен на boot при наличии строк → reconcile возвращает ошибку,
  readiness остаётся not-ready, попытки повторяются (idempotent) до успеха.
- Свежий стенд (нет строк) → reconcile вырождается в **no-op**, readiness
  становится ready сразу.
- Address удалён/не найден (`NotFound`) → строка пропускается: release
  idempotent в любой ветке (отсутствующий адрес → `NotFound` трактуется как
  успех), потери нет.

### Остаточное ограничение

Backfill определяет origin по имени Address (единственный исторический сигнал в
vpc для pre-existing строк). Это строго безопаснее прежней prefix-эвристики
(exact-match, привязанный к listener-id) и применяется однократно; live-путь
(`Create` → колонка → `Delete`) имя не использует вовсе.
