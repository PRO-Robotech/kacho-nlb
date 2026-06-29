-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- +goose StatementBegin

-- =============================================================================
-- listeners.default_target_group_id — композитный FK на приаттаченный TG
-- =============================================================================
-- Within-service инвариант (ban #10): default_target_group_id листенера обязан
-- ссылаться на target group, ПРИАТТАЧЕННУЮ к тому же load balancer. Раньше связь
-- была software-only (check-then-act, TOCTOU) — routing-intent мог указывать на
-- неприаттаченный либо несуществующий TG. Выражаем инвариант композитным FK на
-- pivot attached_target_groups(load_balancer_id, target_group_id): одна
-- DB-конструкция закрывает и существование TG, и факт его attachment к LB.
--
-- Пустой default ('') допустим (листенер без default-routing). Postgres FK не
-- поддерживает WHERE-условие, поэтому вводим generated nullable-проекцию
-- default_tg_fk = NULLIF(default_target_group_id, ''):
--   * default пуст  → default_tg_fk = NULL → композитный FK с MATCH SIMPLE НЕ
--     проверяется (хотя бы одна referencing-колонка NULL → match не требуется);
--   * default задан → обе колонки заполнены → FK энфорсится: требуется строка
--     pivot (load_balancer_id, target_group_id).
--
-- ON DELETE RESTRICT: detach приаттаченного TG, который является default
-- какого-либо листенера, блокируется на DB-уровне (нельзя удалить pivot-строку,
-- на которую ссылается default_tg_fk). Закрывает TOCTOU-гонку detach vs
-- сохранение default — гонкам остаётся только корректный исход.

ALTER TABLE kacho_nlb.listeners
    ADD COLUMN default_tg_fk text
        GENERATED ALWAYS AS (NULLIF(default_target_group_id, '')) STORED;

-- NOT VALID — FK добавляется к уже наполненной таблице: существующие listener-строки
-- не валидируются ретроспективно (grandfather legacy на populated-DB), но энфорс
-- (existence + attachment + ON DELETE RESTRICT) применяется ко всем новым/изменяемым
-- строкам. На fresh-DB эффект эквивалентен обычному FK.
ALTER TABLE kacho_nlb.listeners
    ADD CONSTRAINT listeners_default_tg_attached_fk
        FOREIGN KEY (load_balancer_id, default_tg_fk)
        REFERENCES kacho_nlb.attached_target_groups (load_balancer_id, target_group_id)
        ON DELETE RESTRICT NOT VALID;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE kacho_nlb.listeners
    DROP CONSTRAINT IF EXISTS listeners_default_tg_attached_fk;
ALTER TABLE kacho_nlb.listeners
    DROP COLUMN IF EXISTS default_tg_fk;

-- +goose StatementEnd
