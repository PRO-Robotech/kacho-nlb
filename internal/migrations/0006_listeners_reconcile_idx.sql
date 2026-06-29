-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- +goose StatementBegin

-- =============================================================================
-- listeners_reconcile_idx — partial index под free_ip_runner reconciler
-- =============================================================================
-- free_ip_runner сканирует «застрявшие» листенеры в нетерминальном состоянии
-- (durable-handle create-orphan в 'CREATING' либо незавершённый Delete в
-- 'DELETING') старше age-порога и освобождает их VIP (FreeIP/ClearReference по
-- address_id) с последующим удалением строки. Запрос —
--   WHERE status IN ('DELETING','CREATING') AND updated_at < now() - <age>
--   ORDER BY updated_at ASC ... FOR UPDATE SKIP LOCKED.
-- Partial index покрывает ровно это множество строк (обычно крошечное — только
-- in-flight/осиротевшие саги), не раздувая горячий индекс по ACTIVE-листенерам;
-- updated_at — ведущая колонка под age-фильтр и ORDER BY.

CREATE INDEX listeners_reconcile_idx
    ON kacho_nlb.listeners (updated_at)
    WHERE status IN ('DELETING','CREATING');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS kacho_nlb.listeners_reconcile_idx;

-- +goose StatementEnd
