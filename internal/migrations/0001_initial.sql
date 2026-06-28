-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- +goose StatementBegin

-- =============================================================================
-- Kachō NLB — clean baseline (squashed)
-- =============================================================================
-- Greenfield baseline для kacho-nlb (L4 Network Load Balancer control-plane).
-- Все таблицы / constraint / индексы / триггеры / helper-функции живут в
-- схеме `kacho_nlb` сразу (skill evgeniy §5 E.4). CHECK constraints inline с
-- CREATE TABLE (skill evgeniy §5 E.5).
--
-- Source: design `docs/superpowers/specs/2026-05-23-kacho-nlb-design.md` §5
--         acceptance `docs/specs/sub-phase-4.0-nlb-acceptance.md` §10 (GWT-DB-001..015)
--
-- Состав:
--   - helper-функции: kacho_labels_valid, nlb_outbox_notify, lb_status_recompute
--   - tables (8):
--       operations, load_balancers, listeners, target_groups, targets,
--       attached_target_groups, nlb_outbox, nlb_watch_cursors.
--   - sequences: nlb_outbox_sequence_no_seq (owned by nlb_outbox.sequence_no).
--   - triggers: nlb_outbox_notify_trg, listeners_lb_status_recompute_trg,
--               attached_tg_lb_status_recompute_trg.
-- =============================================================================

CREATE SCHEMA IF NOT EXISTS kacho_nlb;
SET search_path TO kacho_nlb, public;

-- +goose StatementEnd

-- =============================================================================
-- Helper functions
-- =============================================================================

-- +goose StatementBegin
-- kacho_labels_valid — проверка labels JSONB:
--   * cardinality ≤ 64 (GWT-DB-002)
--   * key   regex `^[a-z][-_./@0-9a-z]{0,62}$` (63 char max)
--   * value regex `^[-_./@0-9a-zA-Z]{0,63}$`   (63 char max, allow empty)
-- Используется в CHECK constraint всех ресурсов с labels.
CREATE OR REPLACE FUNCTION kacho_nlb.kacho_labels_valid(lbls jsonb) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE AS $fn$
DECLARE
    k text;
    v text;
    n int;
BEGIN
    IF lbls IS NULL THEN
        RETURN true;
    END IF;
    -- JSONB-null (`'null'::jsonb`) — легитимная сериализация Go nil-map'а.
    IF jsonb_typeof(lbls) = 'null' THEN
        RETURN true;
    END IF;
    IF jsonb_typeof(lbls) <> 'object' THEN
        RETURN false;
    END IF;
    SELECT count(*) INTO n FROM jsonb_object_keys(lbls);
    IF n > 64 THEN
        RETURN false;
    END IF;
    FOR k, v IN SELECT key, value FROM jsonb_each_text(lbls) LOOP
        IF k !~ '^[a-z][-_./@0-9a-z]{0,62}$' THEN
            RETURN false;
        END IF;
        IF v !~ '^[-_./@0-9a-zA-Z]{0,63}$' THEN
            RETURN false;
        END IF;
    END LOOP;
    RETURN true;
END;
$fn$;
-- +goose StatementEnd

-- +goose StatementBegin
-- nlb_outbox_notify — pg_notify на каждом INSERT в nlb_outbox.
-- Используется InternalResourceLifecycleService для realtime event push (LISTEN/NOTIFY).
CREATE OR REPLACE FUNCTION kacho_nlb.nlb_outbox_notify() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('nlb_outbox', NEW.sequence_no::text);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
-- lb_status_recompute — пересчёт load_balancers.status на основе наличия
-- listeners (ACTIVE/CREATING/UPDATING) и attached target groups.
-- Trigger срабатывает только когда current status ∈ ('INACTIVE','ACTIVE') —
-- explicit transitions CREATING / STARTING / STOPPING / STOPPED / DELETING
-- сохраняются (GWT-DB-004).
--
-- Если status изменился (INACTIVE ↔ ACTIVE) — эмитим UPDATED событие в
-- nlb_outbox (D-13 lifecycle stream для kacho-iam).
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

-- +goose StatementBegin

