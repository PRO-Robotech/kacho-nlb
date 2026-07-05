-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- +goose StatementBegin

-- =============================================================================
-- VALIDATE CONSTRAINT для within-service инвариантов, добавленных как NOT VALID
-- (KAC sec-hardening r4, audit DATA #6, ban #10 DB-enforcement).
-- =============================================================================
-- Несколько within-service инвариантов (ban #10) добавлялись как `NOT VALID`
-- ради online-migration safety (ADD CONSTRAINT берёт только короткий lock, не
-- сканируя таблицу). NOT VALID энфорсит инвариант на ВСЕХ новых/изменяемых
-- строках, но НЕ проверяет уже существовавшие на момент миграции. Ни одна
-- последующая миграция не запускала `VALIDATE CONSTRAINT`, поэтому на
-- гипотетической уже-наполненной БД pre-existing строки навсегда обходили бы
-- инвариант. Для greenfield-сервиса (0001 — squashed baseline, живых pre-fill
-- строк нет) это no-op safety net; здесь он ЗАКРЫВАЕТ контракт DB-enforcement и
-- делает поведение единообразным на любой БД.
--
-- VALIDATE CONSTRAINT берёт SHARE UPDATE EXCLUSIVE lock (не блокирует
-- read/write) и однократно сканирует таблицу. Идемпотентно: валидация уже
-- валидного constraint — no-op. Валидируются ТОЛЬКО ныне живые NOT VALID
-- constraints (проверено против 0004/0011; NOT VALID из 0007/0009 к этому
-- моменту уже DROP-нуты в 0011).

-- 0004: составной FK «default_target_group_id ссылается на ПРИКРЕПлённую TG».
ALTER TABLE kacho_nlb.listeners
    VALIDATE CONSTRAINT listeners_default_tg_attached_fk;

-- 0011: placement↔type coupling (INTERNAL ⟹ placement ∈ {ZONAL,REGIONAL}).
ALTER TABLE kacho_nlb.load_balancers
    VALIDATE CONSTRAINT load_balancers_placement_type_check;

-- 0011: per-family address↔ip_families guard (непустой address ⟹ семейство ∈ ip_families).
ALTER TABLE kacho_nlb.load_balancers
    VALIDATE CONSTRAINT load_balancers_address_v4_family_check;
ALTER TABLE kacho_nlb.load_balancers
    VALIDATE CONSTRAINT load_balancers_address_v6_family_check;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- VALIDATE — монотонное усиление: сделать constraint снова NOT VALID нельзя
-- (Postgres не поддерживает «invalidate»; потребовался бы DROP+ADD NOT VALID,
-- что откатило бы уже проверенное состояние без пользы). Down — намеренный no-op.
SELECT 1;

-- +goose StatementEnd
