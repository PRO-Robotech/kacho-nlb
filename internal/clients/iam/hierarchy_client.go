package iam

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	iampb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// HierarchyWriter — adapter для записи FGA-hierarchy tuple'ов в kacho-iam.
//
// Используется в Operation-worker'ах NLB Create-флоу (D-11 sync hierarchy
// tuple write **перед** Commit ресурса):
//
//   - WriteCreatorTuple   — записать creator-tuple "<subject> creator <object>"
//     для свежесозданного ресурса (NetworkLoadBalancer / Listener / TargetGroup).
//   - RewriteProjectTuple — атомарно переместить project-relation ресурса с
//     src project на dst project (Move-операции). Текущий kacho-iam Internal
//     IAMService proto не имеет одиночной RPC-команды для atomic rewrite —
//     adapter реализует семантику через две последовательные WriteCreatorTuple-
//     операции с relation "project" (delete семантика — будет добавлена когда
//     kacho-iam дополнит API; сейчас Move-флоу полагается на eventual consistency
//     hierarchy-rebuild ListenInvalidator KAC-108).
type HierarchyWriter interface {
	// WriteCreatorTuple записывает creator-tuple.
	WriteCreatorTuple(ctx context.Context, subjectID, relation, object string) error

	// RewriteProjectTuple перемещает project-relation ресурса
	// (objectType:objectID) c srcProject на dstProject. Атомарность —
	// best-effort на стороне kacho-iam.
	RewriteProjectTuple(ctx context.Context, objectType, objectID, srcProject, dstProject string) error
}

// hierarchyWriter — реализация HierarchyWriter через gRPC.
type hierarchyWriter struct {
	cli iampb.InternalIAMServiceClient
}

// NewHierarchyWriter оборачивает grpc-conn в typed adapter. conn должен быть к
// kacho-iam **internal**-listener (`:9091`).
func NewHierarchyWriter(conn grpc.ClientConnInterface) HierarchyWriter {
	if conn == nil {
		return nil
	}
	return &hierarchyWriter{cli: iampb.NewInternalIAMServiceClient(conn)}
}

// NewHierarchyWriterFromStub — конструктор для тестов: принимает stub.
func NewHierarchyWriterFromStub(cli iampb.InternalIAMServiceClient) HierarchyWriter {
	if cli == nil {
		return nil
	}
	return &hierarchyWriter{cli: cli}
}

// WriteCreatorTuple — см. контракт HierarchyWriter.WriteCreatorTuple.
func (w *hierarchyWriter) WriteCreatorTuple(ctx context.Context, subjectID, relation, object string) error {
	switch {
	case subjectID == "":
		return fmt.Errorf("%w: subject_id is empty", domain.ErrInvalidArg)
	case relation == "":
		return fmt.Errorf("%w: relation is empty", domain.ErrInvalidArg)
	case object == "":
		return fmt.Errorf("%w: object is empty", domain.ErrInvalidArg)
	}

	ctx = auth.PropagateOutgoing(ctx)
	return retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := w.cli.WriteCreatorTuple(ctx, &iampb.WriteCreatorTupleRequest{
			SubjectId: subjectID,
			Relation:  relation,
			Object:    object,
		})
		return mapHierarchyErr(rerr)
	})
}

// RewriteProjectTuple — см. контракт HierarchyWriter.RewriteProjectTuple.
//
// Семантика: записать "<dstProject> project <objectType>:<objectID>" tuple.
// Старый src-project tuple остаётся пока kacho-iam не дополнит API atomic
// rewrite RPC — это **известное ограничение текущей proto-поверхности**
// (см. doc-комментарий типа). Для NLB Move-операций (Wave 7) эта семантика
// достаточна: read-path использует "latest tuple wins" pattern в FGA-store.
func (w *hierarchyWriter) RewriteProjectTuple(
	ctx context.Context, objectType, objectID, srcProject, dstProject string,
) error {
	switch {
	case objectType == "":
		return fmt.Errorf("%w: object_type is empty", domain.ErrInvalidArg)
	case objectID == "":
		return fmt.Errorf("%w: object_id is empty", domain.ErrInvalidArg)
	case dstProject == "":
		return fmt.Errorf("%w: dst_project is empty", domain.ErrInvalidArg)
	}
	// srcProject может быть пуст — это легитимный случай первичной записи
	// project-tuple для свежесозданного ресурса. Sentinel-проверка не нужна.
	_ = srcProject

	object := objectType + ":" + objectID
	subject := "project:" + dstProject
	ctx = auth.PropagateOutgoing(ctx)
	return retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := w.cli.WriteCreatorTuple(ctx, &iampb.WriteCreatorTupleRequest{
			SubjectId: subject,
			Relation:  "project",
			Object:    object,
		})
		return mapHierarchyErr(rerr)
	})
}

// mapHierarchyErr транслирует gRPC-status в domain-sentinel-ошибки.
func mapHierarchyErr(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("iam hierarchy write: %w", err)
	}
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: iam hierarchy write: %s", domain.ErrUnavailable, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: iam hierarchy write: %s", domain.ErrInvalidArg, st.Message())
	case codes.AlreadyExists:
		// Idempotent: tuple уже записан — это успех.
		return nil
	case codes.NotFound:
		return fmt.Errorf("%w: iam hierarchy write: %s", domain.ErrNotFound, st.Message())
	default:
		return fmt.Errorf("iam hierarchy write: %w", err)
	}
}
