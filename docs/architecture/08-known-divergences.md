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

**Тот же паттерн у `Listener.Insert`.** Листенер несёт денормализованное
зеркало `project_id`/`region_id` родительского LB. Раньше `Listener.Insert` был
plain-`INSERT` с software-captured (в sync-фазе Create) project_id — тот же
move-first TOCTOU: `LoadBalancer.MoveProject` каскадит
`UPDATE listeners SET project_id ... WHERE load_balancer_id=$lb` под
`FOR NO KEY UPDATE` на LB-row, а plain-INSERT берёт лишь FK `KEY SHARE` (не
конфликтует) → листенер персистится со stale project_id, а Move-каскад его не
видит. Фикс (sec-hardening r6, finding DATA #2): `Insert` теперь
`INSERT ... SELECT lb.project_id, lb.region_id ... FROM load_balancers lb
WHERE lb.id=$lb FOR NO KEY UPDATE OF lb` — сериализуется с MoveProject точно как
Attach. Race-тест — `TestListenerCreate_MoveFirst_ProjectConsistent`
(`sec_hardening_r6_integration_test.go`).

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

## Composition-root `runServe` — намеренно линейный, длинный wiring (не «толстая функция»)

**Что.** `cmd/kacho-loadbalancer/main.go:runServe` — единственный composition-root
(CLAUDE.md sanctions `cmd/main.go` как единственное место wiring'а). Функция длинная
(~300 строк, из них ~190 кода — остальное inline-документация каждого шага), но это
**не** нарушение layering'а и не «толстый сервис».

**Почему by-design (а не рефакторить в helper'ы дальше).** Тяжёлые шаги уже вынесены
в `wiring.go`/`observability.go`/`backstop.go`: `dialPeers`, `check.NewInterceptor`,
`assembleBackgroundWorkers`, `buildInterceptorChains`, `registerGRPCServices`,
`startDiagnosticListener`, `startLROWorker`, `buildReadinessCheckers`. Остаток
`runServe` — (1) плоская последовательность создания ресурсов с `defer`-cleanup'ами
(`cancel`/`pool.Close`/`repo.Close`/`closeAll`) наверху функции, где порядок виден
целиком, и (2) когезивная shutdown-оркестрация (`triggerShutdown` sync.Once + errgroup
`g.Go`-блоки). Дальнейшее дробление shutdown-блока в отдельный helper потребовало бы
протащить ~12 локалов (оба gRPC-сервера, `cancel`, `healthAgg`, `background`,
`diagTask`/`diagShutdown`, `cfg`, `logger`) через struct и **разнесло бы** порядок
`defer`/GracefulStop по двум функциям — ровно тот defer-ordering риск, который аудит и
называет опасностью. Держать этот порядок в одной линейной функции с явными
комментариями — сознательный trade-off читаемости-vs-безопасности в пользу второго
(sec-hardening r5b, LEAN-finding «runServe 308 lines»).

## Package-local `shared.*`-делегаторы — намеренная per-package конвенция (не дубль)

**Что.** Каждый use-case пакет (`loadbalancer`, `targetgroup`, `listener`, `announce`,
`operation`) держит тонкие package-local функции, форвардящие в единый `shared.*`:
`mapDomainErr`→`shared.MapDomainErr`, `errInvalidArg`→`shared.ErrInvalidArg`,
`operationToProto`→`shared.OperationToProto`, `stripSentinel`→`shared.StripSentinel`,
`peerErrToStatus`→`shared.PeerErrToStatus`. Тела 2-3 строки и byte-идентичны между
пакетами.

**Почему by-design (а не удалять wrapper'ы).** Логика **уже** консолидирована в
`shared.*` (единый источник истины — audit ARCH #7 / LEAN #10 / #11); package-local
делегаторы оставлены сознательно, чтобы use-case код звал короткое неквалифицированное
имя (`peerErrToStatus(err, kind, id)` вместо `shared.PeerErrToStatus(...)`) — единый
call-site стиль во всех пакетах. Это repo-wide конвенция: 5 видов делегаторов × 5
пакетов. Удалить только `peerErrToStatus` из двух пакетов, оставив остальные 4
делегатора-близнеца в тех же файлах, — непоследовательный churn против устоявшейся
конвенции; логика при этом уже не дублируется (живёт единожды в `shared`). Дрейф
поведения невозможен: изменение правится в одном `shared.*`, wrapper'ы его лишь
пробрасывают (sec-hardening r5b, LEAN-finding «peerErrToStatus wrappers»).

## Peer-client порты определены в `internal/clients/*`, а use-case их только алиасит

**Что.** `architecture.md` предписывает, что port-интерфейсы (`<Peer>Client`)
определяет внутренний слой (use-case). В kacho-nlb конкретные peer-порты и их
transfer-DTO физически объявлены в adapter-пакетах `internal/clients/*`
(`iam.ProjectClient`+`iam.Project`, `geo.RegionClient`/`ZoneClient`,
`vpc.SubnetClient`/`AddressClient`/`InternalAddressClient` + их request/response
DTO), а `apps/kacho/api/<res>/ports.go` их **алиасит** (`type ProjectClient =
iamclient.ProjectClient`). Формально стрелка зависимости смотрит из use-case в
adapter, а не наоборот.

**Почему by-design (а не инвертировать).** Peer-DTO — это тонкие value-структуры
над gRPC-stub'ами конкретного домена (owner API), у которых **единственный**
консумер — этот сервис; они не несут доменной логики kacho-nlb. Держать их
определение рядом с адаптером, который маршалит их из proto-stub'ов, — единый
источник истины формы; инверсия (объявить порты+DTO в use-case и реализовывать в
adapter) продублировала бы ~6 интерфейсов и ~10 DTO в двух местах и потребовала бы
конвертации proto↔use-case-DTO на каждом вызове ради чистоты стрелки, без выигрыша
в тестируемости (use-case-тесты всё равно строят те же value-структуры через
package-local fake'и). Замена одного консумера или второй контракт — редизайн
adapter-пакета в любом случае. Осознанный trade-off «single-definition DTO vs.
формальная dependency-inversion» в пользу первого (sec-hardening r6, ARCH-finding
«port ownership»). Композиционный wiring остаётся в `cmd/kacho-loadbalancer/main.go`.

## `statement_timeout=30s` энфорсится пулом (corelib), `cmd/migrator` освобождён

**Что.** Пул приложения (`coredb.NewPool`, `cmd/kacho-loadbalancer/main.go:139`)
ставит `statement_timeout=30000` (30 s) как `RuntimeParam` на КАЖДОМ соединении —
это делает `kacho-corelib/db.NewPool` (`cfg.ConnConfig.RuntimeParams["statement_timeout"]
= "30000"`). Server-side верхняя граница на любой request-path и фоновый
reconcile-запрос сверх gRPC-context-deadline. `lock_timeout` /
`idle_in_transaction_session_timeout` намеренно не заданы.

**Почему by-design (targeting ровно тот, которого хотел прежний r6-долг).**
`cmd/migrator` открывает СВОЁ `sql.Open("pgx", …)`-соединение (goose поверх него),
а НЕ `coredb.NewPool` — поэтому легитимно-длинный DDL/`CREATE INDEX`/backfill НЕ
обрывается 30-секундным лимитом (migration-abort был бы хуже удержанного коннекта).
Request-path И фоновые reconcile-джобы (`free_ip_runner`/`target_drain` держат тот
же app-pool `d.pool`) ограничены 30 s. `lock_timeout` не нужен: contested
write-пути — короткие атомарные CAS/`FOR NO KEY UPDATE`/`SKIP LOCKED`, а read-пути
keyset-клампятся (`pageSizeOrDefault` cap 1000, `ListTargets` — `MaxTargetsPerGroup`).
NB: прежняя запись «нет `statement_timeout`» (r6) устарела после апгрейда
`kacho-corelib` (pool теперь ставит 30 s) — факт исправлен как stale
(sec-hardening r8b, DATA-finding «no statement_timeout» → resolved).

## update_mask known-set валидируется per-resource inline, не через corelib `validate.UpdateMask`

**Что.** Три мутируемых ресурса (`loadbalancer`/`targetgroup`/`listener`) держат
собственный inline-цикл по `update_mask.paths` против package-local known-set
(`knownUpdateFields` / `knownUpdateFieldsTG` / `listenerMutableMaskPaths`), а не
зовут corelib `validate.UpdateMask(field, mask, known)`. Тексты unknown-field
ошибок при этом **разные по ресурсам** и намеренно зафиксированы: LB/TG отдают
`"unknown update_mask field: <p>"`, listener — `"field '<p>' is not recognised in
update_mask"`.

**Почему by-design (а не унифицировать в corelib).** Текст и форма error —
**часть контракта** (`api-conventions.md`: «Тексты — часть контракта; меняются
только осознанно (через тикет)»); они заассерчены в unit-тестах
(`targetgroup/update_test.go` — `"unknown update_mask field: foo_bar_baz"`).
Corelib `validate.UpdateMask` эмитит **другой** текст (`"unknown field in
update_mask: <f>"`) в **структурированной** `FieldViolation`-обёртке
(`coreerrors.InvalidArgument().AddFieldViolation`), тогда как nlb сейчас отдаёт
flat-`status.Errorf`. Переключение на corelib поменяло бы wire-текст И форму detail
для всех трёх Update — это ломающее изменение замороженного контракта, а не
contract-safe LEAN. Кроме того immutable-ветка (hard-immutable поле в mask →
фиксированный `"<field> is immutable after <R>.Create"`) — genuinely
resource-specific и в corelib-helper не укладывается. Унификация unknown-field
текста/формы к платформенному стандарту — отдельный **осознанный контрактный
тикет** (обновить unit-тесты + newman в том же PR), не sec-hardening-рефактор
(sec-hardening r7b, LEAN-finding «update_mask reimplemented per-resource»).

## `InternalResourceLifecycleService.Subscribe` — оркестрация в transport-handler'е (не отдельный UseCase)

**Что.** В отличие от остальных RPC сервиса (тонкий handler → per-RPC UseCase),
server-stream `Subscribe` держит всю оркестрацию прямо в
`internal_lifecycle/handler.go` (`Handler.Subscribe` / `streamSince`): semaphore
acquire, initial catchup батчами, cursor-менеджмент, NOTIFY wait-loop с
timeout/cancel-ветвлением, event→proto маппинг.

**Почему by-design (а не выносить SubscribeUseCase).** Dependency rule **не**
нарушен: весь DB-доступ спрятан за портом `kacho.LifecycleFeed` (pgx живёт в
repo-слое, не в handler'е) — handler зависит только от порта + proto-типов
stream'а. Оставшаяся логика — это управление жизненным циклом самого gRPC-стрима
(она держит `stream.Send`, `stream.Context()`, dedicated LISTEN-conn и
semaphore-слот на всё время подписки), которая неотделима от transport-объекта и
не является доменной бизнес-логикой kacho-nlb (нет мутаций ресурсов, нет
инвариантов — это чистый read-fan-out outbox-фида для kacho-iam). Выделение
«UseCase», который принимает `Sender`-callback и `LifecycleFeed`, добавило бы слой
косвенности вокруг того же стрим-controlflow без выигрыша в тестируемости (текущий
`handler_test.go` уже гоняет catchup/wait-loop через fake-`LifecycleFeed`). Осознанный
trade-off «stream-controlflow рядом со stream-объектом» в пользу когезии
transport-owned жизненного цикла стрима (sec-hardening r7b, ARCH-finding «Subscribe
orchestration in handler»).

## Фоновые reconcile-джобы — raw `*pgxpool.Pool` в use-case-слое (минуя CQRS Repository)

**Что.** Три фоновые джобы в `internal/apps/kacho/jobs/` (`free_ip_runner`,
`target_drain_runner`, `viporigin_reconcile`) держат `*pgxpool.Pool` и исполняют
hand-written SQL (`SELECT … FOR UPDATE SKIP LOCKED`, `DELETE`, INSERT в
`nlb_outbox`/`fga_register_outbox`) напрямую, минуя `kacho.Repository`/
`RepositoryWriter`, через который идут все per-RPC use-case'ы. Outbox-INSERT'ы в
`free_ip_runner.emitReconcileFinalize` повторяют форму каноничных эмиттеров
`pg.outboxEmitter`/`pg.fgaRegisterEmitter`.

**Почему by-design (а не рефакторить через RepositoryWriter).** Это не
request-path use-case'ы, а multi-replica admin-reconcilers: их суть — claim одной
застрявшей строки под `FOR UPDATE SKIP LOCKED`, удержание row-lock'а на время
внешнего release-вызова (network) и финализация — семантика, которая НЕ ложится на
per-resource CQRS-порт (тот моделирует ресурс-мутации, а не scan-claim-reconcile).
Паттерн — устоявшаяся конвенция сервиса (`TargetDrainRunner` той же формы; см.
`jobs/doc.go` «admin-job поверх *pgxpool.Pool, минуя [repo]»). Джобы —
**вне** dependency-rule request-path'а (нет доменных инвариантов ресурса, только
housekeeping-VIP-release/target-drain). Дрейф дублированных outbox-INSERT'ов от
каноничных эмиттеров закрыт тестами:
`free_ip_runner_integration_test.go`/`target_drain_runner_integration_test.go`
гоняют реальные INSERT'ы против той же схемы (NOT NULL/CHECK из миграций ловят
рассинхрон колонок на CI). Осознанный trade-off «reconcile-controlflow рядом с
raw-SQL» vs. слой косвенности через Repository, без выигрыша в тестируемости
(sec-hardening r8b, ARCH-finding «jobs bypass CQRS»).

## `domain/` импортирует сторонние value-библиотеки (не только stdlib + kacho-proto)

**Что.** `architecture.md` предписывает `domain/` импортировать ТОЛЬКО stdlib +
`kacho-proto`. Фактически `internal/domain/` импортирует
`github.com/H-BF/corlib/pkg/{dict,option}` (`types.go`, `target.go`, `listener.go`),
`go.uber.org/multierr` (`loadbalancer.go`, `target.go`, `health_check.go`,
`target_group.go`, `listener.go`) и
`github.com/PRO-Robotech/kacho-corelib/{errors,ids}` (`types.go`, `status.go`,
`builders.go`).

**Почему by-design (а не заменять на stdlib).** Все три — чистые value-библиотеки
(`dict`/`option` — generic-контейнеры, `multierr` — агрегация ошибок, corelib
`errors`/`ids` — in-org error-builder + ID-генерация); НИ pgx, НИ grpc-stubs, НИ
sqlc-типов, НИ adapter-зависимостей domain не тянет — дух dependency-rule (нет
утечки адаптера в entity-слой) соблюдён. `corelib/errors` даёт единый
gRPC-совместимый `InvalidArgument().AddFieldViolation`-формат (часть
error-контракта, см. `api-conventions.md`), а `option`/`dict` устраняют голые
`*T`/`map`-обёртки в self-validating newtype'ах. Замена на stdlib-эквиваленты
продублировала бы эти утилиты внутри сервиса без выигрыша в чистоте (те же
value-типы, но hand-rolled). Sanctioned location для этого отклонения — здесь
(git-youtrack.md «by-design → docs/architecture»), а не только inline-комментарий в
`types.go` (sec-hardening r8b, ARCH-finding «domain imports third-party»).
