// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package shared

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// MapDomainErr — единый sentinel-error→gRPC-status маппер для всех use-case
// пакетов kacho-nlb (loadbalancer / listener / targetgroup). Раньше был
// продублирован в трёх местах и успел разойтись (pass-through
// guard без codes.Unknown-проверки в loadbalancer, две несовместимые сигнатуры
// stripSentinel). Здесь — один источник истины.
//
// Транслирует sentinel-ошибки `domain` (kacho-repo re-export'ит их через
// live-alias — errors.Is даёт одинаковый результат) и peer-client ошибки в
// gRPC-status. Sentinel-prefix убирается через StripSentinel, чтобы чистый по
// конвенции Kachō текст доходил до клиента.
//
// Если err уже gRPC-status с известным кодом (code != Unknown) — пробрасываем
// как есть (sync corelib/errors, typed peer-status). status с codes.Unknown НЕ
// пробрасываем — он падает в sentinel-switch и превращается в Internal без
// leak'а (иначе один и тот же error давал бы разный код в разных ресурсах).
//
//	ErrNotFound            → NOT_FOUND
//	ErrAlreadyExists       → ALREADY_EXISTS
//	ErrFailedPrecondition  → FAILED_PRECONDITION
//	ErrInvalidArg          → INVALID_ARGUMENT
//	ErrUnavailable         → UNAVAILABLE
//	ErrInternal / прочее   → INTERNAL (no leak)
func MapDomainErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
		// already gRPC-shaped with a meaningful code
		return err
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, StripSentinel(err, "not found"))
	case errors.Is(err, domain.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, StripSentinel(err, "already exists"))
	case errors.Is(err, domain.ErrFailedPrecondition):
		return status.Error(codes.FailedPrecondition, StripSentinel(err, "failed precondition"))
	case errors.Is(err, domain.ErrInvalidArg):
		return status.Error(codes.InvalidArgument, StripSentinel(err, "invalid argument"))
	case errors.Is(err, domain.ErrUnavailable):
		return status.Error(codes.Unavailable, StripSentinel(err, "service unavailable"))
	case errors.Is(err, domain.ErrInternal):
		// Internal: НЕ leak'аем raw pgx text — отдаём константную фразу.
		return status.Error(codes.Internal, "internal database error")
	}
	// Default: Internal без leak'а текста.
	return status.Error(codes.Internal, "internal error")
}

// StripSentinel убирает sentinel-prefix "<text>: " из строки ошибки, чтобы
// чистый по конвенции Kachō текст доходил до клиента. Если префикса нет —
// возвращает err.Error() как есть; nil / пустой результат → fallback.
func StripSentinel(err error, fallback string) string {
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