-- =============================================================================
-- operations (LRO — Long-Running Operations; kacho-corelib pattern,
-- inline в baseline per kacho-vpc / kacho-compute convention).
-- Включает principal-поля из kacho-corelib/migrations/common/0002 (KAC-105).
-- =============================================================================

CREATE TABLE kacho_nlb.operations (
    id                     text         PRIMARY KEY,
    description            text         NOT NULL,
    created_at             timestamptz  NOT NULL DEFAULT now(),
    created_by             text         NOT NULL DEFAULT 'anonymous',
    modified_at            timestamptz  NOT NULL DEFAULT now(),
    done                   boolean      NOT NULL DEFAULT false,
    metadata_type          text,
    metadata_data          bytea,
    resource_id            text,
    -- denorm: для per-resource ListOperations + (project_id, created_at) keyset.
    resource_type          text,
    project_id             text         NOT NULL DEFAULT '',
    principal_type         text         NOT NULL DEFAULT 'system',
    principal_id           text         NOT NULL DEFAULT 'bootstrap',
    principal_display_name text         NOT NULL DEFAULT 'System',
    error_code             integer,
    error_message          text,
    error_details          bytea,
    response_type          text,
    response_data          bytea,

    CONSTRAINT operations_resource_type_check
        CHECK (resource_type IS NULL OR resource_type IN (
            'nlb_load_balancer','nlb_listener','nlb_target_group'
        ))
);

-- (project_id, created_at DESC, id) — keyset для ListOperations per project.
CREATE INDEX operations_project_created_idx
    ON kacho_nlb.operations (project_id, created_at DESC, id);
-- (resource_type, resource_id, created_at DESC) — per-resource history; partial — только заполненные.
CREATE INDEX operations_resource_history_idx
    ON kacho_nlb.operations (resource_type, resource_id, created_at DESC)
    WHERE resource_id IS NOT NULL;
-- (done) WHERE done=false — для worker'а Operations.Worker (перевод pending → done).
CREATE INDEX operations_in_flight_idx
    ON kacho_nlb.operations (done) WHERE done = false;

-- =============================================================================
-- load_balancers
-- =============================================================================

CREATE TABLE kacho_nlb.load_balancers (
    id                   text         PRIMARY KEY,
    project_id           text         NOT NULL,
    region_id            text         NOT NULL,
    created_at           timestamptz  NOT NULL DEFAULT now(),
    updated_at           timestamptz  NOT NULL DEFAULT now(),
    name                 text         NOT NULL DEFAULT '',
    description          text         NOT NULL DEFAULT '',
    labels               jsonb        NOT NULL DEFAULT '{}'::jsonb,
    type                 text         NOT NULL,
    status               text         NOT NULL DEFAULT 'INACTIVE',
    session_affinity     text         NOT NULL DEFAULT 'FIVE_TUPLE',
    cross_zone_enabled   boolean      NOT NULL DEFAULT true,
    deletion_protection  boolean      NOT NULL DEFAULT false,

    CONSTRAINT load_balancers_name_check
        CHECK (name ~ '^([a-z]([-a-z0-9]{1,61}[a-z0-9])?)?$'),
    CONSTRAINT load_balancers_description_check
        CHECK (length(description) <= 256),
    CONSTRAINT load_balancers_labels_valid
        CHECK (kacho_nlb.kacho_labels_valid(labels)),
    CONSTRAINT load_balancers_type_check
        CHECK (type IN ('EXTERNAL','INTERNAL')),
    CONSTRAINT load_balancers_status_check
        CHECK (status IN ('CREATING','STARTING','ACTIVE','STOPPING','STOPPED','DELETING','INACTIVE')),
    CONSTRAINT load_balancers_session_affinity_check
        CHECK (session_affinity IN ('FIVE_TUPLE','CLIENT_IP_ONLY'))
);

-- Partial UNIQUE (project_id, name) WHERE name <> '' — GWT-DB-005.
CREATE UNIQUE INDEX load_balancers_project_name_uniq
    ON kacho_nlb.load_balancers (project_id, name) WHERE name <> '';
-- Keyset pagination (project_id, created_at DESC, id).
CREATE INDEX load_balancers_project_created_idx
    ON kacho_nlb.load_balancers (project_id, created_at DESC, id);
CREATE INDEX load_balancers_region_idx
    ON kacho_nlb.load_balancers (region_id);
