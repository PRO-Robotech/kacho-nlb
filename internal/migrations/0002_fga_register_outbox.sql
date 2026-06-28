-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- +goose StatementBegin

-- =============================================================================
-- Kachō NLB — fga_register_outbox (SEC-D, transactional-outbox FGA-via-IAM)
-- =============================================================================
-- SEC-D (epic §3.1 Вариант A) устраняет прямой best-effort FGA dual-write
-- (GitHub Issue N5). Намерение «register/unregister owner-hierarchy tuple» для
-- созданного/удалённого ресурса (lb_network_load_balancer / lb_listener /
-- lb_target_group) пишется строкой в эту таблицу **в той же writer-tx, что и
-- INSERT/DELETE ресурса** — один commit, no dual-write. Отдельный
-- register-drainer (corelib outbox/drainer, FOR UPDATE SKIP LOCKED claim)
-- применяет каждую строку через kacho-iam InternalIAMService.RegisterResource /
-- UnregisterResource (idempotent SEC-A контракт) по mTLS.
--
-- Схема СОВМЕСТИМА с corelib outbox/drainer (drainer.Config.Table):
--   id (bigserial PK), event_type, payload (jsonb), created_at, sent_at,
--   last_error, attempt_count — ровно те колонки, что читает claimRows /
--   markSuccess / markFailure / markPoisoned (см. kacho-corelib/outbox/drainer).
--
-- OQ-SEC-D-1: ОТДЕЛЬНАЯ таблица (не переиспользование nlb_outbox) — изолирует
-- FGA-relay-drainer от domain D-13 watch-cursor stream (другой applier, другой
-- failure-режим). nlb_outbox имеет несовместимую схему (sequence_no PK,
-- resource_type/action, processed_at) под D-13 lifecycle stream.
-- =============================================================================

CREATE SEQUENCE kacho_nlb.fga_register_outbox_id_seq
    AS bigint START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;

CREATE TABLE kacho_nlb.fga_register_outbox (
    id            bigint                   NOT NULL DEFAULT nextval('kacho_nlb.fga_register_outbox_id_seq'::regclass) PRIMARY KEY,
    -- event_type ∈ {fga.register, fga.unregister}. CHECK — belt-and-suspenders
    -- против typo в caller'е (SQLSTATE 23514 → ErrInvalidArg в mapPgErr).
    event_type    text                     NOT NULL,
    -- payload — JSON-сериализованный набор tuple-намерений ресурса
    -- (project-hierarchy + creator + parent-link), формат — domain/fgaintent.
    -- OQ-SEC-D-2: набор tuple одной строкой (атомарность «весь набор ресурса»).
    payload       jsonb                    NOT NULL,
    -- resource_kind / resource_id — для observability/трассировки в логах и
    -- для assert'ов в integration-тестах (Сценарии SEC-D-01..06). НЕ читаются
    -- drainer'ом (он работает только по id/event_type/payload/attempt_count).
    resource_kind text                     NOT NULL DEFAULT '',
    resource_id   text                     NOT NULL DEFAULT '',
    created_at    timestamp with time zone NOT NULL DEFAULT now(),
    -- sent_at IS NULL → pending; NOT NULL → applied (drainer mark'нул).
    sent_at       timestamp with time zone,
    last_error    text,
    -- attempt_count — попытки drainer'а; ≥ MaxAttempts → poisoned-skip.
    attempt_count integer                  NOT NULL DEFAULT 0,
    CONSTRAINT fga_register_outbox_event_type_check
        CHECK (event_type = ANY (ARRAY['fga.register'::text, 'fga.unregister'::text])),
    CONSTRAINT fga_register_outbox_payload_object_ck
        CHECK (jsonb_typeof(payload) = 'object'::text)
);

ALTER SEQUENCE kacho_nlb.fga_register_outbox_id_seq
    OWNED BY kacho_nlb.fga_register_outbox.id;

-- Partial index на pending-rows — drainer claim'ит только sent_at IS NULL
-- (claimRows WHERE sent_at IS NULL AND attempt_count < MaxAttempts ORDER BY id).
CREATE INDEX fga_register_outbox_pending_idx
    ON kacho_nlb.fga_register_outbox (id) WHERE sent_at IS NULL;

-- +goose StatementEnd

-- +goose StatementBegin
-- fga_register_outbox_notify — pg_notify('kacho_nlb_fga_register_outbox', id)
-- на каждый INSERT. register-drainer LISTEN'ит этот канал (corelib drainer
-- listenLoop) и wake-up'ится без poll-задержки. Payload = id (drainer его
-- игнорирует — делает атомарный claim по всему батчу).
CREATE OR REPLACE FUNCTION kacho_nlb.fga_register_outbox_notify() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM pg_notify('kacho_nlb_fga_register_outbox', NEW.id::text);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER fga_register_outbox_notify_trg
    AFTER INSERT ON kacho_nlb.fga_register_outbox
    FOR EACH ROW EXECUTE FUNCTION kacho_nlb.fga_register_outbox_notify();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS fga_register_outbox_notify_trg ON kacho_nlb.fga_register_outbox;
DROP FUNCTION IF EXISTS kacho_nlb.fga_register_outbox_notify();
DROP TABLE IF EXISTS kacho_nlb.fga_register_outbox;
DROP SEQUENCE IF EXISTS kacho_nlb.fga_register_outbox_id_seq;
-- +goose StatementEnd
