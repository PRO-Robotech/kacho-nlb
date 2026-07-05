// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

// operationToProto — тонкий делегатор к единому `shared.OperationToProto`
// (один источник истины для всех use-case пакетов, см. audit LEAN #10).
func operationToProto(op *operations.Operation) *operationpb.Operation {
	return shared.OperationToProto(op)
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

// errInvalidArg — тонкий делегатор к единому `shared.ErrInvalidArg`
// (см. audit LEAN #11).
func errInvalidArg(field, msg string) error {
	return shared.ErrInvalidArg(field, msg)
}
