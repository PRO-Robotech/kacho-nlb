// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package compute

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	computepb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// DefaultInstanceGetTimeout — per-call deadline применяемый к
// InstanceService.Get, когда client построен без явного timeout'а. Тот же
// класс проблемы, что DefaultCheckTimeout (iam) / DefaultRegionGetTimeout
// (geo): без него зависший (не отвечающий, не Unavailable) compute-peer
// парковал бы вызывающую горутину навсегда.
const DefaultInstanceGetTimeout = 5 * time.Second

// Instance — projection ресурса kacho-compute.Instance, ограниченная полями
// необходимыми consumer'ам NLB (Target.instance_id resolve в TG.AddTargets).
type Instance struct {
	ID                string
	ProjectID         string
	ZoneID            string
	Name              string
	PrimaryNICAddress string // primary NIC v4 address (внутренний)
	Status            string // string-форма Instance_Status enum
}

// InstanceClient — port-интерфейс для service-слоя.
type InstanceClient interface {
	// Get возвращает Instance + extracted PrimaryNICAddress (первый NIC,
	// primary_v4_address.address). Семантика ошибок:
	//   - kacho-compute NotFound       → domain.ErrInvalidArg "Instance <id> not found"
	//     (на TG.AddTargets bad input — instance не существует).
	//   - PermissionDenied             → domain.ErrInvalidArg (не лик'аем authz).
	//   - FailedPrecondition           → domain.ErrFailedPrecondition (instance
	//     в DELETING/ERROR — нельзя attach).
	//   - Unavailable/DeadlineExceeded → domain.ErrUnavailable
	//   - InvalidArgument              → domain.ErrInvalidArg
	//   - Любая другая ошибка          → wrapped error без sentinel-обёртки.
	Get(ctx context.Context, instanceID string) (*Instance, error)
}

// instanceClient — реализация InstanceClient через gRPC.
type instanceClient struct {
	cli     computepb.InstanceServiceClient
	timeout time.Duration
}

// NewInstanceClient оборачивает grpc-conn в typed adapter. conn —
// `clients.Build`. InstanceService живёт на public-listener
// kacho-compute (`:9090`). Per-call timeout — DefaultInstanceGetTimeout.
func NewInstanceClient(conn grpc.ClientConnInterface) InstanceClient {
	return NewInstanceClientWithTimeout(conn, DefaultInstanceGetTimeout)
}

// NewInstanceClientWithTimeout — как NewInstanceClient, но с явным per-call
// timeout'ом. timeout<=0 → DefaultInstanceGetTimeout.
func NewInstanceClientWithTimeout(conn grpc.ClientConnInterface, timeout time.Duration) InstanceClient {
	if conn == nil {
		return nil
	}
	return &instanceClient{cli: computepb.NewInstanceServiceClient(conn), timeout: resolveInstanceTimeout(timeout)}
}

// NewInstanceClientFromStub — конструктор для тестов: принимает stub.
func NewInstanceClientFromStub(cli computepb.InstanceServiceClient) InstanceClient {
	return NewInstanceClientFromStubWithTimeout(cli, DefaultInstanceGetTimeout)
}

// NewInstanceClientFromStubWithTimeout — как NewInstanceClientFromStub, но с
// явным per-call timeout'ом (используется тестами concurrency/timeout-фиксов).
func NewInstanceClientFromStubWithTimeout(cli computepb.InstanceServiceClient, timeout time.Duration) InstanceClient {
	if cli == nil {
		return nil
	}
	return &instanceClient{cli: cli, timeout: resolveInstanceTimeout(timeout)}
}

func resolveInstanceTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultInstanceGetTimeout
	}
	return d
}

// Get — см. контракт InstanceClient.Get.
func (c *instanceClient) Get(ctx context.Context, instanceID string) (*Instance, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("%w: instance_id is empty", domain.ErrInvalidArg)
	}

	ctx = auth.PropagateOutgoing(ctx)

	// Per-call deadline — bounds the ENTIRE retry.OnUnavailable operation,
	// independent of the caller's own ctx (architecture.md "Per-call deadline
	// на КАЖДОМ внешнем вызове"; see check_client.go DefaultCheckTimeout for
	// the sibling rationale).
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var resp *computepb.Instance
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		resp, rerr = c.cli.Get(ctx, &computepb.GetInstanceRequest{InstanceId: instanceID})
		return rerr
	}); err != nil {
		return nil, mapInstanceErr(instanceID, err)
	}

	return &Instance{
		ID:                resp.GetId(),
		ProjectID:         resp.GetProjectId(),
		ZoneID:            resp.GetZoneId(),
		Name:              resp.GetName(),
		PrimaryNICAddress: primaryNICAddress(resp),
		Status:            resp.GetStatus().String(),
	}, nil
}

// primaryNICAddress извлекает первый primary_v4_address.address из NIC[0].
// Пустая строка — если нет NIC или нет primary_v4_address (instance в
// PROVISIONING / ошибочно созданный). Caller проверяет на пустоту.
func primaryNICAddress(inst *computepb.Instance) string {
	nics := inst.GetNetworkInterfaces()
	if len(nics) == 0 {
		return ""
	}
	return nics[0].GetPrimaryV4Address().GetAddress()
}

// mapInstanceErr транслирует gRPC-status в domain-sentinel-ошибки.
func mapInstanceErr(instanceID string, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("compute instance get %q: %w", instanceID, err)
	}
	switch st.Code() {
	case codes.NotFound:
		return fmt.Errorf("%w: Instance %s not found", domain.ErrInvalidArg, instanceID)
	case codes.PermissionDenied:
		return fmt.Errorf("%w: Instance %s not found", domain.ErrInvalidArg, instanceID)
	case codes.FailedPrecondition:
		return fmt.Errorf("%w: compute instance %s: %s", domain.ErrFailedPrecondition, instanceID, st.Message())
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: compute instance %s: %s", domain.ErrUnavailable, instanceID, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: compute instance %s: %s", domain.ErrInvalidArg, instanceID, st.Message())
	default:
		return fmt.Errorf("compute instance get %q: %w", instanceID, err)
	}
}
