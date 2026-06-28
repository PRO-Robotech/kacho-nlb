// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	operationpb "github.com/PRO-Robotech/kacho-corelib/proto/gen/go/kacho/cloud/operation"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// mapDomainErr транслирует sentinel-ошибки `domain`/`kacho` (repo) и peer-client
// ошибки в gRPC-status. Stripping sentinel prefix производится через
// `stripSentinel` так, чтобы текст ошибки доезжал до клиента в чистом
// виде (без префиксов вроде "not found: NetworkLoadBalancer xxx not found").
//
// Если err уже является gRPC-status (например, sync-валидация из corelib/errors)
// пробрасываем как есть.
func mapDomainErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		// already gRPC-shaped
		return err
	}
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, kachorepo.ErrNotFound):
		return status.Error(codes.NotFound, stripSentinel(err, "not found"))
	case errors.Is(err, domain.ErrAlreadyExists), errors.Is(err, kachorepo.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, stripSentinel(err, "already exists"))
	case errors.Is(err, domain.ErrFailedPrecondition), errors.Is(err, kachorepo.ErrFailedPrecondition):
		return status.Error(codes.FailedPrecondition, stripSentinel(err, "failed precondition"))
	case errors.Is(err, domain.ErrInvalidArg), errors.Is(err, kachorepo.ErrInvalidArg):
		return status.Error(codes.InvalidArgument, stripSentinel(err, "invalid argument"))
	case errors.Is(err, domain.ErrUnavailable), errors.Is(err, kachorepo.ErrUnavailable):
		return status.Error(codes.Unavailable, stripSentinel(err, "service unavailable"))
	case errors.Is(err, domain.ErrInternal), errors.Is(err, kachorepo.ErrInternal):
		// Internal: НЕ leak'аем raw pgx text — отдаём константную фразу.
		return status.Error(codes.Internal, "internal database error")
	}
	// Default: преобразуем в Internal без leak'а текста.
	return status.Error(codes.Internal, "internal error")
}

// stripSentinel убирает sentinel-prefix "<text>: " из строки ошибки, чтобы
// чистый по конвенции Kachō текст доходил до клиента. Если префикса нет —
// возвращает err.Error как есть. Защищает от пустого результата (fallback
// на fallbackText).
func stripSentinel(err error, fallbackText string) string {
	if err == nil {
		return fallbackText
	}
	msg := err.Error()
	prefixes := []string{
		"not found: ", "already exists: ", "failed precondition: ",
		"invalid argument: ", "internal database error: ", "service unavailable: ",
	}
	for _, p := range prefixes {
		if len(msg) > len(p) && msg[:len(p)] == p {
			return msg[len(p):]
		}
	}
	if msg == "" {
		return fallbackText
	}
	return msg
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
