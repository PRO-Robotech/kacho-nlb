package check

import (
	"context"
	"errors"

	"github.com/PRO-Robotech/kacho-corelib/authz"

	iamclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
)

// IAMCheckClient — adapter, реализующий port `authz.CheckClient` поверх
// уже существующего peer-клиента `iam.CheckClient` (`internal/clients/iam`).
//
// Decoupling: kacho-corelib/authz НЕ зависит от kacho-proto stubs (см.
// `kacho-corelib/authz/check_client.go`); peer-client уже инкапсулирует
// gRPC вызов `InternalIAMService.Check` + auth.PropagateOutgoing + retry
// + sentinel mapping (`authz.ErrNoPath` для FGA "no path", domain.ErrUnavailable
// для transport-level fail).
//
// Adapter сводит контракт `iam.CheckClient.Check(ctx, sub, rel, obj) → (bool, error)`
// к `authz.CheckClient.Check(ctx, sub, rel, obj) → (bool, error)` — сигнатуры
// идентичны, но интерфейсы разные (peer-client живёт в `internal/clients/iam`,
// нужен для composition root; authz-interceptor требует kacho-corelib интерфейс).
//
// Sentinel passthrough (KAC-133):
//   - peer-client возвращает `authz.ErrNoPath` если IAM сказал
//     allowed=false с причиной "no path" → adapter транзитом передаёт
//     наружу (interceptor → DecisionNoPath → handler → NOT_FOUND из БД).
//   - peer-client возвращает обёрнутый sentinel domain-ошибок
//     (`ErrUnavailable`, `ErrInvalidArg`) → adapter транзитом передаёт
//     наружу (interceptor → DecisionUnavailable → PermissionDenied
//     fail-closed для других error'ов или DecisionDenied для invalid args).
type IAMCheckClient struct {
	peer iamclient.CheckClient
}

// NewIAMCheckClient — конструктор adapter'а. peer обычно создаётся в
// composition root через `iamclient.NewCheckClient(grpcConn)`. Если peer=nil
// — adapter тоже nil (caller decides: fail если не в breakglass mode).
func NewIAMCheckClient(peer iamclient.CheckClient) *IAMCheckClient {
	if peer == nil {
		return nil
	}
	return &IAMCheckClient{peer: peer}
}

// Check — реализация `authz.CheckClient.Check`. Просто транзит к peer-клиенту;
// все sentinel-ы (`authz.ErrNoPath`, `domain.ErrUnavailable`, ...) уже
// нормализованы в peer-client'е, дополнительной обработки не требуется.
func (c *IAMCheckClient) Check(ctx context.Context, subjectID, relation, object string) (bool, error) {
	if c == nil || c.peer == nil {
		// Defensive — не должно случаться: caller обязан проверить nil перед wiring.
		return false, errors.New("check: IAMCheckClient.peer is nil")
	}
	return c.peer.Check(ctx, subjectID, relation, object)
}

// Compile-time гарантия, что adapter реализует authz.CheckClient.
var _ authz.CheckClient = (*IAMCheckClient)(nil)
