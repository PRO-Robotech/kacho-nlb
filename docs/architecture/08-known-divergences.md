# kacho-nlb — 08-known-divergences

Осознанные by-design отклонения kacho-nlb. Здесь фиксируются решения, которые
могут выглядеть как расхождение со «стандартом» (внешним облаком, прежней схемой,
интуицией аудитора), но приняты намеренно — чтобы будущий разработчик/аудитор не
заводил на них баг-тикеты повторно.

> YC-стилистика остаётся (именование, error-format, timestamp truncate,
> update_mask discipline), но **структура методов/ресурсов 1-в-1 не копируется** —
> см. workspace CLAUDE.md «Что это за проект». Структурное расхождение с внешним
> облаком само по себе — не баг.

## VIP-уникальность живёт на LoadBalancer, а не на Listener

**Что.** Партиальный `UNIQUE listeners_region_vip_uniq (region_id,
allocated_address, port, protocol) WHERE status<>'DELETING'`, существовавший в
baseline-миграции `0001`, **намеренно снят** в миграции `0009`
(`DROP INDEX IF EXISTS ... listeners_region_vip_uniq`). Комментарий 0009:
«VIP-уникальность переехала на LoadBalancer».

**Почему.** Модель VIP переехала на уровень LB: anycast-VIP региона уникален как
свойство самого LoadBalancer'а. Инвариант теперь энфорсится
`load_balancers_region_v4_uniq` / `load_balancers_region_v6_uniq` (партиальные
`UNIQUE (region_id, address_vN) WHERE address_vN <> ''`), а AttachVIP-CAS
полагается на их `23505`. Валидность этих индексов дополнительно само-лечится
миграцией `0012`. Race-тест инварианта — `load_balancer_vip_concurrent_integration_test.go`.

**Следствие для аудита.** На уровне Listener **нет** within-service инварианта
region-VIP-уникальности, и его отсутствие — не пробел в покрытии. Комментарий в
`listener_integration_test.go`, ранее ссылавшийся на несуществующий
`TestListener_RegionVipUnique_RaceTest`, исправлен (sec-hardening r3, finding #6).

## Move ↔ Attach: двусторонний DB-guard (не только CAS на move-стороне)

**Что.** `LoadBalancer.MoveProject` и `TargetGroup.MoveProject` несут атомарный
guard `UPDATE ... WHERE NOT EXISTS(attached_target_groups ...)`, а
`AttachedTargetGroups.Attach` — locking-read `... FOR NO KEY UPDATE OF lb, tg` в
своём `INSERT ... SELECT`-JOIN'е.

**Почему обе стороны.** Guard на move-стороне закрывает только порядок
«attach закоммичен раньше». Обратный порядок (move выполнил свой `UPDATE`, держит
row-lock, ещё не закоммичен; конкурентный Attach под READ COMMITTED plain-read'ом
видит *stale* до-move project и вставляет cross-project attach) закрывается только
locking-read'ом на стороне Attach: он блокируется на move'нутой row-е и после
commit'а Move через EvalPlanQual пере-оценивает project-JOIN на свежем project →
mismatch → 0 rows → `FailedPrecondition`. `FOR NO KEY UPDATE` (не `FOR KEY SHARE`
от FK) обязателен — только он конфликтует с `FOR NO KEY UPDATE`, который Move
берёт на свою row-у. Обоснование и race-тесты — `sec_hardening_r3_integration_test.go`
(sec-hardening r3, finding #1).

## lb_status_recompute — CAS-guard на финальной записи статуса

**Что.** Триггер `lb_status_recompute()` (пересчёт `INACTIVE↔ACTIVE` при
listener/attach INSERT/DELETE) пишет статус через
`UPDATE ... WHERE id=$1 AND status=cur_status` и эмитит outbox-событие только при
`ROW_COUNT>0` (миграция `0013`).

**Почему.** Прежний безусловный `UPDATE` мог затереть конкурентный explicit-переход
(`ACTIVE→STOPPING`/`→DELETING`) lost-update'ом: recompute читал `ACTIVE`, explicit
CAS коммитил `STOPPING`, а безусловная запись возвращала `ACTIVE`/`INACTIVE`.
CAS-guard + row-lock сериализуют пересчёт с `SetStatusCAS`; при проигранном CAS
статус не трогается и spurious-outbox не эмитится (sec-hardening r3, finding #2).
