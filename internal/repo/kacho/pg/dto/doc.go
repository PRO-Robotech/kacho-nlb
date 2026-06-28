// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package dto — мост domain ↔ DB-row (LabelsToJSONB / HealthCheckToJSONB /...).
//
// Используется pg-impl-репозиторием kacho-nlb (`internal/repo/kacho/pg`) для
// сериализации labels (jsonb) и health_check (jsonb) при INSERT/UPDATE, и для
// обратной десериализации при SELECT.
//
// Domain-пакет ничего не знает про JSONB-сериализацию (workspace CLAUDE.md
// «Чистая архитектура»); этот пакет — единственное место, где доменные типы
// превращаются в JSONB-tape и обратно.
package dto
