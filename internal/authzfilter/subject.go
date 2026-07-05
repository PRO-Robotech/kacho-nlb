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
// principal → "". Пустой subject НЕ трактуется как bypass: Resolve прокидывает
// его в filter, который fail-close'ит (enabled FGAFilter → Unauthenticated).
//
// Зеркало internal/domain.FGASubjectFromPrincipal — единый формат subject-строки.
func SubjectFromCtx(ctx context.Context) string {
	p := operations.PrincipalFromContext(ctx)
	return domain.FGASubjectFromPrincipal(p.Type, p.ID)
}

// Resolve — единый entry-point для List use-case'ов всех ресурсов nlb. Извлекает
// subject из ctx, вызывает filter.ListAllowedIDs и нормализует решение:
//
//   - filter == nil               → bypass (list-filter disabled / dev — явно
//     не сконфигурирован filter в composition root).
//   - subject == "" (system)      → subject прокидывается в filter КАК ЕСТЬ
//     (fail-closed). Реальный FGAFilter вернёт Unauthenticated (enabled) либо
//     BypassAll (disabled — enabled=false проверяется ДО subject); никакого
//     short-circuit-bypass на пустом subject'е здесь НЕТ — это была
//     cross-tenant enumeration дыра (audit SEC-high #1 / CWE-862): запрос,
//     потерявший forwarded-principal, не должен перечислять чужой проект.
//   - filter вернул BypassAll      → bypass (admin / wildcard-grant / fail-open /
//     enabled=false).
//   - filter вернул Empty/пусто    → Decision{Empty:true} (use-case вернёт пустой ответ).
//   - иначе                        → Decision{AllowedIDs:...} (пересечение в SQL).
//   - filter error                 → возвращается как есть (fail-closed
//     Unauthenticated/Unavailable).
//
// Use-case'ы НЕ дублируют subject-extraction/bypass-логику — зовут Resolve.
func Resolve(ctx context.Context, filter Filter, resourceType, action string) (Decision, error) {
	if filter == nil {
		return Decision{BypassAll: true}, nil
	}
	subject := SubjectFromCtx(ctx)
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
