// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package type2pb

import (
	"time"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// healthCheckToPb — конвертер domain.HealthCheck → *lbv1.HealthCheck.
//
// Proto имеет только oneof {tcp_options, http_options}. Domain имеет 4
// варианта (TCP/HTTP/HTTPS/GRPC). HTTPS и GRPC варианты пока не имеют
// proto-эквивалентов — для них возвращаем pb без `options` set (downstream
// должен trip-fail на missing oneof). В будущем при появлении HTTPS/GRPC в
// proto — расширить switch.
//
// Это helper-функция (не Interface[F,T] в registry), потому что HealthCheck —
// embedded value в TargetGroup, а не самостоятельная сущность с CreatedAt
// (нет отдельной репо-сущности). Вызывается inline из targetGroup{}.toPb.
func healthCheckToPb(hc domain.HealthCheck) *lbv1.HealthCheck {
	// Если HC пустой (нулевой) — возвращаем nil (proto-field optional).
	if isHealthCheckZero(hc) {
		return nil
	}
	out := &lbv1.HealthCheck{
		Name:               string(hc.Name),
		Interval:           durationpb.New(time.Duration(hc.Interval)),
		Timeout:            durationpb.New(time.Duration(hc.Timeout)),
		UnhealthyThreshold: int64(hc.UnhealthyThreshold),
		HealthyThreshold:   int64(hc.HealthyThreshold),
	}
	switch {
	case hc.TCP != nil:
		out.Options = &lbv1.HealthCheck_TcpOptions_{
			TcpOptions: &lbv1.HealthCheck_TcpOptions{Port: int64(hc.TCP.Port)},
		}
	case hc.HTTP != nil:
		out.Options = &lbv1.HealthCheck_HttpOptions_{
			HttpOptions: &lbv1.HealthCheck_HttpOptions{
				Port: int64(hc.HTTP.Port), Path: hc.HTTP.Path,
			},
		}
		// HTTPS / GRPC — нет в proto. Domain.Validate их разрешает (TargetGroup
		// принимает их на input), но output → клиент получит pb без options-oneof.
		// Это сознательная limitation до расширения proto.
	}
	return out
}

func isHealthCheckZero(hc domain.HealthCheck) bool {
	return hc.Name == "" &&
		hc.Interval == 0 &&
		hc.Timeout == 0 &&
		hc.UnhealthyThreshold == 0 &&
		hc.HealthyThreshold == 0 &&
		hc.TCP == nil && hc.HTTP == nil && hc.HTTPS == nil && hc.GRPC == nil
}
