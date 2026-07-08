// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package iam

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/authz"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	iampb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// DefaultCheckTimeout — per-call deadline применяемый к
// InternalIAMService.Check, когда client построен без явного timeout'а
// (`NewCheckClient` / `NewCheckClientFromStub`). Значение мирроит fallback
// самого `authz.Interceptor` (`kacho-corelib/authz/interceptor.go`,
// CheckTimeout<=0 → 2s) — интерцептор применяет CheckTimeout только к
// вызовам, которые проходят через него; handler-side прямые Check-вызовы
// (attach_target_group.go, move.go) вне интерцептора без этого поля висели
// бы на raw caller-ctx неограниченно долго при зависшем iam/FGA-peer'е.
// Production-wiring (main.go) передаёт явный `cfg.Authz.IAM.RequestTimeout`
// через `NewCheckClientWithTimeout` — этот default только для конструкторов
// без явного значения (тесты, dev fallback).
const DefaultCheckTimeout = 2 * time.Second

// CheckClient — per-RPC FGA authorization gate; обёртка над
// `kacho.iam.v1.InternalIAMService.Check`. Используется authz-interceptor
// (`internal/check`). Подключение типа passthrough — sentinel
// `authz.ErrNoPath` пробрасывается наружу (interceptor решает DecisionNoPath →
// пропустить вызов в handler, который вернёт NOT_FOUND из DB).
type CheckClient interface {
	// Check возвращает (allowed, error). Семантика:
	//   - allowed=true,  err=nil      — пользователь имеет доступ.
	//   - allowed=false, err=nil      — пользователь не имеет доступа (deny).
	//   - allowed=false, err=authz.ErrNoPath — нет hierarchy-tuple для object'а
	//     (ресурс скорее всего не существует; interceptor → DecisionNoPath →
	//     handler вернёт NOT_FOUND из DB).
	//   - allowed=false, err=domain.ErrUnavailable — FGA / kacho-iam недоступен
	//     (fail-closed: interceptor → DecisionUnavailable → PermissionDenied).
	//   - allowed=false, err=domain.ErrInvalidArg — bad subject/relation/object.
	Check(ctx context.Context, subjectID, relation, object string) (bool, error)
}

// checkClient — реализация CheckClient через gRPC.
type checkClient struct {
	cli     iampb.InternalIAMServiceClient
	timeout time.Duration
}

// NewCheckClient оборачивает grpc-conn в typed adapter. conn должен быть к
// kacho-iam **internal**-listener (`:9091`) — InternalIAMService.Check не
// публикуется на external endpoint (Internal-only). Per-call timeout —
// DefaultCheckTimeout; композиционный root, у которого есть configured
// `cfg.Authz.IAM.RequestTimeout`, обязан использовать
// NewCheckClientWithTimeout вместо этого конструктора.
func NewCheckClient(conn grpc.ClientConnInterface) CheckClient {
	return NewCheckClientWithTimeout(conn, DefaultCheckTimeout)
}

// NewCheckClientWithTimeout — как NewCheckClient, но с явным per-call
// timeout'ом (mirror `cfg.Authz.IAM.RequestTimeout`, тот же источник, что
// `check.Options.CheckTimeout` интерцептора). timeout<=0 → DefaultCheckTimeout.
func NewCheckClientWithTimeout(conn grpc.ClientConnInterface, timeout time.Duration) CheckClient {
	if conn == nil {
		return nil
	}
	return &checkClient{cli: iampb.NewInternalIAMServiceClient(conn), timeout: resolveCheckTimeout(timeout)}
}

// NewCheckClientFromStub — конструктор для тестов: принимает напрямую stub.
func NewCheckClientFromStub(cli iampb.InternalIAMServiceClient) CheckClient {
	return NewCheckClientFromStubWithTimeout(cli, DefaultCheckTimeout)
}

// NewCheckClientFromStubWithTimeout — как NewCheckClientFromStub, но с явным
// per-call timeout'ом (используется тестами concurrency/timeout-фиксов).
func NewCheckClientFromStubWithTimeout(cli iampb.InternalIAMServiceClient, timeout time.Duration) CheckClient {
	if cli == nil {
		return nil
	}
	return &checkClient{cli: cli, timeout: resolveCheckTimeout(timeout)}
}

func resolveCheckTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultCheckTimeout
	}
	return d
}

// Check — см. контракт CheckClient.Check.
func (c *checkClient) Check(ctx context.Context, subjectID, relation, object string) (bool, error) {
	switch {
	case subjectID == "":
		return false, fmt.Errorf("%w: subject_id is empty", domain.ErrInvalidArg)
	case relation == "":
		return false, fmt.Errorf("%w: relation is empty", domain.ErrInvalidArg)
	case object == "":
		return false, fmt.Errorf("%w: object is empty", domain.ErrInvalidArg)
	}

	// follow-up: outgoing ctx wrap with auth.PropagateOutgoing
	// so iam-side UnaryPrincipalExtract sees real caller (not SystemPrincipal).
	ctx = auth.PropagateOutgoing(ctx)

	// Per-call deadline — bounds the ENTIRE retry.OnUnavailable operation
	// (not just a single attempt), independent of the caller's own ctx.
	// Without this, a stalled iam/FGA peer (keepalive still acked, no
	// response) parks the calling goroutine forever: handler-side Check
	// calls (attach_target_group.go, move.go) run outside the authz
	// interceptor's own CheckTimeout-bounded ctx, so raw caller-ctx alone
	// does not bound the call (architecture.md "Per-call deadline на КАЖДОМ
	// внешнем вызове").
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var resp *iampb.CheckResponse
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		resp, rerr = c.cli.Check(ctx, &iampb.CheckRequest{
			SubjectId: subjectID,
			Relation:  relation,
			Object:    object,
		})
		return rerr
	})
	if err != nil {
		return false, mapCheckErr(err)
	}
	if resp.GetAllowed() {
		return true, nil
	}
	// Passthrough `no-path` reason: interceptor → DecisionNoPath →
	// handler вернёт NOT_FOUND из DB. Reason-формат — substring "no path"
	// (kacho-iam emits "no FGA path: <object>") либо явный prefix.
	if isNoPathReason(resp.GetReason()) {
		return false, authz.ErrNoPath
	}
	return false, nil
}

// isNoPathReason — повторяет detection-логику kacho-corelib/authz: сравнивает
// `CheckResponse.reason` с известными "no path"-маркерами FGA.
func isNoPathReason(reason string) bool {
	r := strings.ToLower(reason)
	return strings.Contains(r, "no path") || strings.Contains(r, "no_path")
}

// mapCheckErr транслирует gRPC-status в domain-sentinel-ошибки. См. контракт
// CheckClient.Check.
func mapCheckErr(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("iam check: %w", err)
	}
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: iam check: %s", domain.ErrUnavailable, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: iam check: %s", domain.ErrInvalidArg, st.Message())
	default:
		return fmt.Errorf("iam check: %w", err)
	}
}
