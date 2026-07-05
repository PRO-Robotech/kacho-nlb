-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up

-- =============================================================================
-- lb_status_recompute() — CAS-guard on the final status write (KAC sec-hardening
-- r3, finding #2).
-- =============================================================================
-- Baseline 0001 defined the trigger with a stale-read TOCTOU: it reads
-- `cur_status` (unlocked), gates on `cur_status IN ('INACTIVE','ACTIVE')`, then
-- issues an UNCONDITIONAL `UPDATE load_balancers SET status=new_status WHERE
-- id=...`. A listener/attach INSERT/DELETE takes only a FOR KEY SHARE FK lock on
-- the parent LB row, which does NOT conflict with an explicit status transition
-- (SetStatusCAS → FOR NO KEY UPDATE). So a recompute can read ACTIVE, an explicit
-- ACTIVE→STOPPING (or →DELETING) can commit in between, and the recompute's
-- unconditional write then clobbers it back to ACTIVE/INACTIVE — a lost update on
-- the very column the guard is meant to protect, plus a spurious UPDATED
-- lifecycle-outbox event to IAM.
--
-- Fix (project-rule #10, atomic CAS for within-service invariants): the final
-- write becomes `... WHERE id=affected_lb_id AND status=cur_status`. Because a
-- single-statement UPDATE on one row takes a row lock, a concurrent explicit
-- transition holding that row forces the recompute UPDATE to wait; after the
-- transition commits, EvalPlanQual re-checks `status=cur_status`, the CAS misses,
-- 0 rows change, and STOPPING/DELETING survives. The outbox INSERT is gated on the
-- UPDATE actually affecting a row (GET DIAGNOSTICS), so no spurious event fires.
--
-- CREATE OR REPLACE (not an edit of 0001 — project-rule #5: applied migrations are
-- immutable). Function body is otherwise identical to 0001.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION kacho_nlb.lb_status_recompute() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    affected_lb_id  text;
    cur_status      text;
    cur_project_id  text;
    has_listener    boolean;
    has_attached    boolean;
    new_status      text;
    recomputed_rows integer;
BEGIN
    -- Какой LB затронут текущей TG/Listener-операцией.
    IF TG_TABLE_NAME = 'listeners' THEN
        affected_lb_id := COALESCE(NEW.load_balancer_id, OLD.load_balancer_id);
    ELSIF TG_TABLE_NAME = 'attached_target_groups' THEN
        affected_lb_id := COALESCE(NEW.load_balancer_id, OLD.load_balancer_id);
    ELSE
        RETURN COALESCE(NEW, OLD);
    END IF;

    IF affected_lb_id IS NULL THEN
        RETURN COALESCE(NEW, OLD);
    END IF;

    SELECT status, project_id INTO cur_status, cur_project_id
      FROM kacho_nlb.load_balancers
     WHERE id = affected_lb_id;

    -- LB удалён или не существует — нечего пересчитывать.
    IF cur_status IS NULL THEN
        RETURN COALESCE(NEW, OLD);
    END IF;

    -- Trigger preserves explicit transitions (GWT-DB-004).
    IF cur_status NOT IN ('INACTIVE','ACTIVE') THEN
        RETURN COALESCE(NEW, OLD);
    END IF;

    SELECT EXISTS (
        SELECT 1 FROM kacho_nlb.listeners
         WHERE load_balancer_id = affected_lb_id
           AND status <> 'DELETING'
    ) INTO has_listener;

    SELECT EXISTS (
        SELECT 1 FROM kacho_nlb.attached_target_groups
         WHERE load_balancer_id = affected_lb_id
    ) INTO has_attached;

    IF has_listener AND has_attached THEN
        new_status := 'ACTIVE';
    ELSE
        new_status := 'INACTIVE';
    END IF;

    IF new_status <> cur_status THEN
        -- CAS: пишем только если status не увели из-под нас конкурентным
        -- explicit-переходом между нашим SELECT и этим UPDATE. Row-lock на LB
        -- сериализует нас с SetStatusCAS; после его commit'а EvalPlanQual
        -- пере-проверяет `status = cur_status` → CAS-miss → 0 rows → не затираем
        -- STOPPING/DELETING (lost-update больше не происходит).
        UPDATE kacho_nlb.load_balancers
           SET status = new_status
         WHERE id = affected_lb_id
           AND status = cur_status;
        GET DIAGNOSTICS recomputed_rows = ROW_COUNT;

        -- Outbox-событие только если пересчёт реально применился (иначе spurious
        -- UPDATED в IAM при проигранном CAS).
        IF recomputed_rows > 0 THEN
            INSERT INTO kacho_nlb.nlb_outbox
                (resource_type, resource_id, project_id, action, payload)
            VALUES (
                'nlb_load_balancer',
                affected_lb_id,
                cur_project_id,
                'UPDATED',
                jsonb_build_object(
                    'id', affected_lb_id,
                    'status', new_status,
                    'recomputed', true
                )
            );
        END IF;
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$;
-- +goose StatementEnd

-- +goose Down

-- Восстанавливаем 0001-версию функции (unconditional write, без CAS-guard).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION kacho_nlb.lb_status_recompute() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    affected_lb_id text;
    cur_status     text;
    cur_project_id text;
    has_listener   boolean;
    has_attached   boolean;
    new_status     text;
BEGIN
    IF TG_TABLE_NAME = 'listeners' THEN
        affected_lb_id := COALESCE(NEW.load_balancer_id, OLD.load_balancer_id);
    ELSIF TG_TABLE_NAME = 'attached_target_groups' THEN
        affected_lb_id := COALESCE(NEW.load_balancer_id, OLD.load_balancer_id);
    ELSE
        RETURN COALESCE(NEW, OLD);
    END IF;

    IF affected_lb_id IS NULL THEN
        RETURN COALESCE(NEW, OLD);
    END IF;

    SELECT status, project_id INTO cur_status, cur_project_id
      FROM kacho_nlb.load_balancers
     WHERE id = affected_lb_id;

    IF cur_status IS NULL THEN
        RETURN COALESCE(NEW, OLD);
    END IF;

    IF cur_status NOT IN ('INACTIVE','ACTIVE') THEN
        RETURN COALESCE(NEW, OLD);
    END IF;

    SELECT EXISTS (
        SELECT 1 FROM kacho_nlb.listeners
         WHERE load_balancer_id = affected_lb_id
           AND status <> 'DELETING'
    ) INTO has_listener;

    SELECT EXISTS (
        SELECT 1 FROM kacho_nlb.attached_target_groups
         WHERE load_balancer_id = affected_lb_id
    ) INTO has_attached;

    IF has_listener AND has_attached THEN
        new_status := 'ACTIVE';
    ELSE
        new_status := 'INACTIVE';
    END IF;

    IF new_status <> cur_status THEN
        UPDATE kacho_nlb.load_balancers
           SET status = new_status
         WHERE id = affected_lb_id;

        INSERT INTO kacho_nlb.nlb_outbox
            (resource_type, resource_id, project_id, action, payload)
        VALUES (
            'nlb_load_balancer',
            affected_lb_id,
            cur_project_id,
            'UPDATED',
            jsonb_build_object(
                'id', affected_lb_id,
                'status', new_status,
                'recomputed', true
            )
        );
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$;
-- +goose StatementEnd
