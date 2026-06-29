-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- +goose StatementBegin

-- =============================================================================
-- load_balancers.security_group_ids — набор vpc.SecurityGroup (control-plane intent)
-- =============================================================================
-- Описывает допустимый inbound к VIP. Каждый элемент — cross-service ref на
-- kacho-vpc SecurityGroup (TEXT, без FK; существование + same-network валидируются
-- peer-API vpc.SecurityGroupService.Get на request-path). Mutable: Update заменяет
-- набор целиком (single-statement UPDATE атомарно переписывает text[] под row-lock'ом
-- → конкурентные replace'ы сериализуются без torn-state). Валиден только для INTERNAL
-- (SG живут внутри VPC-сети) — энфорсится sync-валидацией (use-case) И DB CHECK как
-- defense-in-depth (ban #10).

ALTER TABLE kacho_nlb.load_balancers
    ADD COLUMN security_group_ids text[] NOT NULL DEFAULT '{}';

-- Cross-field CHECK: непустой набор SG допустим только для INTERNAL.
ALTER TABLE kacho_nlb.load_balancers
    ADD CONSTRAINT load_balancers_sg_internal_check
    CHECK (cardinality(security_group_ids) = 0 OR type = 'INTERNAL');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_sg_internal_check;
ALTER TABLE kacho_nlb.load_balancers
    DROP COLUMN IF EXISTS security_group_ids;

-- +goose StatementEnd