-- GIN на labels для эффективного `labels @> '{...}'::jsonb` (GWT-DB-013).
CREATE INDEX load_balancers_labels_gin
    ON kacho_nlb.load_balancers USING gin (labels jsonb_path_ops);

-- =============================================================================
-- listeners
-- =============================================================================
-- FK на load_balancers ON DELETE RESTRICT — delete order bottom-up (Listener →
-- AttachedTG → LB). region_id денормализован для VIP UNIQUE constraint
-- (GWT-DB-007). project_id денормализован для keyset pagination.

CREATE TABLE kacho_nlb.listeners (
    id                       text         PRIMARY KEY,
    load_balancer_id         text         NOT NULL
        REFERENCES kacho_nlb.load_balancers(id) ON DELETE RESTRICT,
    project_id               text         NOT NULL,
    region_id                text         NOT NULL,
    created_at               timestamptz  NOT NULL DEFAULT now(),
    updated_at               timestamptz  NOT NULL DEFAULT now(),
    name                     text         NOT NULL DEFAULT '',
    description              text         NOT NULL DEFAULT '',
    labels                   jsonb        NOT NULL DEFAULT '{}'::jsonb,
    protocol                 text         NOT NULL,
    port                     integer      NOT NULL,
    target_port              integer      NOT NULL,
    ip_version               text         NOT NULL,
    -- VIP refs: address_id — kacho-vpc Address id (BYO либо auto-allocated);
    -- allocated_address — резолвленный IP-string (для UNIQUE region/vip/port/proto).
    address_id               text         NOT NULL DEFAULT '',
    allocated_address        text         NOT NULL DEFAULT '',
    subnet_id                text         NOT NULL DEFAULT '',
    proxy_protocol_v2        boolean      NOT NULL DEFAULT false,
    default_target_group_id  text         NOT NULL DEFAULT '',
    status                   text         NOT NULL DEFAULT 'CREATING',

    CONSTRAINT listeners_name_check
        CHECK (name ~ '^([a-z]([-a-z0-9]{1,61}[a-z0-9])?)?$'),
    CONSTRAINT listeners_description_check
        CHECK (length(description) <= 256),
    CONSTRAINT listeners_labels_valid
        CHECK (kacho_nlb.kacho_labels_valid(labels)),
    CONSTRAINT listeners_protocol_check
        CHECK (protocol IN ('TCP','UDP')),
    CONSTRAINT listeners_port_check
        CHECK (port BETWEEN 1 AND 65535),
    CONSTRAINT listeners_target_port_check
        CHECK (target_port BETWEEN 1 AND 65535),
    CONSTRAINT listeners_ip_version_check
        CHECK (ip_version IN ('IPV4','IPV6')),
    CONSTRAINT listeners_status_check
        CHECK (status IN ('CREATING','ACTIVE','UPDATING','DELETING'))
);

-- UNIQUE (load_balancer_id, port, protocol) — GWT-DB-006.
CREATE UNIQUE INDEX listeners_lb_port_proto_uniq
    ON kacho_nlb.listeners (load_balancer_id, port, protocol);
-- Partial UNIQUE (load_balancer_id, name) WHERE name <> ''.
CREATE UNIQUE INDEX listeners_lb_name_uniq
    ON kacho_nlb.listeners (load_balancer_id, name) WHERE name <> '';
-- Partial UNIQUE (region_id, allocated_address, port, protocol) WHERE status<>'DELETING' — GWT-DB-007.
CREATE UNIQUE INDEX listeners_region_vip_uniq
    ON kacho_nlb.listeners (region_id, allocated_address, port, protocol)
    WHERE status <> 'DELETING' AND allocated_address <> '';
-- Keyset (project_id, created_at DESC, id).
CREATE INDEX listeners_project_created_idx
    ON kacho_nlb.listeners (project_id, created_at DESC, id);
CREATE INDEX listeners_lb_idx
    ON kacho_nlb.listeners (load_balancer_id);
CREATE INDEX listeners_address_idx
    ON kacho_nlb.listeners (address_id) WHERE address_id <> '';
CREATE INDEX listeners_default_tg_idx
    ON kacho_nlb.listeners (default_target_group_id) WHERE default_target_group_id <> '';
