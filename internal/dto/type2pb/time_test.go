// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package type2pb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
)

func TestTime_TruncateToSeconds(t *testing.T) {
	in := time.Date(2026, 5, 24, 12, 34, 56, 999_888_777, time.UTC)
	var out *timestamppb.Timestamp
	require.NoError(t, dto.Transfer(dto.FromTo(in, &out)))
	require.NotNil(t, out)
	assert.Equal(t, in.Truncate(time.Second).Unix(), out.AsTime().Unix())
	assert.Equal(t, int32(0), out.Nanos, "nanos truncated")
}

func TestTime_ZeroValue(t *testing.T) {
	var zero time.Time
	var out *timestamppb.Timestamp
	require.NoError(t, dto.Transfer(dto.FromTo(zero, &out)))
	require.NotNil(t, out)
	assert.Equal(t, zero.Unix(), out.AsTime().Unix())
}
