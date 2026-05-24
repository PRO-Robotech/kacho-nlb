package iam

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/authz"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	iampb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// CheckClient — per-RPC FGA authorization gate; обёртка над
// `kacho.iam.v1.InternalIAMService.Check`. Используется Wave 8 authz-interceptor
// (`internal/check`). Подключение типа KAC-133 passthrough — sentinel
// `authz.ErrNoPath` пробрасывается наружу (interceptor решает DecisionNoPath →
// пропустить вызов в handler, который вернёт NOT_FOUND из DB).
type CheckClient interface {
	// Check возвращает (allowed, error). Семантика:
	//   - allowed=true,  err=nil      — пользователь имеет доступ.
	//   - allowed=false, err=nil      — пользователь не имеет доступа (deny).
	//   - allowed=false, err=authz.ErrNoPath — нет hierarchy-tuple для object'а
	//     (ресурс скорее всего не существует; interceptor → DecisionNoPath →
	//     handler вернёт NOT_FOUND из DB).
	//   - allowed=false, err=domain.ErrUnavailable — FGA / kacho-iam недоступен
	//     (fail-closed: interceptor → DecisionUnavailable → PermissionDenied).
	//   - allowed=false, err=domain.ErrInvalidArg — bad subject/relation/object.
	Check(ctx context.Context, subjectID, relation, object string) (bool, error)
}

// checkClient — реализация CheckClient через gRPC.
type checkClient struct {
	cli iampb.InternalIAMServiceClient
}

// NewCheckClient оборачивает grpc-conn в typed adapter. conn должен быть к
// kacho-iam **internal**-listener (`:9091`) — InternalIAMService.Check не
// публикуется на external endpoint (см. workspace CLAUDE.md «Запреты» #6).
func NewCheckClient(conn grpc.ClientConnInterface) CheckClient {
	if conn == nil {
		return nil
	}
	return &checkClient{cli: iampb.NewInternalIAMServiceClient(conn)}
}

// NewCheckClientFromStub — конструктор для тестов: принимает напрямую stub.
func NewCheckClientFromStub(cli iampb.InternalIAMServiceClient) CheckClient {
	if cli == nil {
		return nil
	}
	return &checkClient{cli: cli}
}

// Check — см. контракт CheckClient.Check.
func (c *checkClient) Check(ctx context.Context, subjectID, relation, object string) (bool, error) {
	switch {
	case subjectID == "":
		return false, fmt.Errorf("%w: subject_id is empty", domain.ErrInvalidArg)
	case relation == "":
		return false, fmt.Errorf("%w: relation is empty", domain.ErrInvalidArg)
	case object == "":
		return false, fmt.Errorf("%w: object is empty", domain.ErrInvalidArg)
	}

	// KAC-178 §1 follow-up (W1.4): outgoing ctx wrap with auth.PropagateOutgoing
	// so iam-side UnaryPrincipalExtract sees real caller (not SystemPrincipal()).
	ctx = auth.PropagateOutgoing(ctx)

	var resp *iampb.CheckResponse
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		resp, rerr = c.cli.Check(ctx, &iampb.CheckRequest{
			SubjectId: subjectID,
			Relation:  relation,
			Object:    object,
		})
		return rerr
	})
	if err != nil {
		return false, mapCheckErr(err)
	}
	if resp.GetAllowed() {
		return true, nil
	}
	// Passthrough `no-path` reason (KAC-133): interceptor → DecisionNoPath →
	// handler вернёт NOT_FOUND из DB. Reason-формат — substring "no path"
	// (kacho-iam emits "no FGA path: <object>") либо явный prefix.
	if isNoPathReason(resp.GetReason()) {
		return false, authz.ErrNoPath
	}
	return false, nil
}

// isNoPathReason — повторяет detection-логику kacho-corelib/authz: сравнивает
// `CheckResponse.reason` с известными "no path"-маркерами FGA.
func isNoPathReason(reason string) bool {
	r := strings.ToLower(reason)
	return strings.Contains(r, "no path") || strings.Contains(r, "no_path")
}

// mapCheckErr транслирует gRPC-status в domain-sentinel-ошибки. См. контракт
// CheckClient.Check.
func mapCheckErr(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("iam check: %w", err)
	}
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: iam check: %s", domain.ErrUnavailable, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: iam check: %s", domain.ErrInvalidArg, st.Message())
	default:
		return fmt.Errorf("iam check: %w", err)
	}
}
