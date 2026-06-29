// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package kacho

import (
	"context"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// ListenerReaderIface — read-операции Listener.
type ListenerReaderIface interface {
	Get(ctx context.Context, id string) (*ListenerRecord, error)
	List(ctx context.Context, f ListenerFilter, p Pagination) ([]*ListenerRecord, string, error)
	ListByLB(ctx context.Context, lbID string, p Pagination) ([]*ListenerRecord, string, error)
}

// ListenerWriterIface — write-операции + read.
type ListenerWriterIface interface {
	ListenerReaderIface

	// Insert — INSERT listeners RETURNING. UNIQUE-violation на
	// (load_balancer_id, port, protocol) или (region_id, allocated_address,
	// port, protocol) WHERE status<>'DELETING' → ErrAlreadyExists.
	Insert(ctx context.Context, l *domain.Listener) (*ListenerRecord, error)

	// Update — UPDATE listeners SET mutable fields (name/description/labels/
	// default_target_group_id/proxy_protocol_v2). Immutable lb_id/protocol/port/
	// ip_version/address_id обрабатываются в use-case (rejected sync if в mask).
	Update(ctx context.Context, l *domain.Listener) (*ListenerRecord, error)

	// SetStatusCAS — atomic CAS на status (CREATING → ACTIVE → DELETING).
	SetStatusCAS(ctx context.Context, id string, expected, newStatus domain.ListenerStatus) (*ListenerRecord, error)

	// SetAllocatedAddress проставляет allocated_address после VIP-аллокации
	// (worker-side). UNIQUE-violation на region/VIP/port/proto →
	// ErrAlreadyExists (race с параллельной аллокацией того же VIP).
	SetAllocatedAddress(ctx context.Context, id, address string) (*ListenerRecord, error)

	// SetVIP персистит address_id + allocated_address в durable-handle строку
	// (TX-2 create-саги) ОТДЕЛЬНЫМ немедленным commit'ом сразу после
	// VIP-аллокации, ещё в status='CREATING'. Делает address_id durable до
	// перехода в ACTIVE: при сбое финала free_ip_runner детерминированно
	// освобождает VIP по address_id. UNIQUE-violation (region, VIP, port,
	// protocol) → ErrAlreadyExists (гонка аллокации того же VIP).
	SetVIP(ctx context.Context, id, addressID, allocatedAddress string) (*ListenerRecord, error)

	// MoveProject — каскад от LB.MoveProject; вызывается из
	// LoadBalancerWriterIface.MoveProject внутри той же TX.
	MoveProject(ctx context.Context, lbID, newProjectID string) (int64, error)

	// Delete — DELETE listeners WHERE id=$1.
	Delete(ctx context.Context, id string) error
}
