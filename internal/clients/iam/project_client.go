// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package iam

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	iampb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// DefaultProjectGetTimeout — per-call deadline применяемый к
// ProjectService.Get, когда client построен без явного timeout'а. Тот же
// класс проблемы, что DefaultCheckTimeout (check_client.go, тот же пакет):
// без него зависший (не отвечающий, не Unavailable) iam-peer парковал бы
// вызывающую горутину навсегда (round-6 audit sweep).
const DefaultProjectGetTimeout = 5 * time.Second

// Project — projection ресурса kacho-iam.Project, ограниченная полями
// необходимыми consumer'ам. NLB зовёт ProjectClient.Get только как
// existence-precheck (все callers отбрасывают результат в `_`), поэтому
// проекция несёт лишь ID/Name — по образцу sibling geo.Region.
type Project struct {
	ID   string
	Name string
}

// ProjectClient — port-интерфейс для service-слоя; реализуется adapter'ом
// ниже (`projectClient`) и mock-структурой в тестах use-case'ов.
type ProjectClient interface {
	// Get возвращает Project metadata. Семантика ошибок:
	//   - kacho-iam NotFound          → domain.ErrNotFound
	//   - PermissionDenied            → domain.ErrFailedPrecondition (мапится
	//     в "Project... not found"; tenant не должен видеть разницу между
	//     "не существует" и "нет доступа" — leak'ы про authz tenant'у запрещены).
	//   - Unavailable/DeadlineExceeded → domain.ErrUnavailable
	//   - InvalidArgument             → domain.ErrInvalidArg
	//   - Любая другая ошибка         → wrapped error без sentinel-обёртки
	//     (service-слой пометит operation INTERNAL).
	Get(ctx context.Context, projectID string) (*Project, error)
}

// projectClient — реализация ProjectClient через gRPC.
type projectClient struct {
	cli     iampb.ProjectServiceClient
	timeout time.Duration
}

// NewProjectClient оборачивает grpc-conn в typed adapter.
// conn — обычно `clients.Build` из builder.go (corlib ClientConn, реализует
// grpc.ClientConnInterface). Для unit-тестов — bufconn-style `*grpc.ClientConn`.
// Per-call timeout — DefaultProjectGetTimeout.
func NewProjectClient(conn grpc.ClientConnInterface) ProjectClient {
	return NewProjectClientWithTimeout(conn, DefaultProjectGetTimeout)
}

// NewProjectClientWithTimeout — как NewProjectClient, но с явным per-call
// timeout'ом. timeout<=0 → DefaultProjectGetTimeout.
func NewProjectClientWithTimeout(conn grpc.ClientConnInterface, timeout time.Duration) ProjectClient {
	if conn == nil {
		return nil
	}
	return &projectClient{cli: iampb.NewProjectServiceClient(conn), timeout: resolveProjectTimeout(timeout)}
}

// NewProjectClientFromStub — конструктор для тестов: принимает напрямую
// `iampb.ProjectServiceClient` (in-memory fake / mockgen-generated stub).
func NewProjectClientFromStub(cli iampb.ProjectServiceClient) ProjectClient {
	return NewProjectClientFromStubWithTimeout(cli, DefaultProjectGetTimeout)
}

// NewProjectClientFromStubWithTimeout — как NewProjectClientFromStub, но с
// явным per-call timeout'ом (используется тестами concurrency/timeout-фиксов).
func NewProjectClientFromStubWithTimeout(cli iampb.ProjectServiceClient, timeout time.Duration) ProjectClient {
	if cli == nil {
		return nil
	}
	return &projectClient{cli: cli, timeout: resolveProjectTimeout(timeout)}
}

func resolveProjectTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultProjectGetTimeout
	}
	return d
}

// Get — см. контракт в ProjectClient.Get.
func (c *projectClient) Get(ctx context.Context, projectID string) (*Project, error) {
	if projectID == "" {
		return nil, fmt.Errorf("%w: project_id is empty", domain.ErrInvalidArg)
	}

	// propagate Principal в outgoing MD, чтобы iam-side ProjectService.Get
	// passes its per-RPC authz Check (viewer@project) для реального user'а, а не
	// SystemPrincipal=user:bootstrap fallback'а.
	ctx = auth.PropagateOutgoing(ctx)

	// Per-call deadline — bounds the ENTIRE retry.OnUnavailable operation,
	// independent of the caller's own ctx (architecture.md "Per-call deadline
	// на КАЖДОМ внешнем вызове"; see check_client.go DefaultCheckTimeout for
	// the sibling rationale).
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var resp *iampb.Project
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		resp, rerr = c.cli.Get(ctx, &iampb.GetProjectRequest{ProjectId: projectID})
		return rerr
	})
	if err != nil {
		return nil, mapProjectErr(projectID, err)
	}
	return &Project{
		ID:   resp.GetId(),
		Name: resp.GetName(),
	}, nil
}

// mapProjectErr транслирует gRPC-status в domain-sentinel ошибки. См. контракт
// ProjectClient.Get.
func mapProjectErr(projectID string, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("iam project get %q: %w", projectID, err)
	}
	switch st.Code() {
	case codes.NotFound:
		return fmt.Errorf("%w: Project %s not found", domain.ErrNotFound, projectID)
	case codes.PermissionDenied:
		// Не лик'аем разницу: tenant видит "не существует" и для NotFound,
		// и для denied (existence-hiding: не раскрываем инфра-чувствительные данные).
		// Используется FailedPrecondition (а не NotFound) чтобы handler-слой
		// сервиса различал internal-precondition от honest-NotFound резолва.
		return fmt.Errorf("%w: Project %s not found", domain.ErrFailedPrecondition, projectID)
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: iam project %s: %s", domain.ErrUnavailable, projectID, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: iam project %s: %s", domain.ErrInvalidArg, projectID, st.Message())
	default:
		return fmt.Errorf("iam project get %q: %w", projectID, err)
	}
}
