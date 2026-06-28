// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package kacho

import "context"

// AttachedTargetGroupReaderIface — read для M:N pivot LoadBalancer ↔ TG.
type AttachedTargetGroupReaderIface interface {
	// Get — одна pivot-row по PK (load_balancer_id, target_group_id) или
	// ErrNotFound.
	Get(ctx context.Context, lbID, tgID string) (*AttachedTargetGroupRecord, error)

	// ListByLB — все TG, привязанные к данному LB.
	ListByLB(ctx context.Context, lbID string) ([]*AttachedTargetGroupRecord, error)

	// ListByTG — все LB, к которым привязан данный TG (для blocked-Delete checks).
	ListByTG(ctx context.Context, tgID string) ([]*AttachedTargetGroupRecord, error)
}

// AttachedTargetGroupWriterIface — write для pivot.
type AttachedTargetGroupWriterIface interface {
	AttachedTargetGroupReaderIface

	// Attach — INSERT attached_target_groups ON CONFLICT DO NOTHING.
	// Возвращает (record, attached=true) если row вставлена, либо
	// (existing-record, attached=false) если pair уже существовал
	// (idempotent attach —).
	//
	// FK-violation на load_balancer_id / target_group_id →
	// ErrFailedPrecondition (LB или TG не существуют).
	Attach(ctx context.Context, lbID, tgID string, priority int32) (*AttachedTargetGroupRecord, bool, error)

	// Detach — DELETE attached_target_groups WHERE pair. 0 affected → no-op
	// (idempotent detach).
	Detach(ctx context.Context, lbID, tgID string) error
}