CREATE INDEX listeners_labels_gin
    ON kacho_nlb.listeners USING gin (labels jsonb_path_ops);

-- =============================================================================
-- target_groups
-- =============================================================================

CREATE TABLE kacho_nlb.target_groups (
    id                            text         PRIMARY KEY,
    project_id                    text         NOT NULL,
    region_id                     text         NOT NULL,
    created_at                    timestamptz  NOT NULL DEFAULT now(),
    updated_at                    timestamptz  NOT NULL DEFAULT now(),
    name                          text         NOT NULL DEFAULT '',
    description                   text         NOT NULL DEFAULT '',
    labels                        jsonb        NOT NULL DEFAULT '{}'::jsonb,
    -- HealthCheck — JSONB (см. design §2.2 + acceptance TGR-003..006).
    health_check                  jsonb        NOT NULL DEFAULT '{}'::jsonb,
    deregistration_delay_seconds  integer      NOT NULL DEFAULT 300,
    slow_start_seconds            integer      NOT NULL DEFAULT 0,
    status                        text         NOT NULL DEFAULT 'ACTIVE',

    CONSTRAINT target_groups_name_check
        CHECK (name ~ '^([a-z]([-a-z0-9]{1,61}[a-z0-9])?)?$'),
    CONSTRAINT target_groups_description_check
        CHECK (length(description) <= 256),
    CONSTRAINT target_groups_labels_valid
        CHECK (kacho_nlb.kacho_labels_valid(labels)),
    CONSTRAINT target_groups_dereg_delay_check
        CHECK (deregistration_delay_seconds BETWEEN 0 AND 3600),
    CONSTRAINT target_groups_slow_start_check
        CHECK (slow_start_seconds BETWEEN 0 AND 900),
    CONSTRAINT target_groups_status_check
        CHECK (status IN ('ACTIVE','DELETING'))
);

-- Partial UNIQUE (project_id, name) WHERE name <> '' — TGR-014.
CREATE UNIQUE INDEX target_groups_project_name_uniq
    ON kacho_nlb.target_groups (project_id, name) WHERE name <> '';
CREATE INDEX target_groups_project_created_idx
    ON kacho_nlb.target_groups (project_id, created_at DESC, id);
CREATE INDEX target_groups_region_idx
    ON kacho_nlb.target_groups (region_id);
CREATE INDEX target_groups_labels_gin
    ON kacho_nlb.target_groups USING gin (labels jsonb_path_ops);

-- =============================================================================
-- targets — embedded child of target_groups.
-- =============================================================================
-- 4-way identity oneof (exactly-one CHECK): instance_id | nic_id |
-- (ip_ref.subnet_id + ip_ref.address) | (external_ip.address + external_ip.zone_id).
-- Partial UNIQUE NULLS NOT DISTINCT per identity-type (GWT-DB-008).
-- Drain consistency CHECK (GWT-DB-012):
--   status='ACTIVE'   → drain_started_at IS NULL
--   status='DRAINING' → drain_started_at IS NOT NULL

