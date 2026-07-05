-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose NO TRANSACTION
-- +goose Up

-- =============================================================================
-- VIP консолидируется с Listener на LoadBalancer (anycast active-active, INTERNAL)
-- =============================================================================
-- Tenant-facing VIP переезжает с Listener на LoadBalancer: один anycast-VIP на
-- сервис на семейство (address_v4/address_v6). Listener становится «портом на VIP
-- LB» и собственной аллокации больше не делает.
--
-- Within-service инварианты (ban #10):
--   * single-VIP-per-LB — одна строка load_balancers несёт одну address_v4/v6;
--     привязка адреса — атомарный CAS в коде repo (UPDATE … WHERE id=$ AND
--     (addr='' OR addr=$new) RETURNING), не в миграции.
--   * per-region UNIQUE — двойная claim одного IP в регионе ловится partial
--     UNIQUE-индексом (region_id, address_*) WHERE addr<>''. Для UNIQUE-индекса
--     опции NOT VALID нет, поэтому на живой таблице строится CONCURRENTLY (вне
--     транзакции) — отсюда директива NO TRANSACTION на весь файл.
--   * status-aware CHECK — однонаправленный: непустой address ⟹ scheme/family,
--     но НЕ наоборот (durable-handle INSERT с пустым address в CREATING обязан
--     проходить).
--
-- ВАЖНО (NO TRANSACTION): goose выполняет statement'ы этой миграции вне общей
-- транзакции, и database/sql может брать РАЗНЫЕ соединения из пула под разные
-- statement'ы. Поэтому `SET search_path` здесь ненадёжен — все объекты
-- квалифицируются схемой `kacho_nlb` явно. Statement'ы идемпотентны
-- (IF NOT EXISTS / guard), чтобы повторный прогон после mid-failure был безопасен.
--
-- Hard-cut: новые VIP-колонки DEFAULT '' (instant metadata-only ALTER без
-- table-rewrite); адреса существующих LB остаются пустыми до операторской
-- реаллокации в anycast (IP меняется). Миграция НЕ копирует legacy
-- listeners.allocated_address в address_v4/v6 — мис-лейбл zone-scoped VIP как
-- region-anycast запрещён.
--
-- Listener address-колонки (ip_version/address_id/allocated_address/subnet_id/
-- vip_origin) и денорм listeners.region_id здесь НЕ дропаются: это отдельный
-- ПОЗДНИЙ шаг ПОСЛЕ завершения mixed-version rollout. В окне раскатки pod старой
-- версии держит free_ip_runner, сканирующий listeners.allocated_address/
-- address_id/vip_origin (release осиротевших VIP) — drop этих колонок в 0009
-- сломал бы старые pod'ы. Новый runner re-home'ится на load_balancers
-- (load_balancers_reconcile_idx ниже); listener-колонки снимаются миграцией 0010+
-- после того, как старая версия выведена из кластера.

-- Output-only anycast-VIP и binding на vpc Address, per-family. DEFAULT '' даёт
-- instant metadata-only ALTER (без переписывания таблицы).
ALTER TABLE kacho_nlb.load_balancers
    ADD COLUMN IF NOT EXISTS address_v4     text   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS address_v6     text   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS address_id_v4  text   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS address_id_v6  text   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS ip_families    text[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS vip_origin_v4  text   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS vip_origin_v6  text   NOT NULL DEFAULT '';

-- Status-aware однонаправленный CHECK (NOT VALID — таблица наполнена; sync-
-- валидация use-case остаётся первичным гейтом). «Непустой address_v4 ⟹
-- type=INTERNAL И IPV4 объявлен в ip_families», НЕ «scheme ⟹ address присутствует»:
-- INSERT durable-handle (status='CREATING', address_v4='') до alloc проходит.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'load_balancers_address_v4_scheme_family_check'
           AND conrelid = 'kacho_nlb.load_balancers'::regclass
    ) THEN
        ALTER TABLE kacho_nlb.load_balancers
            ADD CONSTRAINT load_balancers_address_v4_scheme_family_check
            CHECK (
                address_v4 = ''
             OR (type = 'INTERNAL' AND 'IPV4' = ANY(ip_families))
            ) NOT VALID;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'load_balancers_address_v6_scheme_family_check'
           AND conrelid = 'kacho_nlb.load_balancers'::regclass
    ) THEN
        ALTER TABLE kacho_nlb.load_balancers
            ADD CONSTRAINT load_balancers_address_v6_scheme_family_check
            CHECK (
                address_v6 = ''
             OR (type = 'INTERNAL' AND 'IPV6' = ANY(ip_families))
            ) NOT VALID;
    END IF;
