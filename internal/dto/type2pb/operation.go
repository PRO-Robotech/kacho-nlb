package type2pb

import (
	operationv1 "github.com/PRO-Robotech/kacho-corelib/proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
)

// operationPb — identity pass-through трансфер. Зарегистрирован чтобы handler'ы
// могли uniform-вызывать `dto.Transfer(dto.FromTo(opPb, &dst))` для всех output
// типов (LRO Operation проходит через тот же DTO-пайплайн что и ресурсы).
//
// Pass-through нужен потому, что `kacho-corelib/operations.Operation` (domain
// LRO) → `*operationv1.Operation` (proto) — конверсия живёт в corelib (см.
// operations.OperationToProto). К моменту handler-вызова это уже proto-type,
// и DTO-пайплайн должен прокинуть его без изменений (но с тем же error-shape).
type operationPb struct{}

func (operationPb) toPb(op *operationv1.Operation) (*operationv1.Operation, error) {
	return op, nil
}

func init() {
	dto.RegTransfer(dto.Fn2Face(operationPb{}.toPb))
}