CREATE TABLE kacho_nlb.targets (
    id                  text         PRIMARY KEY,
    target_group_id     text         NOT NULL
        REFERENCES kacho_nlb.target_groups(id) ON DELETE RESTRICT,
    created_at          timestamptz  NOT NULL DEFAULT now(),
    updated_at          timestamptz  NOT NULL DEFAULT now(),
    -- 4-way identity (exactly one set).
    instance_id         text,
    nic_id              text,
    ip_ref_subnet_id    text,
    ip_ref_address      text,
    external_ip_address text,
    external_ip_zone_id text,
    -- weight 0..1000.
    weight              integer      NOT NULL DEFAULT 100,
    -- runtime state.
    status              text         NOT NULL DEFAULT 'ACTIVE',
    drain_started_at    timestamptz,

    CONSTRAINT targets_weight_check
        CHECK (weight BETWEEN 0 AND 1000),
    CONSTRAINT targets_status_check
        CHECK (status IN ('ACTIVE','DRAINING')),
    -- GWT-DB-009: 4-way oneof exactly-one (defense-in-depth, parity с domain.Validate).
    CONSTRAINT targets_identity_exactly_one
        CHECK (
            (CASE WHEN instance_id         IS NOT NULL THEN 1 ELSE 0 END)
          + (CASE WHEN nic_id              IS NOT NULL THEN 1 ELSE 0 END)
          + (CASE WHEN ip_ref_subnet_id    IS NOT NULL OR ip_ref_address    IS NOT NULL THEN 1 ELSE 0 END)
          + (CASE WHEN external_ip_address IS NOT NULL OR external_ip_zone_id IS NOT NULL THEN 1 ELSE 0 END)
            = 1
        ),
    -- ip_ref — оба под-поля выставлены вместе.
    CONSTRAINT targets_ip_ref_both_or_neither
        CHECK (
            (ip_ref_subnet_id IS NULL AND ip_ref_address IS NULL)
         OR (ip_ref_subnet_id IS NOT NULL AND ip_ref_address IS NOT NULL)
        ),
    -- external_ip.zone_id optional — но address обязателен если zone_id задан.
    CONSTRAINT targets_external_ip_address_present
        CHECK (
            external_ip_zone_id IS NULL
         OR external_ip_address IS NOT NULL
        ),
    -- GWT-DB-012: drain consistency.
    CONSTRAINT targets_drain_consistency
        CHECK (
            (status = 'ACTIVE'   AND drain_started_at IS NULL)
         OR (status = 'DRAINING' AND drain_started_at IS NOT NULL)
        )
);

-- GWT-DB-008: partial UNIQUE per identity-type (NULLS NOT DISTINCT в семантике —
-- partial WHERE col IS NOT NULL обеспечивает то же поведение).
CREATE UNIQUE INDEX targets_instance_id_uniq
    ON kacho_nlb.targets (target_group_id, instance_id)
    WHERE instance_id IS NOT NULL;
CREATE UNIQUE INDEX targets_nic_id_uniq
    ON kacho_nlb.targets (target_group_id, nic_id)
    WHERE nic_id IS NOT NULL;
CREATE UNIQUE INDEX targets_ip_ref_uniq
    ON kacho_nlb.targets (target_group_id, ip_ref_subnet_id, ip_ref_address)
    WHERE ip_ref_subnet_id IS NOT NULL AND ip_ref_address IS NOT NULL;
CREATE UNIQUE INDEX targets_external_ip_uniq
    ON kacho_nlb.targets (target_group_id, external_ip_address)
    WHERE external_ip_address IS NOT NULL;

CREATE INDEX targets_tg_idx
    ON kacho_nlb.targets (target_group_id);
-- Index для Phase B drain-runner: DELETE WHERE status='DRAINING'.
CREATE INDEX targets_draining_idx
    ON kacho_nlb.targets (drain_started_at)
    WHERE status = 'DRAINING';

-- =============================================================================
-- attached_target_groups — M:N pivot LoadBalancer ↔ TargetGroup.
-- =============================================================================
-- PK composite (load_balancer_id, target_group_id) — GWT-DB-011.
-- FK both RESTRICT — delete order bottom-up (Detach перед LB/TG delete).
-- priority 0..1000 CHECK (NLB-035).

CREATE TABLE kacho_nlb.attached_target_groups (
    load_balancer_id  text         NOT NULL
        REFERENCES kacho_nlb.load_balancers(id) ON DELETE RESTRICT,
    target_group_id   text         NOT NULL
        REFERENCES kacho_nlb.target_groups(id) ON DELETE RESTRICT,
    priority          integer      NOT NULL DEFAULT 100,
    attached_at       timestamptz  NOT NULL DEFAULT now(),

    PRIMARY KEY (load_balancer_id, target_group_id),

    CONSTRAINT attached_target_groups_priority_check
        CHECK (priority BETWEEN 0 AND 1000)
);

CREATE INDEX attached_target_groups_tg_idx
    ON kacho_nlb.attached_target_groups (target_group_id);

-- =============================================================================
-- nlb_outbox + sequence + LISTEN/NOTIFY trigger.
-- =============================================================================
-- D-13 lifecycle stream к kacho-iam: каждая мутация ресурса эмитит row в той
-- же TX. Trigger nlb_outbox_notify_trg шлёт pg_notify('nlb_outbox', seq).

