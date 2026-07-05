-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- +goose StatementBegin

-- =============================================================================
-- LoadBalancer placement + per-family VIP-source link/allocate model
-- =============================================================================
-- Форма VIP переопределяется: источник задаётся пофамильно (subnet-auto /
-- address-link / platform-public) и резолвится в связанный vpc Address. Плоский
-- placement_type (ZONAL|REGIONAL) фиксирует размещение INTERNAL-LB;
-- disabled_announce_zones — deny-list зон anycast-drain (REGIONAL only).
--
-- Снимаются поля прежней модели: network derived (не tenant-facing → колонка
-- network_id вместе с её scheme-CHECK убирается), security_group_ids (таргеты
-- фаерволятся на своей стороне), cross_zone_enabled (свёрнут в placement).
--
-- Within-service инварианты (ban #10): placement↔type и drain↔placement — DB
-- CHECK; single-VIP-per-LB (CAS-attach) и per-region UNIQUE переиспользуются из
-- прежней схемы без изменений.

-- placement_type — размещение INTERNAL-LB. Пусто для EXTERNAL. DEFAULT '' даёт
-- instant metadata-only ALTER (без переписывания таблицы).
ALTER TABLE kacho_nlb.load_balancers
    ADD COLUMN IF NOT EXISTS placement_type          text   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS disabled_announce_zones text[] NOT NULL DEFAULT '{}';

-- placement↔type coupling: INTERNAL ⟹ placement ∈ {ZONAL,REGIONAL}; иначе пусто.
-- NOT VALID — таблица наполнена: legacy-INTERNAL c placement_type='' не
-- валидируются ретроспективно (sync-precheck use-case — первичный гейт), но
-- энфорс применяется ко всем новым/изменяемым строкам.
ALTER TABLE kacho_nlb.load_balancers
    ADD CONSTRAINT load_balancers_placement_type_check
    CHECK (
        (type = 'INTERNAL' AND placement_type IN ('ZONAL','REGIONAL'))
     OR (type <> 'INTERNAL' AND placement_type = '')
    ) NOT VALID;

-- drain↔placement: непустой disabled_announce_zones только для REGIONAL. Пустой
-- набор проходит при любом placement — существующие строки валидны, CHECK VALID.
ALTER TABLE kacho_nlb.load_balancers
    ADD CONSTRAINT load_balancers_disabled_announce_zones_check
    CHECK (cardinality(disabled_announce_zones) = 0 OR placement_type = 'REGIONAL');

-- network derived, не tenant-facing → колонка и её scheme-CHECK снимаются
-- (иначе (type='INTERNAL')=(network_id<>'') запрещал бы новый INTERNAL без
-- network_id). security_group_ids / cross_zone_enabled — сняты (см. заголовок).
ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_network_id_scheme_check;
ALTER TABLE kacho_nlb.load_balancers
    DROP COLUMN IF EXISTS network_id,
    DROP COLUMN IF EXISTS security_group_ids,
    DROP COLUMN IF EXISTS cross_zone_enabled;

-- Status-aware address CHECK развязывается от type: и INTERNAL, и EXTERNAL несут
-- VIP → остаётся только family-guard (непустой address ⟹ семейство объявлено в
-- ip_families, sequencing саги). Прежние type-coupled CHECK (0009) снимаются.
ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_address_v4_scheme_family_check;
ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_address_v6_scheme_family_check;
ALTER TABLE kacho_nlb.load_balancers
    ADD CONSTRAINT load_balancers_address_v4_family_check
    CHECK (address_v4 = '' OR 'IPV4' = ANY(ip_families)) NOT VALID;
ALTER TABLE kacho_nlb.load_balancers
    ADD CONSTRAINT load_balancers_address_v6_family_check
    CHECK (address_v6 = '' OR 'IPV6' = ANY(ip_families)) NOT VALID;

-- vip_origin дискриминатор: 'byo' переименован в 'linked' (link существующего
-- Address). Переносим существующие значения и меняем допустимое множество.
UPDATE kacho_nlb.load_balancers SET vip_origin_v4 = 'linked' WHERE vip_origin_v4 = 'byo';
UPDATE kacho_nlb.load_balancers SET vip_origin_v6 = 'linked' WHERE vip_origin_v6 = 'byo';

ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_vip_origin_v4_check;
ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_vip_origin_v6_check;
ALTER TABLE kacho_nlb.load_balancers
    ADD CONSTRAINT load_balancers_vip_origin_v4_check
    CHECK (vip_origin_v4 IN ('', 'auto', 'linked'));
ALTER TABLE kacho_nlb.load_balancers
    ADD CONSTRAINT load_balancers_vip_origin_v6_check
    CHECK (vip_origin_v6 IN ('', 'auto', 'linked'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_vip_origin_v6_check;
ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_vip_origin_v4_check;
UPDATE kacho_nlb.load_balancers SET vip_origin_v4 = 'byo' WHERE vip_origin_v4 = 'linked';
UPDATE kacho_nlb.load_balancers SET vip_origin_v6 = 'byo' WHERE vip_origin_v6 = 'linked';
ALTER TABLE kacho_nlb.load_balancers
    ADD CONSTRAINT load_balancers_vip_origin_v4_check
    CHECK (vip_origin_v4 IN ('', 'auto', 'byo'));
ALTER TABLE kacho_nlb.load_balancers
    ADD CONSTRAINT load_balancers_vip_origin_v6_check
    CHECK (vip_origin_v6 IN ('', 'auto', 'byo'));

ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_address_v6_family_check;
ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_address_v4_family_check;
ALTER TABLE kacho_nlb.load_balancers
    ADD CONSTRAINT load_balancers_address_v4_scheme_family_check
    CHECK (address_v4 = '' OR (type = 'INTERNAL' AND 'IPV4' = ANY(ip_families))) NOT VALID;
ALTER TABLE kacho_nlb.load_balancers
    ADD CONSTRAINT load_balancers_address_v6_scheme_family_check
    CHECK (address_v6 = '' OR (type = 'INTERNAL' AND 'IPV6' = ANY(ip_families))) NOT VALID;

ALTER TABLE kacho_nlb.load_balancers
    ADD COLUMN IF NOT EXISTS cross_zone_enabled boolean NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS security_group_ids text[]  NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS network_id         text    NOT NULL DEFAULT '';
ALTER TABLE kacho_nlb.load_balancers
    ADD CONSTRAINT load_balancers_sg_internal_check
    CHECK (cardinality(security_group_ids) = 0 OR type = 'INTERNAL');
ALTER TABLE kacho_nlb.load_balancers
    ADD CONSTRAINT load_balancers_network_id_scheme_check
    CHECK ((type = 'INTERNAL') = (network_id <> '')) NOT VALID;

ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_disabled_announce_zones_check;
ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_placement_type_check;
ALTER TABLE kacho_nlb.load_balancers
    DROP COLUMN IF EXISTS disabled_announce_zones,
    DROP COLUMN IF EXISTS placement_type;

-- +goose StatementEnd
