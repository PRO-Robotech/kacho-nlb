// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain

import (
	"math"

	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
)

// Proto numeric fields (port, target_port, health-check thresholds) are int64 on
// the wire, but their domain newtypes are int32. A bare int64→int32 conversion
// truncates the high bits (CWE-190): an out-of-range value such as 2^32+443 would
// silently alias onto a valid remainder (443) and slip past the range Validate.
// The helpers below reject anything outside the int32 domain BEFORE narrowing, so
// the conversion is provably lossless; the canonical range boundary ([1,65535] for
// ports, [2,10] for thresholds) is still enforced by the newtype's Validate.

// LbPortFromProto narrows a proto int64 port field into the LbPort newtype,
// rejecting int32 overflow with InvalidArgument.
func LbPortFromProto(v int64) (LbPort, error) {
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, coreerrors.InvalidArgument().
			AddFieldViolation("port", "port must be in range [1, 65535]").
			Err()
	}
	return LbPort(v), nil
}

// HealthThresholdFromProto narrows a proto int64 health-check threshold field into
// int32, rejecting int32 overflow with InvalidArgument. field names the offending
// threshold (unhealthy_threshold / healthy_threshold) in the error message.
func HealthThresholdFromProto(field string, v int64) (int32, error) {
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, coreerrors.InvalidArgument().
			AddFieldViolation("health_check."+field, field+" must be in range [2, 10]").
			Err()
	}
	return int32(v), nil
}
