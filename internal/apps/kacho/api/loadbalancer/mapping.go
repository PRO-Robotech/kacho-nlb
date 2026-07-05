// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/shared"
	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// mapDomainErr транслирует sentinel-ошибки `domain`/`kacho` (repo) и peer-client
// ошибки в gRPC-status. Делегирует единому мапперу `shared.MapDomainErr` (один
// источник истины для всех use-case пакетов, см. audit ARCH-medium).
func mapDomainErr(err error) error {
	return shared.MapDomainErr(err)
}

// stripSentinel убирает sentinel-prefix "<text>: " из строки ошибки. Тонкий
// wrapper над `shared.StripSentinel` (сохранён для внутренних вызовов/тестов).
func stripSentinel(err error, fallbackText string) string {
	return shared.StripSentinel(err, fallbackText)
}

// operationToProto конвертирует domain `operations.Operation` в proto Operation.
// Включает principal_* поля.
func operationToProto(op *operations.Operation) *operationpb.Operation {
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

// lbRecordToProto — repo-entity LoadBalancerRecord → proto NetworkLoadBalancer
// через зарегистрированный DTO transfer (`internal/dto/type2pb/loadbalancer.go`).
func lbRecordToProto(rec *kachorepo.LoadBalancerRecord) (*lbv1.NetworkLoadBalancer, error) {
	if rec == nil {
		return nil, status.Error(codes.Internal, "nil load_balancer record")
	}
	var dst *lbv1.NetworkLoadBalancer
	if err := dto.Transfer(dto.FromTo(*rec, &dst)); err != nil {
		return nil, mapDomainErr(err)
	}
	return dst, nil
}

// errInvalidArg формирует InvalidArgument с указанием поля + ошибки.
// Используется handler'ами для sync-проверок required-полей до use-case'а.
func errInvalidArg(field, msg string) error {
	return status.Errorf(codes.InvalidArgument, "%s: %s", field, msg)
}
