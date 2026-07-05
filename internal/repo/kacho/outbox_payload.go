// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package kacho

import "encoding/json"

// Canonical `nlb_outbox` JSON-payload key names — ЕДИНЫЙ источник истины,
// разделяемый КАЖДЫМ producer'ом (mutation use-cases) И consumer'ом
// (InternalResourceLifecycleService.Subscribe → kacho-iam FGA-sync).
//
// Ранее каждый producer вручную собирал `map[string]any` с inline-строками, а
// consumer (`extractPayloadFields`) читал ДРУГОЙ набор литералов
// (`parent_resource_id` / `old_project_id`), который НИ ОДИН producer не писал:
// listener-producer писал `load_balancer_id`, MOVED-producer — `src_project_id`.
// Как следствие `ResourceLifecycleEvent.ParentResourceId` / `.OldProjectId`,
// стримящиеся в kacho-iam, всегда были пустыми → iam не мог снести stale
// owner/hierarchy-tuples на Move (stale-permission leak). Централизация ключей
// здесь + типизированные builder (`LifecyclePayload.Map`) и parser
// (`ParseLifecyclePayload`) устраняют дрейф: обе стороны используют одни и те же
// имена ключей. Frozen-proto поля `ResourceLifecycleEvent` НЕ меняются — только
// ранее всегда-пустые поля теперь заполняются.
const (
	PayloadKeyID               = "id"
	PayloadKeyProjectID        = "project_id"
	PayloadKeyRegionID         = "region_id"
	PayloadKeyName             = "name"
	PayloadKeyStatus           = "status"
	PayloadKeyType             = "type"
	PayloadKeyProtocol         = "protocol"
	PayloadKeyPort             = "port"
	PayloadKeyTrigger          = "trigger"
	PayloadKeyParentResourceID = "parent_resource_id"
	PayloadKeyOldProjectID     = "old_project_id"
	PayloadKeyNewProjectID     = "new_project_id"
)

// LifecyclePayload — типизированный snapshot `nlb_outbox`-payload'а. Producer
// заполняет релевантные поля и вызывает Map(); Subscribe-consumer вызывает
// ParseLifecyclePayload, чтобы прочитать cross-cutting поля (ParentResourceID —
// parent-link Listener→LB; OldProjectID — исходный project для MOVED). Пустые
// поля в payload не сериализуются (минимальный snapshot; consumer при
// необходимости делает Get(id)).
type LifecyclePayload struct {
	// ID — resource id (LB / Listener / TargetGroup).
	ID string
	// ParentResourceID — id родительского ресурса (Listener → parent LB).
	// Пусто для project-scoped ресурсов (LB / TargetGroup), у которых родителя нет.
	ParentResourceID string
	ProjectID        string
	RegionID         string
	Name             string
	Status           string
	// Type — LB-only (network_load_balancer type).
	Type string
	// Protocol / Port — Listener-only.
	Protocol string
	Port     int32
	// Trigger — диагностический маркер cross-resource UPDATED-эмита
	// (`listener_created` / `listener_deleted` / ...).
	Trigger string
	// OldProjectID — исходный project ресурса для MOVED-события.
	OldProjectID string
	// NewProjectID — целевой project ресурса для MOVED-события.
	NewProjectID string
}

// Map — строит `map[string]any` для OutboxEmitter.Emit, включая только
// непустые поля (минимальный snapshot). Ключи — из констант выше.
func (p LifecyclePayload) Map() map[string]any {
	m := make(map[string]any, 12)
	putNonEmpty(m, PayloadKeyID, p.ID)
	putNonEmpty(m, PayloadKeyParentResourceID, p.ParentResourceID)
	putNonEmpty(m, PayloadKeyProjectID, p.ProjectID)
	putNonEmpty(m, PayloadKeyRegionID, p.RegionID)
	putNonEmpty(m, PayloadKeyName, p.Name)
	putNonEmpty(m, PayloadKeyStatus, p.Status)
	putNonEmpty(m, PayloadKeyType, p.Type)
	putNonEmpty(m, PayloadKeyProtocol, p.Protocol)
	putNonEmpty(m, PayloadKeyTrigger, p.Trigger)
	putNonEmpty(m, PayloadKeyOldProjectID, p.OldProjectID)
	putNonEmpty(m, PayloadKeyNewProjectID, p.NewProjectID)
	if p.Port != 0 {
		m[PayloadKeyPort] = p.Port
	}
	return m
}

func putNonEmpty(m map[string]any, key, val string) {
	if val != "" {
		m[key] = val
	}
}

// ParseLifecyclePayload — толерантный parser payload-JSON'а из `nlb_outbox`-row.
// Неизвестные ключи игнорируются; поле неверного типа игнорируется (остаётся
// пустым) — event должен дойти до подписчика даже с частично-битым payload'ом
// (graceful degradation). Ошибку возвращает ТОЛЬКО на невалидный JSON.
func ParseLifecyclePayload(raw []byte) (LifecyclePayload, error) {
	var p LifecyclePayload
	if len(raw) == 0 {
		return p, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return p, err
	}
	p.ID, _ = m[PayloadKeyID].(string)
	p.ParentResourceID, _ = m[PayloadKeyParentResourceID].(string)
	p.ProjectID, _ = m[PayloadKeyProjectID].(string)
	p.RegionID, _ = m[PayloadKeyRegionID].(string)
	p.Name, _ = m[PayloadKeyName].(string)
	p.Status, _ = m[PayloadKeyStatus].(string)
	p.Type, _ = m[PayloadKeyType].(string)
	p.Protocol, _ = m[PayloadKeyProtocol].(string)
	p.Trigger, _ = m[PayloadKeyTrigger].(string)
	p.OldProjectID, _ = m[PayloadKeyOldProjectID].(string)
	p.NewProjectID, _ = m[PayloadKeyNewProjectID].(string)
	if f, ok := m[PayloadKeyPort].(float64); ok {
		p.Port = int32(f)
	}
	return p, nil
}