END
$$;
-- +goose StatementEnd

-- vip_origin_v4/v6 — DB-only дискриминатор источника VIP (parity с listeners.vip_origin,
-- 0005): '' (семейство отсутствует) | 'auto' (аллоцирован, Delete→FreeIP) | 'byo'
-- (принесён tenant'ом, Delete→ClearReference). Простой предикат → CHECK (ban #10).
-- Новые колонки DEFAULT '' → все строки проходят, CHECK добавляется validated.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'load_balancers_vip_origin_v4_check'
           AND conrelid = 'kacho_nlb.load_balancers'::regclass
    ) THEN
        ALTER TABLE kacho_nlb.load_balancers
            ADD CONSTRAINT load_balancers_vip_origin_v4_check
            CHECK (vip_origin_v4 IN ('', 'auto', 'byo'));
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'load_balancers_vip_origin_v6_check'
           AND conrelid = 'kacho_nlb.load_balancers'::regclass
    ) THEN
        ALTER TABLE kacho_nlb.load_balancers
            ADD CONSTRAINT load_balancers_vip_origin_v6_check
            CHECK (vip_origin_v6 IN ('', 'auto', 'byo'));
    END IF;
END
$$;
-- +goose StatementEnd

-- free_ip_runner re-home: скан осиротевших/in-flight LB-handle.
--   WHERE status IN ('CREATING','DELETING') AND updated_at < now() - <age>
--   ORDER BY updated_at ASC ... FOR UPDATE SKIP LOCKED.
-- Partial index покрывает ровно это множество (обычно крошечное), updated_at —
-- ведущая колонка под age-фильтр и ORDER BY (зеркало listeners_reconcile_idx).
CREATE INDEX IF NOT EXISTS load_balancers_reconcile_idx
    ON kacho_nlb.load_balancers (updated_at)
    WHERE status IN ('CREATING','DELETING');

-- per-region partial UNIQUE: один и тот же anycast-IP не привязывается к двум LB
-- в регионе (double-claim → 23505). CONCURRENTLY обязателен на живой таблице
-- (для UNIQUE опции NOT VALID нет). IF NOT EXISTS — на случай повторного прогона;
-- если предыдущий CONCURRENTLY-прогон упал, он оставит INVALID-индекс — его нужно
-- снять вручную (DROP INDEX CONCURRENTLY) перед повтором, IF NOT EXISTS его не
-- пересоздаст.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS load_balancers_region_v4_uniq
    ON kacho_nlb.load_balancers (region_id, address_v4)
    WHERE address_v4 <> '';

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS load_balancers_region_v6_uniq
    ON kacho_nlb.load_balancers (region_id, address_v6)
    WHERE address_v6 <> '';

-- VIP-уникальность переехала на LoadBalancer — listener-level индекс снимается.
-- (Hard-cut: новые листенеры VIP не несут; legacy-строки до remediation
-- допускают редкий дубль VIP в окне раскатки — приемлемо по §7.)
DROP INDEX IF EXISTS kacho_nlb.listeners_region_vip_uniq;

-- +goose Down

-- Восстанавливаем listener-level VIP UNIQUE из baseline 0001 (listener address-
-- колонки в 0009 не дропались, поэтому индекс воспроизводим как есть).
CREATE UNIQUE INDEX IF NOT EXISTS listeners_region_vip_uniq
    ON kacho_nlb.listeners (region_id, allocated_address, port, protocol)
    WHERE status <> 'DELETING' AND allocated_address <> '';

DROP INDEX IF EXISTS kacho_nlb.load_balancers_region_v6_uniq;
DROP INDEX IF EXISTS kacho_nlb.load_balancers_region_v4_uniq;
DROP INDEX IF EXISTS kacho_nlb.load_balancers_reconcile_idx;

ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_vip_origin_v6_check;
ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_vip_origin_v4_check;
ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_address_v6_scheme_family_check;
ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_address_v4_scheme_family_check;

ALTER TABLE kacho_nlb.load_balancers
    DROP COLUMN IF EXISTS vip_origin_v6,
    DROP COLUMN IF EXISTS vip_origin_v4,
    DROP COLUMN IF EXISTS ip_families,
    DROP COLUMN IF EXISTS address_id_v6,
    DROP COLUMN IF EXISTS address_id_v4,
    DROP COLUMN IF EXISTS address_v6,
    DROP COLUMN IF EXISTS address_v4;
