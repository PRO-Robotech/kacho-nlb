// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// validateSecurityGroups — sync-precheck набора security_group_ids через
// vpc.SecurityGroupService.Get (ребро nlb→vpc, request-path, fail-closed).
// Каждый SG обязан существовать И принадлежать сети networkID (same-network).
// not-found/чужая сеть → InvalidArgument; vpc недоступен → Unavailable. Пустой
// набор или nil-client (dev-mode без peer) — no-op.
func validateSecurityGroups(
	ctx context.Context, client SecurityGroupClient, networkID string, sgs []domain.SecurityGroupID,
) error {
	if len(sgs) == 0 || client == nil {
		return nil
	}
	for _, sg := range sgs {
		got, err := client.Get(ctx, string(sg))
		if err != nil {
			return securityGroupPeerErr(err, string(sg))
		}
		if got.NetworkID != networkID {
			return status.Errorf(codes.InvalidArgument,
				"security group %s does not belong to network %s", sg, networkID)
		}
	}
	return nil
}

// securityGroupPeerErr — маппинг ошибок vpc.SecurityGroupService.Get в gRPC-status.
// Клиент оборачивает grpc-status в domain-sentinel: NotFound/InvalidArgument peer'а
// (включая PermissionDenied, который клиент маскирует под InvalidArgument, чтобы не
// лик'ать authz) — bad-input на request-time → InvalidArgument "security group <id>
// not found"; недоступность → Unavailable (fail-closed для мутации); прочее →
// Internal (без leak'а).
func securityGroupPeerErr(err error, id string) error {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrInvalidArg):
		return status.Errorf(codes.InvalidArgument, "security group %s not found", id)
	case errors.Is(err, domain.ErrUnavailable):
		return status.Errorf(codes.Unavailable, "security group lookup unavailable")
	}
	return status.Errorf(codes.Internal, "security group lookup failed")
}

// securityGroupIDsInMask reports whether the Update touches security_group_ids:
// explicit path in the mask, or an empty mask (full-object PATCH reapplies all
// mutable fields). Только в этом случае набор перевалидируется через peer —
// мутация, не трогающая SG, не должна падать на dangling-SG (data-integrity п.4).
func securityGroupIDsInMask(mask []string) bool {
	if len(mask) == 0 {
		return true
	}
	for _, p := range mask {
		if p == "security_group_ids" {
			return true
		}
	}
	return false
}
