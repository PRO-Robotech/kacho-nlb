package kacho

import "github.com/PRO-Robotech/kacho-nlb/internal/domain"

// Sentinel errors репо-слоя kacho-nlb.
//
// Re-export'ы из `domain` пакета (там же — gRPC-mapping контракт): live-aliasing
// через `var X = domain.X` гарантирует, что `errors.Is(repoErr, kacho.ErrNotFound)`
// и `errors.Is(repoErr, domain.ErrNotFound)` дают одинаковый результат — нет
// двух независимых identity, что сломало бы `errors.Is` в service-слое.
//
// gRPC mapping (см. domain/errors.go):
//
//	ErrNotFound            → codes.NotFound
//	ErrAlreadyExists       → codes.AlreadyExists       (UNIQUE 23505)
//	ErrFailedPrecondition  → codes.FailedPrecondition  (FK 23503 / CAS-miss)
//	ErrInvalidArg          → codes.InvalidArgument     (CHECK 23514)
//	ErrInternal            → codes.Internal            (no leak)
//	ErrUnavailable         → codes.Unavailable
var (
	ErrNotFound           = domain.ErrNotFound
	ErrAlreadyExists      = domain.ErrAlreadyExists
	ErrFailedPrecondition = domain.ErrFailedPrecondition
	ErrInvalidArg         = domain.ErrInvalidArg
	ErrInternal           = domain.ErrInternal
	ErrUnavailable        = domain.ErrUnavailable
)
