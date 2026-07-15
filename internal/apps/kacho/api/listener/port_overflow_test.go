// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	errdetails "google.golang.org/genproto/googleapis/rpc/errdetails"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// fieldViolationsText — собирает FieldViolation.Description из gRPC-status details
// (corelib InvalidArgument.AddFieldViolation) в одну строку для assert.Contains
// (field-текст лежит в BadRequest details, не в status.Message).
func fieldViolationsText(err error) string {
	if err == nil {
		return ""
	}
	st, ok := grpcstatus.FromError(err)
	if !ok {
		return err.Error()
	}
	parts := []string{st.Message()}
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok {
			for _, v := range br.GetFieldViolations() {
				parts = append(parts, v.GetField()+": "+v.GetDescription())
			}
		}
	}
	return strings.Join(parts, " | ")
}

// overflowTo — int64, который при узком int32-приведении даёт in-range остаток
// low (2^32 + low). Голое int32(...) молча усечёт high-биты и алиасит на low,
// проскакивая LbPort.Validate — именно это ловит guard (gosec G115 / CWE-190).
func overflowTo(low int64) int64 { return int64(1)<<32 + low }

// TestCreateListener_PortOverflowRejected — proto Port это int64; значение,
// переполняющее int32 (2^32+443), голым приведением усеклось бы до валидного
// порта 443 и обошло бы LbPort.Validate. Guard обязан отвергнуть его
// InvalidArgument ДО сужения. Verifies code-scanning gosec G115 alerts #29/#30.
func TestCreateListener_PortOverflowRejected(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lb := seedParentLB(t, repo)
	uc := newCreateUC(repo, newFakeOpsRepo())

	_, err := uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(lb.ID), Name: "ovf-port",
		Protocol: lbv1.Listener_TCP, Port: overflowTo(443), TargetPort: 8080,
	})
	require.Equal(t, codes.InvalidArgument, grpcstatus.Code(err))
	require.Contains(t, fieldViolationsText(err), "port must be in range [1, 65535]")
}

// TestCreateListener_TargetPortOverflowRejected — тот же guard на target_port.
func TestCreateListener_TargetPortOverflowRejected(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lb := seedParentLB(t, repo)
	uc := newCreateUC(repo, newFakeOpsRepo())

	_, err := uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(lb.ID), Name: "ovf-tport",
		Protocol: lbv1.Listener_TCP, Port: 443, TargetPort: overflowTo(8080),
	})
	require.Equal(t, codes.InvalidArgument, grpcstatus.Code(err))
	require.Contains(t, fieldViolationsText(err), "port must be in range [1, 65535]")
}
