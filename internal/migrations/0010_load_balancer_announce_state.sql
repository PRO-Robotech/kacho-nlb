-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- +goose StatementBegin

-- =============================================================================
-- load_balancer_announce_state — наблюдаемая per-zone announce-state anycast-VIP
-- =============================================================================
-- Control-plane сторона feedback-петли: data plane анонсирует/отзывает anycast-VIP
-- из зон и репортит наблюдаемую per-zone announce-state обратно в nlb. Хранилище
-- этой проекции — инфра-чувствительные данные (security.md): BGP up/down,
-- route/VRF id, статус программирования ядра, числовой инфра-id. Доступны только
-- через Internal API (:9091, InternalLoadBalancerAnnounceService) — на публичную
-- проекцию NetworkLoadBalancer не выходят.
--
-- Зерно строки — per-(load_balancer, zone, ip_version): один зональный анонс
-- относится к одному семейству (IPV4/IPV6), а зона может анонсировать оба
-- семейства одновременно. Поэтому семейство входит в первичный ключ — иначе
-- upsert второго семейства затёр бы первое в той же зоне.
--
-- Within-service инвариант (ban #10): announce-row не существует без своего LB —
-- FK ON DELETE CASCADE снимает announce-state атомарно при удалении LB (same-DB
-- cascade, не cross-service). Идемпотентный upsert (ON CONFLICT) живёт в repo:
-- единственный writer — data plane, репорт повторяем.
CREATE TABLE IF NOT EXISTS kacho_nlb.load_balancer_announce_state (
    load_balancer_id  text         NOT NULL
        REFERENCES kacho_nlb.load_balancers(id) ON DELETE CASCADE,
    zone_id           text         NOT NULL,
    ip_version        text         NOT NULL DEFAULT '',
    bgp_session_up    boolean      NOT NULL DEFAULT false,
    route_id          text         NOT NULL DEFAULT '',
    vrf_id            text         NOT NULL DEFAULT '',
    kernel_programmed boolean      NOT NULL DEFAULT false,
    infra_id          bigint       NOT NULL DEFAULT 0,
    updated_at        timestamptz  NOT NULL DEFAULT now(),

    CONSTRAINT load_balancer_announce_state_pk
        PRIMARY KEY (load_balancer_id, zone_id, ip_version),
    CONSTRAINT load_balancer_announce_state_ip_version_check
        CHECK (ip_version IN ('', 'IPV4', 'IPV6'))
);

-- Индекс под чтение всей announce-state одного LB (GetAnnounceState). PK ведёт по
-- load_balancer_id, поэтому отдельный индекс — для read-проекции с явным ORDER BY
-- (zone_id, ip_version) без скана PK с лишними колонками.
CREATE INDEX IF NOT EXISTS load_balancer_announce_state_lb_idx
    ON kacho_nlb.load_balancer_announce_state (load_balancer_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS kacho_nlb.load_balancer_announce_state_lb_idx;
DROP TABLE IF EXISTS kacho_nlb.load_balancer_announce_state;

-- +goose StatementEnd
