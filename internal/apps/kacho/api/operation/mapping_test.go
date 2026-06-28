// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

func TestOperationToProto_AllFields(t *testing.T) {
	t.Run("in-flight without principal — fallback (zero principal carried through)", func(t *testing.T) {
		op := &operations.Operation{
			ID:          "nlb12345678901234567",
			Description: "Create NLB",
			CreatedAt:   time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC),
			ModifiedAt:  time.Date(2026, 5, 24, 12, 0, 5, 0, time.UTC),
			CreatedBy:   "usr-test",
			Done:        false,
		}
		p := operationToProto(op)
		require.NotNil(t, p)
		assert.Equal(t, op.ID, p.GetId())
		assert.Equal(t, op.Description, p.GetDescription())
		assert.Equal(t, op.CreatedBy, p.GetCreatedBy())
		assert.False(t, p.GetDone())
		assert.Nil(t, p.GetError())
		assert.Nil(t, p.GetResponse())
		assert.Empty(t, p.GetPrincipalType())
		assert.Empty(t, p.GetPrincipalId())
	})

	t.Run("completed with response anypb (success)", func(t *testing.T) {
		resp, err := anypb.New(&emptypb.Empty{})
		require.NoError(t, err)
		op := &operations.Operation{
			ID:        "nlb22345678901234567",
			Done:      true,
			Response:  resp,
			Principal: operations.Principal{Type: "user", ID: "usr-x", DisplayName: "alice@kacho"},
		}
		p := operationToProto(op)
		assert.True(t, p.GetDone())
		require.NotNil(t, p.GetResponse(), "response oneof must be set")
		assert.Nil(t, p.GetError())
		assert.Equal(t, "user", p.GetPrincipalType())
		assert.Equal(t, "usr-x", p.GetPrincipalId())
		assert.Equal(t, "alice@kacho", p.GetPrincipalDisplayName())
	})

	t.Run("completed with error (CANCELLED)", func(t *testing.T) {
		op := &operations.Operation{
			ID:    "nlb32345678901234567",
			Done:  true,
			Error: &status.Status{Code: 1, Message: "operation cancelled"},
		}
		p := operationToProto(op)
		assert.True(t, p.GetDone())
		require.NotNil(t, p.GetError(), "error oneof must be set")
		assert.EqualValues(t, 1, p.GetError().GetCode())
		assert.Equal(t, "operation cancelled", p.GetError().GetMessage())
		assert.Nil(t, p.GetResponse())
	})
}
