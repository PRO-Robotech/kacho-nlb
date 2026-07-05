// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package targetgroup

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// mapDomainErr транслирует sentinel-ошибки `domain` и `kacho`(repo) в gRPC-status.
// Если err уже gRPC-status — пробрасываем как есть (sync corelib/errors).
//
// Mirror'ит соглашение loadbalancer.mapDomainErr (стабильная форма
// ошибки доходит до клиента; raw pgx-текст НЕ leak'аем).
func mapDomainErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
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
		return status.Error(codes.Internal, "internal database error")
	}
	return status.Error(codes.Internal, "internal error")
}

// stripSentinel убирает sentinel-prefix `<text>: ` из ошибки. Подбирает все
// варианты из обоих наборов sentinel'ов (domain + kacho repo).
func stripSentinel(err error, fallback string) string {
	if err == nil {
		return fallback
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
		return fallback
	}
	return msg
}

// peerErrToStatus — peer-client error (sentinel-wrapped) → gRPC-status.
// Используется при sync project/region precheck и в worker per-target peer-validate.
func peerErrToStatus(err error, kind, id string) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return status.Errorf(codes.InvalidArgument, "%s %s not found", caser(kind), id)
	case errors.Is(err, domain.ErrInvalidArg):
		return status.Errorf(codes.InvalidArgument, "%s: %v", kind, err)
	case errors.Is(err, domain.ErrFailedPrecondition):
		return status.Errorf(codes.FailedPrecondition, "%s %s: %v", kind, id, err)
	case errors.Is(err, domain.ErrUnavailable):
		return status.Errorf(codes.Unavailable, "%s lookup unavailable", kind)
	}
	return status.Errorf(codes.Internal, "%s lookup failed", kind)
}

// caser — Title-case 1-char для kind.
func caser(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 32
	}
	return string(b)
}

// errInvalidArg формирует InvalidArgument с указанием поля + ошибки.
func errInvalidArg(field, msg string) error {
	return status.Errorf(codes.InvalidArgument, "%s: %s", field, msg)
}

// operationToProto — domain `operations.Operation` → proto Operation (с principal
// полями). Зеркалит соглашение loadbalancer.operationToProto.
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
