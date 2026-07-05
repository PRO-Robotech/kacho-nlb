// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operation

import (
	"github.com/PRO-Robotech/kacho-corelib/operations"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/shared"
)

// operationToProto — тонкий делегатор к единому `shared.OperationToProto`
// (один источник истины для всех use-case пакетов, см. audit LEAN #10).
func operationToProto(op *operations.Operation) *operationpb.Operation {
	return shared.OperationToProto(op)
}
