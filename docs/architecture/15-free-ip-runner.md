# kacho-nlb — 15-free-ip-runner (durable-handle + reconciler застрявших листенеров)

## Задача

VIP-аллокация листенера — внешний side-effect в `kacho-vpc` (единственный
dual-write edge create/delete). Если процесс падает (или peer-vpc недоступен)
между аллокацией VIP и финализацией строки листенера, VIP «утекает»: остаётся
аллоцированным в vpc, а в `kacho_nlb` нет согласованного состояния, чтобы его
вернуть.

Два сценария утечки:

- **create-path** — VIP аллоцирован, но финальный commit строки не прошёл →
  раньше строки листенера в БД вообще не было → VIP осиротел, и его нечем
  реконсилировать (нет address_id);
- **delete-path** — release VIP при `Delete` упал (vpc недоступен) → листенер
  остался в `status='DELETING'`, VIP всё ещё аллоцирован.

Решение из двух частей: **durable-handle create-сага** (делает create-сироту
реконсилируемой) + **`free_ip_runner`** (фоновый reconciler, освобождает VIP по
address_id и финализирует/удаляет handle).

## Durable-handle create-сага (3 TX)

`Listener.Create` worker (`internal/apps/kacho/api/listener/create.go`) разбит
так, чтобы `address_id` был durable **до** финализации:

1. **TX-1** — `INSERT listeners (status='CREATING', allocated_address='';
   address_id известен для BYO, пуст для auto)`. Строка — durable handle с
   детерминированным owner `nlb_listener:<id>`.
2. **acquireVIP** — внешний side-effect: BYO `Get` + `SetReference`; auto
   `AllocateExternalIP`/`AllocateInternalIP`. В ответе известен `address_id`.
3. **TX-2 (отдельный немедленный commit)** — `UPDATE … SET address_id,
   allocated_address` (ещё `status='CREATING'`). Персистит реальный `address_id`
   сразу после alloc-ответа: `free_ip_runner` ключует release именно адресом.
4. **TX-3 (writer-TX)** — `UPDATE … SET status='ACTIVE'` + outbox `CREATED` + LB
   `UPDATED` + fga-register-intent, атомарно одним commit'ом.

`CREATING` — **транзиентный** статус (handle in-flight саги), терминал успеха —
`ACTIVE`. Откат до финализации (graceful) — `compensateCreate`: освобождает VIP
(если аллоцирован) + удаляет handle. Если процесс умирает раньше compensation —
осиротевший `CREATING`-handle с известным `address_id` добивает `free_ip_runner`.

## free_ip_runner (reconciler)

`internal/apps/kacho/jobs/free_ip_runner.go` — фоновый worker под errgroup-
супервизором (как `target_drain_runner`), зарегистрирован в
`cmd/kacho-loadbalancer/main.go`. Каждый тик:

1. Claim одной застрявшей строки: `status IN ('DELETING','CREATING') AND
   updated_at < now() - age_threshold` (partial index `listeners_reconcile_idx`),
   `ORDER BY updated_at ASC LIMIT 1 FOR UPDATE SKIP LOCKED`.
2. Release VIP **по `address_id` строки** (детерминированно, не листингом):
   `vip_origin='auto' → FreeIP(address_id)`, `'byo' → ClearReference(address_id)`
   (чужой статический Address tenant'а уцелел). Idempotent — клиент трактует
   `NotFound` как успех.
3. `DELETE` handle. Для `DELETING` дополнительно эмитит то, что сделал бы успешный
   `Delete` (outbox `DELETED` + LB `UPDATED` + fga-unregister) — всё в той же TX,
   что и DELETE. Для `CREATING`-сироты **ничего не эмитим**: она никогда не
   достигла `ACTIVE` и не анонсировалась (`CREATED`/fga-register не отправлялись).

**Age-порог** исключает гонку с нормальным in-flight create/delete: свежая строка
(моложе порога) не трогается, пока легитимный worker дорабатывает. Default —
interval `30s`, age-threshold `5m` (`jobs.free-ip.*`).

**Multi-replica-safety** — `FOR UPDATE SKIP LOCKED`: release+DELETE по строке
выполняет ровно одна реплика, вторая пропускает залоченную строку. Release
вызывается внутри залоченной TX (held across network call) — приемлемо: сироты
редки, лок per-row, остальные реплики работают другие строки (SKIP LOCKED).

**Failure isolation** — транзиентная ошибка (vpc Unavailable / SQL) логируется,
TX откатывается, строка остаётся в прежнем статусе и переедет на следующем тике
(идемпотентно). Только `ctx.Done()` завершает `Run`.

Reconciler требует vpc internal-address client (release): без него нельзя
безопасно удалять handle (иначе утечка VIP) → runner не стартует.

## Остаточный known-gap (auto-only)

Узкое окно «alloc-ответ получен, но TX-2 (persist `address_id`) ещё не
закоммичен». Краш **строго** в этом окне → в строке `address_id=''`:

- reconcile by-address невозможен текущим vpc-клиентом (free-by-owner RPC в vpc
  нет — намеренно не вводим, лишний cross-repo);
- `free_ip_runner` удаляет такой `CREATING`-handle (освобождать нечем), фиксируя
  WARN-лог. Это очищает port-слот; остаточный осиротевший Address в vpc — принятый
  auto-only edge.

Окно сведено к **одному локальному commit'у** сразу после alloc. **BYO свободен**
от gap: `address_id` известен до INSERT (TX-1), поэтому BYO-сирота всегда
реконсилируема (`ClearReference` по адресу). Расширение vpc (free-by-owner) —
отдельный cross-repo, вне этой работы.
