package iam

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	iampb "github.com/PRO-Robotech/kacho-iam/proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// Project — projection ресурса kacho-iam.Project, ограниченная полями
// необходимыми consumer'ам (NLB не оперирует labels/description).
type Project struct {
	ID        string
	Name      string
	AccountID string
	// Status — текущий жизненный статус Project. kacho-iam proto не несёт
	// явного status-поля для Project (всегда "ACTIVE" если возвращается
	// успешно из Get; иначе NotFound). Поле оставлено для будущей
	// proto-эволюции (KAC-iam status-tracking) — сейчас всегда "ACTIVE".
	Status string
}

// ProjectClient — port-интерфейс для service-слоя; реализуется adapter'ом
// ниже (`projectClient`) и mock-структурой в тестах use-case'ов.
type ProjectClient interface {
	// Get возвращает Project metadata. Семантика ошибок:
	//   - kacho-iam NotFound          → domain.ErrNotFound
	//   - PermissionDenied            → domain.ErrFailedPrecondition (мапится
	//     в "Project ... not found"; tenant не должен видеть разницу между
	//     "не существует" и "нет доступа" — leak'ы про authz tenant'у запрещены).
	//   - Unavailable/DeadlineExceeded → domain.ErrUnavailable
	//   - InvalidArgument             → domain.ErrInvalidArg
	//   - Любая другая ошибка         → wrapped error без sentinel-обёртки
	//     (service-слой пометит operation INTERNAL).
	Get(ctx context.Context, projectID string) (*Project, error)
}

// projectClient — реализация ProjectClient через gRPC.
type projectClient struct {
	cli iampb.ProjectServiceClient
}

// NewProjectClient оборачивает grpc-conn в typed adapter.
// conn — обычно `clients.Build(...)` из builder.go (corlib ClientConn, реализует
// grpc.ClientConnInterface). Для unit-тестов — bufconn-style `*grpc.ClientConn`.
func NewProjectClient(conn grpc.ClientConnInterface) ProjectClient {
	if conn == nil {
		return nil
	}
	return &projectClient{cli: iampb.NewProjectServiceClient(conn)}
}

// NewProjectClientFromStub — конструктор для тестов: принимает напрямую
// `iampb.ProjectServiceClient` (in-memory fake / mockgen-generated stub).
func NewProjectClientFromStub(cli iampb.ProjectServiceClient) ProjectClient {
	if cli == nil {
		return nil
	}
	return &projectClient{cli: cli}
}

// Get — см. контракт в ProjectClient.Get.
func (c *projectClient) Get(ctx context.Context, projectID string) (*Project, error) {
	if projectID == "" {
		return nil, fmt.Errorf("%w: project_id is empty", domain.ErrInvalidArg)
	}

	// KAC-178 §2: propagate Principal в outgoing MD, чтобы iam-side ProjectService.Get
	// passes its per-RPC authz Check (viewer@project) для реального user'а, а не
	// SystemPrincipal()=user:bootstrap fallback'а.
	ctx = auth.PropagateOutgoing(ctx)

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
		ID:        resp.GetId(),
		Name:      resp.GetName(),
		AccountID: resp.GetAccountId(),
		Status:    "ACTIVE",
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
		// и для denied (см. workspace CLAUDE.md §«Инфра-чувствительные данные»).
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
