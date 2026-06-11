package kacho

import (
	"context"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// FGARegisterEmitter — emit одного FGA-register-intent в
// `kacho_nlb.fga_register_outbox` (SEC-D). Использует pgx.Tx writer'а, поэтому
// INSERT/DELETE ресурса + register-intent commit'ятся атомарно одной writer-tx
// (epic §3.1 Вариант A — no dual-write, в отличие от прежнего best-effort
// fgawrite после Commit, GitHub Issue N5).
//
// eventType ∈ {domain.FGAEventRegister, domain.FGAEventUnregister}. CHECK
// constraint в `fga_register_outbox` (миграция 0002) защищает от typo →
// SQLSTATE 23514 → ErrInvalidArg в mapPgErr.
//
// Пустой набор tuple (intent.Tuples == 0) — no-op (строка не пишется): нечего
// регистрировать (напр. system-initiated Create без creator-tuple, но с
// project-hierarchy — набор непуст; полностью пустой набор не возникает в
// нормальном флоу, но guard защищает от записи пустой строки).
type FGARegisterEmitter interface {
	Emit(ctx context.Context, eventType string, intent domain.FGARegisterIntent) error
}