CREATE SEQUENCE kacho_nlb.nlb_outbox_sequence_no_seq
    START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;

CREATE TABLE kacho_nlb.nlb_outbox (
    sequence_no    bigint       NOT NULL DEFAULT nextval('kacho_nlb.nlb_outbox_sequence_no_seq'::regclass) PRIMARY KEY,
    resource_type  text         NOT NULL,
    resource_id    text         NOT NULL,
    project_id     text         NOT NULL DEFAULT '',
    action         text         NOT NULL,
    payload        jsonb        NOT NULL DEFAULT '{}'::jsonb,
    emitted_at     timestamptz  NOT NULL DEFAULT now(),
    processed_at   timestamptz,

    CONSTRAINT nlb_outbox_resource_type_check
        CHECK (resource_type IN ('nlb_load_balancer','nlb_listener','nlb_target_group')),
    CONSTRAINT nlb_outbox_action_check
        CHECK (action IN ('CREATED','UPDATED','DELETED','MOVED','FAILED'))
);

ALTER SEQUENCE kacho_nlb.nlb_outbox_sequence_no_seq OWNED BY kacho_nlb.nlb_outbox.sequence_no;

CREATE INDEX nlb_outbox_seq_idx          ON kacho_nlb.nlb_outbox (sequence_no);
CREATE INDEX nlb_outbox_resource_idx     ON kacho_nlb.nlb_outbox (resource_type, sequence_no);
CREATE INDEX nlb_outbox_project_idx
    ON kacho_nlb.nlb_outbox (project_id, sequence_no) WHERE project_id <> '';
CREATE INDEX nlb_outbox_unprocessed_idx
    ON kacho_nlb.nlb_outbox (sequence_no) WHERE processed_at IS NULL;

-- =============================================================================
-- nlb_watch_cursors — per-subscriber cursor для D-13 catchup-семантики.
-- =============================================================================

CREATE TABLE kacho_nlb.nlb_watch_cursors (
    subscriber_id     text         NOT NULL PRIMARY KEY,
    last_sequence_no  bigint       NOT NULL DEFAULT 0,
    updated_at        timestamptz  NOT NULL DEFAULT now()
);

-- =============================================================================
-- Triggers (LISTEN/NOTIFY + lb_status recompute)
-- =============================================================================

CREATE TRIGGER nlb_outbox_notify_trg
    AFTER INSERT ON kacho_nlb.nlb_outbox
    FOR EACH ROW EXECUTE FUNCTION kacho_nlb.nlb_outbox_notify();

-- AFTER INSERT/UPDATE-OF-status/DELETE listeners — пересчёт LB.status.
CREATE TRIGGER listeners_lb_status_recompute_ins_trg
    AFTER INSERT ON kacho_nlb.listeners
    FOR EACH ROW EXECUTE FUNCTION kacho_nlb.lb_status_recompute();

CREATE TRIGGER listeners_lb_status_recompute_upd_trg
    AFTER UPDATE OF status ON kacho_nlb.listeners
    FOR EACH ROW
    WHEN (OLD.status IS DISTINCT FROM NEW.status)
    EXECUTE FUNCTION kacho_nlb.lb_status_recompute();

CREATE TRIGGER listeners_lb_status_recompute_del_trg
    AFTER DELETE ON kacho_nlb.listeners
    FOR EACH ROW EXECUTE FUNCTION kacho_nlb.lb_status_recompute();

-- AFTER INSERT/DELETE attached_target_groups — пересчёт LB.status.
CREATE TRIGGER attached_tg_lb_status_recompute_ins_trg
    AFTER INSERT ON kacho_nlb.attached_target_groups
    FOR EACH ROW EXECUTE FUNCTION kacho_nlb.lb_status_recompute();

CREATE TRIGGER attached_tg_lb_status_recompute_del_trg
    AFTER DELETE ON kacho_nlb.attached_target_groups
    FOR EACH ROW EXECUTE FUNCTION kacho_nlb.lb_status_recompute();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Greenfield baseline: down — drop schema целиком (с CASCADE на все объекты).
DROP SCHEMA IF EXISTS kacho_nlb CASCADE;
SET search_path TO public;

-- +goose StatementEnd
