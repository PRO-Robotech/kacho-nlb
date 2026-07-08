// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/authz"
	"github.com/PRO-Robotech/kacho-corelib/operations"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// checkTargetGroupViewer authorizes the caller (`viewer` on
// `lb_target_group:<tg>`) against a caller-supplied TargetGroup object
// (CWE-863 guard).
//
// Shared by every use-case that reads/mutates a TargetGroup referenced by a
// request field on an LB RPC (AttachTargetGroup, GetTargetStates, …): the
// per-RPC interceptor gates only the LoadBalancer object (its
// StaticExtractor resolves `network_load_balancer_id`), so without this
// explicit object-scoped Check a narrowly-scoped custom grant on the LB
// (e.g. `v_update`/`v_get` without project-editor) could read or wire in a
// TargetGroup the caller holds no authorization over — including one in a
// different project. The standard FGA cascade (project-editor ⇒ viewer on
// same-project TGs) already implies this Check for ordinary bindings, so it
// is a no-op there; it only bites narrowly-scoped custom grants and
// cross-project ids.
//
// nil checkClient or system/empty subject (breakglass/dev — the source
// object's own Check is bypassed by the interceptor for the same reason) →
// skip.
func checkTargetGroupViewer(ctx context.Context, checkClient CheckClient, tgID string) error {
	if checkClient == nil {
		return nil
	}
	p := operations.PrincipalFromContext(ctx)
	subject := domain.FGASubjectFromPrincipal(p.Type, p.ID)
	if subject == "" {
		return nil
	}
	allowed, err := checkClient.Check(ctx, subject, domain.FGARelationViewer,
		domain.FGAObjectRef(domain.FGAObjectTypeTargetGroup, tgID))
	if err != nil {
		return targetGroupCheckErr(err, tgID)
	}
	if !allowed {
		return status.Errorf(codes.PermissionDenied,
			"caller is not authorized (viewer) on target group %s", tgID)
	}
	return nil
}

// targetGroupCheckErr maps a TG-authz Check error to a gRPC status
// (fail-closed): no-path → PermissionDenied; iam unavailable → Unavailable;
// bad args → InvalidArgument; anything else → Internal.
func targetGroupCheckErr(err error, tgID string) error {
	switch {
	case errors.Is(err, authz.ErrNoPath):
		return status.Errorf(codes.PermissionDenied,
			"caller is not authorized (viewer) on target group %s", tgID)
	case errors.Is(err, domain.ErrUnavailable):
		return status.Error(codes.Unavailable, "authorization check unavailable")
	case errors.Is(err, domain.ErrInvalidArg):
		return status.Errorf(codes.InvalidArgument, "authorization check: %v", err)
	}
	return status.Error(codes.Internal, "authorization check failed")
}
