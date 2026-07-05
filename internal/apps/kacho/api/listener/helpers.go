// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/shared"
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

// operationToProto — тонкий делегатор к единому `shared.OperationToProto`
// (один источник истины для всех use-case пакетов, см. audit LEAN #10).
func operationToProto(op *operations.Operation) *operationpb.Operation {
	return shared.OperationToProto(op)
}

// mapDomainErr — translate domain-sentinel error → gRPC status. Делегирует
// единому мапперу `shared.MapDomainErr` (один источник истины для всех use-case
// пакетов kacho-nlb, см. audit ARCH-medium):
//
//	ErrNotFound            → NOT_FOUND
//	ErrAlreadyExists       → ALREADY_EXISTS
//	ErrFailedPrecondition  → FAILED_PRECONDITION
//	ErrInvalidArg          → INVALID_ARGUMENT
//	ErrUnavailable         → UNAVAILABLE
//	ErrInternal / other    → INTERNAL (no leak)
func mapDomainErr(err error) error {
	return shared.MapDomainErr(err)
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
	// ParentResourceID = parent LB id (canonical `parent_resource_id` key) — the
	// Subscribe consumer reads it into ResourceLifecycleEvent.ParentResourceId for
	// kacho-iam FGA-sync (listener→LB hierarchy). Single source of truth for the
	// key names is kachorepo.LifecyclePayload.
	return kachorepo.LifecyclePayload{
		ID:               string(rec.ID),
		ParentResourceID: string(rec.LoadBalancerID),
		ProjectID:        string(rec.ProjectID),
		RegionID:         string(rec.RegionID),
		Name:             string(rec.Name),
		Protocol:         string(rec.Protocol),
		Port:             int32(rec.Port),
		Status:           string(rec.Status),
	}.Map()
}

// lbUpdatedPayloadMap — outbox-payload для cross-resource sync эмита
// `nlb_load_balancer:<lb_id> UPDATED` после Listener.Create /.Delete
// . Minimal — consumer резолвит full LB через Get.
func lbUpdatedPayloadMap(lbID, projectID, regionID, trigger string) map[string]any {
	return kachorepo.LifecyclePayload{
		ID:        lbID,
		ProjectID: projectID,
		RegionID:  regionID,
		Trigger:   trigger,
	}.Map()
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
