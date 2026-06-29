-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- +goose StatementBegin

-- =============================================================================
-- load_balancers.network_id — VPC-сеть приватного VIP (INTERNAL scheme)
-- =============================================================================
-- INTERNAL-LB размещает приватный VIP внутри VPC-сети → network_id обязателен.
-- EXTERNAL-LB несёт публичный VIP (не из сети) → network_id запрещён. Cross-field
-- инвариант энфорсится sync-валидацией на request-path (use-case) И DB CHECK как
-- defense-in-depth (ban #10). network_id — cross-service ref на kacho-vpc Network
-- (TEXT, без FK; существование валидируется peer-API vpc.NetworkService.Get).
-- Immutable после Create.

ALTER TABLE kacho_nlb.load_balancers
    ADD COLUMN network_id text NOT NULL DEFAULT '';

-- Cross-field CHECK: (type = 'INTERNAL') ⟺ (network_id непустой).
-- NOT VALID — constraint добавляется к уже наполненной таблице: legacy-LB,
-- созданные до появления network_id (INTERNAL c network_id=''), не валидируются
-- ретроспективно (grandfather), но энфорс применяется ко всем новым/изменяемым
-- строкам. Sync-валидация use-case остаётся первичным гейтом нового контракта.
ALTER TABLE kacho_nlb.load_balancers
    ADD CONSTRAINT load_balancers_network_id_scheme_check
    CHECK ((type = 'INTERNAL') = (network_id <> '')) NOT VALID;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE kacho_nlb.load_balancers
    DROP CONSTRAINT IF EXISTS load_balancers_network_id_scheme_check;
ALTER TABLE kacho_nlb.load_balancers
    DROP COLUMN IF EXISTS network_id;

-- +goose StatementEnd
