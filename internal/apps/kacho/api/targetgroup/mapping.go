// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package targetgroup

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/shared"
	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// mapDomainErr транслирует sentinel-ошибки `domain` и `kacho`(repo) в gRPC-status.
// Делегирует единому мапперу `shared.MapDomainErr` (один источник истины для всех
// use-case пакетов).
func mapDomainErr(err error) error {
	return shared.MapDomainErr(err)
}

// stripSentinel убирает sentinel-prefix `<text>: ` из ошибки. Тонкий wrapper над
// `shared.StripSentinel` (сохранён для внутренних вызовов/тестов).
func stripSentinel(err error, fallback string) string {
	return shared.StripSentinel(err, fallback)
}

// peerErrToStatus — тонкий делегатор к единому `shared.PeerErrToStatus`
// (project/region precheck + per-target peer-validate).
func peerErrToStatus(err error, kind, id string) error {
	return shared.PeerErrToStatus(err, kind, id)
}

// errInvalidArg — тонкий делегатор к единому `shared.ErrInvalidArg`.
func errInvalidArg(field, msg string) error {
	return shared.ErrInvalidArg(field, msg)
}

// operationToProto — тонкий делегатор к единому `shared.OperationToProto`.
func operationToProto(op *operations.Operation) *operationpb.Operation {
	return shared.OperationToProto(op)
}

// tgRecordToProto — repo-record → proto.TargetGroup через зарегистрированный DTO
// transfer (`internal/dto/type2pb/target_group.go`).
func tgRecordToProto(rec *kachorepo.TargetGroupRecord) (*lbv1.TargetGroup, error) {
	if rec == nil {
		return nil, status.Error(codes.Internal, "nil target_group record")
	}
	var dst *lbv1.TargetGroup
	if err := dto.Transfer(dto.FromTo(*rec, &dst)); err != nil {
		return nil, mapDomainErr(err)
	}
	return dst, nil
}

// errsIs — internal alias на `errors.Is` (хелпер для удобства внутри пакета,
// чтобы избежать дополнительного import-блока в add_targets.go).
func errsIs(err, target error) bool {
	return errors.Is(err, target)
}
