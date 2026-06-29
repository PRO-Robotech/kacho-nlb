-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- +goose StatementBegin

-- =============================================================================
-- listeners.vip_origin — дискриминатор источника VIP (release-ветка на Delete)
-- =============================================================================
-- Within-service инвариант (ban #10): выбор release-ветки при Listener.Delete
-- (auto → FreeIP, byo → ClearReference) обязан опираться на надёжный признак, а
-- не на имя Address. Прежняя реализация решала эвристикой по ПРЕФИКСУ имени
-- Address (`nlb-listener-`) — tenant с BYO-Address, названным так же, терял свой
-- статический адрес (FreeIP удалял его). Дискриминатор хранится колонкой:
-- проставляется на Create (auto-alloc → 'auto'; переданный tenant'ом address_id
-- → 'byo') и читается на Delete как источник истины.
--
-- DEFAULT 'auto': все существующие строки получают 'auto' без сложного backfill
-- (в `listeners` нет origin-сигнала, кроме address_id+allocated_address). CHECK
-- ограничивает домен значений.
--
-- Уже существующие BYO-листенеры временно несут 'auto' (см. риск ниже) — их
-- backfill в реальный 'byo' выполняет ОТДЕЛЬНЫЙ idempotent Go-reconcile на boot
-- (vpc.AddressService.Get per address_id), запускаемый ДО приёма трафика;
-- readiness держится not-ready (fail-closed), пока reconcile не завершён, чтобы
-- ни один Delete не ушёл по неверной ветке. На свежем стенде (нет строк)
-- reconcile вырождается в no-op. Подробности — docs/architecture/14-vip-origin.md.
--
-- vip_origin — DB-only внутренний дискриминатор; в публичную proto-проекцию
-- листенера НЕ попадает (security.md — это деталь реализации, не tenant-поле).

ALTER TABLE kacho_nlb.listeners
    ADD COLUMN vip_origin text NOT NULL DEFAULT 'auto';

ALTER TABLE kacho_nlb.listeners
    ADD CONSTRAINT listeners_vip_origin_check
        CHECK (vip_origin IN ('byo','auto'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE kacho_nlb.listeners
    DROP CONSTRAINT IF EXISTS listeners_vip_origin_check;
ALTER TABLE kacho_nlb.listeners
    DROP COLUMN IF EXISTS vip_origin;

-- +goose StatementEnd
