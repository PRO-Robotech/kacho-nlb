// Package fgaboot wires the corelib fail-closed boot-gate (sub-phase 1.4 D-8)
// into kacho-nlb's gRPC server: a unary interceptor that refuses mutating
// tenant-resource Create RPCs when require-iam is armed and the IAM-connected
// register-drainer is not up, so no resource is ever created without a
// deliverable owner-tuple intent. Read RPCs (and any Internal*-admin service)
// are not gated.
package fgaboot

import (
	"context"
	"strings"

	"google.golang.org/grpc"

	"github.com/PRO-Robotech/kacho-corelib/outbox/bootgate"
)

// GuardCreateUnary returns the unary interceptor that consults the boot-gate on
// tenant-resource Create RPCs (1.4-31). On a refusal it returns the gate's
// UNAVAILABLE error and never invokes the handler (the resource is not created).
func GuardCreateUnary(gate *bootgate.Gate) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if IsGatedCreate(info.FullMethod) {
			if err := gate.GuardMutation(); err != nil {
				return nil, err
			}
		}
		return handler(ctx, req)
	}
}

// IsGatedCreate reports whether the full gRPC method is a tenant-resource Create
// that records an owner-tuple intent (gated). It matches "/<pkg>.<Service>/Create"
// but excludes Internal*-admin services (detected by a ".Internal" prefix on the
// service-name segment). nlb has no Internal-admin Create today; the exclusion is
// kept for parity with vpc/compute and future-proofing.
func IsGatedCreate(fullMethod string) bool {
	if !strings.HasSuffix(fullMethod, "/Create") {
		return false
	}
	return !strings.Contains(fullMethod, ".Internal")
}
