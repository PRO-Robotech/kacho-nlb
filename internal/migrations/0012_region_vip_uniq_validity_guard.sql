-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose NO TRANSACTION
-- +goose Up

-- =============================================================================
-- Region-VIP UNIQUE index validity guard / self-heal (backstop к 0009)
-- =============================================================================
-- 0009 создаёт load_balancers_region_v4_uniq / _v6_uniq через
-- CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS (partial UNIQUE — DB-level
-- backstop против двойного claim'а одного anycast-VIP в регионе; AttachVIP CAS
-- полагается на 23505 от этих индексов). Прерванный CONCURRENTLY-build (deadlock,
-- обрыв соединения, crash) оставляет INVALID-индекс, который НЕ энфорсит
-- уникальность; при повторном прогоне 0009 `IF NOT EXISTS` матчит INVALID-индекс
-- по имени и НЕ пересоздаёт его — инвариант молча остаётся отсутствующим, а
-- миграция рапортует успех. Правило #10: within-service инвариант обязан быть
-- DB-энфорсимым — значит и его валидность должна быть гарантирована.
--
-- Эта миграция:
--   1) обнаруживает INVALID-остатки region-uniq индексов и снимает их (обычный
--      DROP INDEX внутри DO — INVALID-индекс ничего не энфорсит, снятие дёшево и
--      безопасно; VALID-индекс НЕ трогаем, чтобы не открыть окно без уникальности);
--   2) пересобирает недостающие индексы CONCURRENTLY (IF NOT EXISTS → no-op, если
--      индекс уже валиден);
--   3) пост-условие: если после пересборки хоть один индекс отсутствует или
--      остался INVALID — RAISE EXCEPTION → миграция падает (прерванный build
--      никогда не запишется как успешный; повторный прогон само-лечится).
--
-- NO TRANSACTION: CREATE/DROP INDEX CONCURRENTLY нельзя в транзакции; DO-блоки
-- (детект+drop invalid, пост-assert) идут как отдельные autocommit-statement'ы,
-- обычный DROP INDEX внутри DO — транзакционен и допустим. Все объекты
-- квалифицируются схемой kacho_nlb явно (search_path в NO TRANSACTION ненадёжен).

-- (1) Снять INVALID-остатки (VALID-индексы не трогаем).
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_index i
          JOIN pg_class c ON c.oid = i.indexrelid
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'kacho_nlb'
           AND c.relname = 'load_balancers_region_v4_uniq'
           AND NOT i.indisvalid
    ) THEN
        DROP INDEX kacho_nlb.load_balancers_region_v4_uniq;
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_index i
          JOIN pg_class c ON c.oid = i.indexrelid
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'kacho_nlb'
           AND c.relname = 'load_balancers_region_v6_uniq'
           AND NOT i.indisvalid
    ) THEN
        DROP INDEX kacho_nlb.load_balancers_region_v6_uniq;
    END IF;
END
$$;
-- +goose StatementEnd

-- (2) Пересобрать недостающие индексы (идемпотентно: IF NOT EXISTS).
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS load_balancers_region_v4_uniq
    ON kacho_nlb.load_balancers (region_id, address_v4)
    WHERE address_v4 <> '';

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS load_balancers_region_v6_uniq
    ON kacho_nlb.load_balancers (region_id, address_v6)
    WHERE address_v6 <> '';

-- (3) Пост-условие: оба индекса обязаны существовать И быть VALID.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index i
          JOIN pg_class c ON c.oid = i.indexrelid
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'kacho_nlb'
           AND c.relname = 'load_balancers_region_v4_uniq'
           AND i.indisvalid
    ) THEN
        RAISE EXCEPTION 'load_balancers_region_v4_uniq missing or INVALID after rebuild — per-region VIP uniqueness is NOT enforced';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index i
          JOIN pg_class c ON c.oid = i.indexrelid
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'kacho_nlb'
           AND c.relname = 'load_balancers_region_v6_uniq'
           AND i.indisvalid
    ) THEN
        RAISE EXCEPTION 'load_balancers_region_v6_uniq missing or INVALID after rebuild — per-region VIP uniqueness is NOT enforced';
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down

-- No-op: эта миграция лишь чинит/валидирует индексы, созданные в 0009, и не
-- вводит собственных объектов схемы. Снятие region-uniq индексов принадлежит
-- Down-секции 0009.
SELECT 1;
