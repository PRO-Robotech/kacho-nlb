// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// listenerRecordToPb — repo-record → proto.Listener via DTO registry
// (registered in `internal/dto/type2pb/listener.go`).
func listenerRecordToPb(rec *kachorepo.ListenerRecord) (*lbv1.Listener, error) {
	if rec == nil {
		return nil, status.Error(codes.Internal, "nil listener record")
	}
	var dst *lbv1.Listener
	if err := dto.Transfer(dto.FromTo(*rec, &dst)); err != nil {
		return nil, mapDomainErr(err)
	}
	return dst, nil
}

// operationToProto — domain `operations.Operation` → proto Operation. Mirrors
// `operation` package's helper; duplicated here to avoid cross-package leak
// (handler-only mapping; kacho-vpc-style — see networkinterface/handler.go).
func operationToProto(op *operations.Operation) *operationpb.Operation {
	if op == nil {
		return nil
	}
	p := &operationpb.Operation{
		Id:                   op.ID,
		Description:          op.Description,
		CreatedAt:            timestamppb.New(op.CreatedAt),
		CreatedBy:            op.CreatedBy,
		ModifiedAt:           timestamppb.New(op.ModifiedAt),
		Done:                 op.Done,
		Metadata:             op.Metadata,
		PrincipalType:        op.Principal.Type,
		PrincipalId:          op.Principal.ID,
		PrincipalDisplayName: op.Principal.DisplayName,
	}
	if op.Error != nil {
		p.Result = &operationpb.Operation_Error{Error: op.Error}
	} else if op.Response != nil {
		p.Result = &operationpb.Operation_Response{Response: op.Response}
	}
	return p
}

// mapDomainErr — translate domain-sentinel error → gRPC status.
//
// Mirrors `internal/repo/kacho.errors` mapping (workspace CLAUDE.md
// «Within-service refs», Error mapping kacho-vpc):
//
//	ErrNotFound            → NOT_FOUND
//	ErrAlreadyExists       → ALREADY_EXISTS
//	ErrFailedPrecondition  → FAILED_PRECONDITION
//	ErrInvalidArg          → INVALID_ARGUMENT
//	ErrUnavailable         → UNAVAILABLE
//	ErrInternal / other    → INTERNAL (no leak)
//
// Uses errors.Is so chained errors (`fmt.Errorf("%w:...", domain.ErrX)`) are
// matched. Verbatim message text from the wrapped error is preserved.
func mapDomainErr(err error) error {
	if err == nil {
		return nil
	}
	// Already a gRPC status — pass through (handler already mapped, or peer
	// adapter returned a typed status).
	if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
		return err
	}
	msg := err.Error()
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, stripSentinel(msg, domain.ErrNotFound.Error()))
	case errors.Is(err, domain.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, stripSentinel(msg, domain.ErrAlreadyExists.Error()))
	case errors.Is(err, domain.ErrFailedPrecondition):
		return status.Error(codes.FailedPrecondition, stripSentinel(msg, domain.ErrFailedPrecondition.Error()))
	case errors.Is(err, domain.ErrInvalidArg):
		return status.Error(codes.InvalidArgument, stripSentinel(msg, domain.ErrInvalidArg.Error()))
	case errors.Is(err, domain.ErrUnavailable):
		return status.Error(codes.Unavailable, stripSentinel(msg, domain.ErrUnavailable.Error()))
	case errors.Is(err, domain.ErrInternal):
		return status.Error(codes.Internal, "internal database error")
	}
	return status.Error(codes.Internal, "internal error")
}

// stripSentinel — remove `<sentinel-text>: ` prefix (or leading `<sentinel-text>`)
// from wrapped error so the caller sees с фиксированным текстом message text from the wrapping
// `fmt.Errorf("%w: <message>", domain.ErrX)` without the sentinel marker.
func stripSentinel(full, sentinel string) string {
	prefix := sentinel + ": "
	if len(full) >= len(prefix) && full[:len(prefix)] == prefix {
		return full[len(prefix):]
	}
	if full == sentinel {
		return sentinel
	}
	return full
}

// marshalListener — anypb.New(Listener) wrapper used as Operation.response on
// successful Create/Update worker completion. Returns gRPC Internal on
// marshal-failure (should be impossible — Listener is a proto message).
func marshalListener(rec *kachorepo.ListenerRecord) (*anypb.Any, error) {
	pb, err := listenerRecordToPb(rec)
	if err != nil {
		return nil, err
	}
	any, err := anypb.New(pb)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return any, nil
}

// listenerPayloadMap — outbox-payload snapshot (`map[string]any`) для
// `nlb_outbox`. Минимальный набор полей для consumer'ов (kacho-iam reader,
// metrics). Полный record не сериализуем — outbox-event это уведомление, а не
// полный ресурс (consumer делает дополнительный Get(id) если нужно).
func listenerPayloadMap(rec *kachorepo.ListenerRecord) map[string]any {
	if rec == nil {
		return nil
	}
	return map[string]any{
		"id":               string(rec.ID),
		"load_balancer_id": string(rec.LoadBalancerID),
		"project_id":       string(rec.ProjectID),
		"region_id":        string(rec.RegionID),
		"name":             string(rec.Name),
		"protocol":         string(rec.Protocol),
		"port":             int32(rec.Port),
		"status":           string(rec.Status),
	}
}

// lbUpdatedPayloadMap — outbox-payload для cross-resource sync эмита
// `nlb_load_balancer:<lb_id> UPDATED` после Listener.Create /.Delete
// . Minimal — consumer резолвит full LB через Get.
func lbUpdatedPayloadMap(lbID, projectID, regionID, trigger string) map[string]any {
	return map[string]any{
		"id":         lbID,
		"project_id": projectID,
		"region_id":  regionID,
		"trigger":    trigger,
	}
}

// loggerOrDiscard — defensive accessor для nil-loggers. Возвращает global
// default slog (через slog.Default) если переданный logger == nil; иначе
// возвращает его. Use-case helpers могут безопасно вызывать `loggerOrDiscard(u.logger).Info`.
func loggerOrDiscard(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.Default()
}
