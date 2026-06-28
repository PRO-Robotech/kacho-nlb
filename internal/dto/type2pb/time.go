// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package type2pb

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
)

// timeObj — нулевой struct-receiver для метода-трансфера time.Time → pb timestamp.
// Существует ради единого стиля «<resource>{}.toPb» (см. loadbalancer.go).
type timeObj struct{}

// toPb — truncate до секунд (по конвенции Kachō).
func (timeObj) toPb(t time.Time) (*timestamppb.Timestamp, error) {
	return timestamppb.New(t.Truncate(time.Second)), nil
}

func init() {
	dto.RegTransfer(dto.Fn2Face(timeObj{}.toPb))
}
