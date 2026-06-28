-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up

-- Additive, nullable account_id denormalization on kacho_nlb.operations so the
-- corelib operations.Repo INSERT (CreateWithPrincipal) succeeds. corelib main
-- (#24, cb438e4) now INSERTs account_id UNCONDITIONALLY; without this column every
-- nlb async mutation (Create/Update/Delete → Operation row) fails with 42703
-- undefined_column.
--
-- This mirrors the corelib common migration
-- (kacho-corelib/migrations/common/0003_operations_account_id.sql) and the IAM
-- copy (kacho-iam/internal/migrations/0016_operations_account_id.sql): the
-- kacho_nlb.operations table is the NLB copy of the shared operations schema
-- (it carries the principal_* columns inline in the 0001 baseline), so the column
-- must be added here too.
--
-- ADDITIVE / BACK-COMPAT: nullable, no DEFAULT, no NOT NULL. account_id stays
-- NULL for nlb — NLB metadata carries no exact-name account_id field, so corelib
-- extractAccountID returns "" → SQL NULL. account_id is an IAM-only
-- denormalization; nlb operations stay out of any account-scoped listing.
--
-- partial index (account_id, created_at, id) WHERE account_id IS NOT NULL —
-- covers account-scoped cursor pagination (WHERE account_id = $x ORDER BY
-- created_at, id) and does NOT index the NULL rows (no bloat from nlb operations,
-- which all leave account_id NULL).

ALTER TABLE kacho_nlb.operations
  ADD COLUMN account_id text NULL;

CREATE INDEX operations_account_id_idx
  ON kacho_nlb.operations (account_id, created_at, id)
  WHERE account_id IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS kacho_nlb.operations_account_id_idx;

ALTER TABLE kacho_nlb.operations
  DROP COLUMN account_id;
