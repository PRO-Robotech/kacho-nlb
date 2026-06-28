// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authzfilter

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// SubjectFromCtx — извлекает FGA subject ("user:<id>" / "service_account:<id>")
// из ctx через operations.PrincipalFromContext (nlb-конвенция: principal кладётся
// grpcsrv.UnaryPrincipalExtract на обоих listener'ах). system / unauthenticated
// principal → "" (use-case трактует "" как bypass — фоновые/dev вызовы не
// фильтруются; production-mode anonymous отбивается interceptor'ом ДО handler'а).
//
// Зеркало internal/domain.FGASubjectFromPrincipal — единый формат subject-строки.
func SubjectFromCtx(ctx context.Context) string {
	p := operations.PrincipalFromContext(ctx)
	return domain.FGASubjectFromPrincipal(p.Type, p.ID)
}

// Resolve — единый entry-point для List use-case'ов всех ресурсов nlb. Извлекает
// subject из ctx, вызывает filter.ListAllowedIDs и нормализует решение:
//
//   - filter == nil               → bypass (list-filter disabled / dev).
//   - subject == "" (system)      → bypass (фоновые/dev вызовы без identity).
//   - filter вернул BypassAll      → bypass (admin / wildcard-grant / fail-open).
//   - filter вернул Empty/пусто    → Decision{Empty:true} (use-case вернёт пустой ответ).
//   - иначе                        → Decision{AllowedIDs:...} (пересечение в SQL).
//   - filter error                 → возвращается как есть (fail-closed Unavailable).
//
// Use-case'ы НЕ дублируют subject-extraction/bypass-логику — зовут Resolve.
func Resolve(ctx context.Context, filter Filter, resourceType, action string) (Decision, error) {
	if filter == nil {
		return Decision{BypassAll: true}, nil
	}
	subject := SubjectFromCtx(ctx)
	if subject == "" {
		return Decision{BypassAll: true}, nil
	}
	dec, err := filter.ListAllowedIDs(ctx, subject, resourceType, action)
	if err != nil {
		// Fail-closed: любая ошибка фильтра — Unavailable. FGAFilter уже
		// маппит на gRPC-status; defensive guard на случай, если реализация Filter
		// вернула не-status ошибку (иначе она утекла бы как codes.Unknown, не
		// fail-closed). НЕ возвращаем нефильтрованный список — no-leak.
		if _, ok := status.FromError(err); !ok {
			return Decision{}, status.Errorf(codes.Unavailable, "list filter: %v", err)
		}
		return Decision{}, err
	}
	return dec, nil
}
