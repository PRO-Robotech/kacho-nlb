# Безопасность: Internal-vs-external + инфра-чувствительные данные

## Internal-vs-external (ban #6)

`Internal.*` методы **не публикуются на external TLS endpoint** (`api.kacho.local:443`,
advertised для внешних клиентов). Они живут на cluster-internal listener (:9091) и могут
быть проброшены через api-gateway REST mux **только** на cluster-internal listener (UI,
admin-tooling, port-forward).

Текущие Internal admin-ресурсы (kacho-only, нет на external): `AddressPool`
(`/vpc/v1/addressPools`, kacho-vpc); `Region`/`Zone` (`/compute/v1/regions`, `/compute/v1/zones`, kacho-compute).

**Admin-UI правило**: любой новый RPC для admin-UI, которого нет в публичном API ресурса —
добавлять **только в `Internal*`-сервис** на :9091 и регистрировать через `*InternalAddr`-блок
в `kacho-api-gateway/internal/restmux/mux.go`. Не расширять публичные сервисы под admin-нужды —
это засветит admin-функции на external endpoint. Ответственность за корректную регистрацию —
агент `api-gateway-registrar`.

## Инфра-чувствительные данные — ТОЛЬКО в Internal*-API

Любая информация, раскрытие которой помогает картировать/таргетировать физику и data-plane,
живёт **исключительно** в `Internal*`-API (:9091) — никогда на публичной gRPC/REST-поверхности:

- **placement / физика**: на каком хосте лежит ресурс; инвентарь/ёмкость хостов.
- **underlay / транспорт**: транспортные/маршрутные id хостов, carrier-адреса, туннельные эндпоинты, id VRF/routing-таблиц.
- **wiring**: имена host-интерфейсов, netns, gateway-anchor'ы, id контейнеров на хостах, статусы программирования ядра.
- **числовой инфра-идентификатор** ресурса.

**Публичная поверхность** ресурса показывает только tenant-facing «намерение + результат»:
id, name/labels, привязки (project/network/subnet/instance), выделенный tenant-адрес(а), `status`.
«Как разложено по железу» — только через `Internal*`.

**Две проекции ресурса** (допустимо): публичная (lean, tenant-facing) + internal (full,
с инфра-полями) — отдельный internal-message либо поле «internal-only, не заполняется в
публичных ответах». (Шире и строже, чем ban #6 про *методы* — здесь про *данные*.)

Зачем: defense-in-depth — даже скомпрометированный публичный API не должен раскрыть
физическую топологию/placement (разведка для lateral movement; tenant A не должен вывести
«мой и чужой инстанс на одном железе»).

## AuthN/AuthZ (текущее состояние)

- Per-RPC authz-gate: `InternalIAMService.Check` (vpc/compute читают caller-identity из
  metadata `x-kacho-project-id` / `x-kacho-admin` / `x-kacho-actor`; identity-носитель — `internal/tenant`).
- В dev-mode (без AuthN) — anonymous → full access (backward-compat); production-mode
  (`KACHO_VPC_AUTH_MODE=production`) — anonymous fail-closed.
- Публичный `List<Resource>` обязан фильтровать результат через listauthz (CI-гейт `make audit-list-filter`).
- Реальный AuthN (validated JWT/IAM-token) — приходит с интеграцией IAM; downstream API
  (`tenant.TenantFromCtx`, `AssertProjectOwnership`) не меняется.
