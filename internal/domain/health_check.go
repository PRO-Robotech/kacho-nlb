package domain

import (
	"time"

	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
	"go.uber.org/multierr"
)

// HealthCheck — desired-конфигурация health-check (design §2.2). В
// control-plane-only фазе не исполняется (acceptance §0.1); GetTargetStates
// возвращает детерминированный computed-ramp.
//
// Probe-тип — 4-way oneof: exactly one of TCP/HTTP/HTTPS/GRPC должен быть
// не-nil (acceptance TGR-003/004). Конкретные options-структуры — внутри
// этого файла, чтобы domain-пакет был compact.
type HealthCheck struct {
	Name               LbName
	Interval           LbDuration
	Timeout            LbDuration
	UnhealthyThreshold int32
	HealthyThreshold   int32
	TCP                *HealthCheckTCP
	HTTP               *HealthCheckHTTP
	HTTPS              *HealthCheckHTTPS
	GRPC               *HealthCheckGRPC
}

// HealthCheckTCP — TCP-probe; полезной нагрузки нет, только port.
type HealthCheckTCP struct {
	Port LbPort
}

// HealthCheckHTTP — HTTP-probe.
type HealthCheckHTTP struct {
	Port             LbPort
	Path             string
	ExpectedStatuses []int32 // (опционально; пусто = «любой 2xx», semantics в worker'е)
}

// HealthCheckHTTPS — HTTPS-probe.
type HealthCheckHTTPS struct {
	Port             LbPort
	Path             string
	ExpectedStatuses []int32
}

// HealthCheckGRPC — gRPC health-probe.
type HealthCheckGRPC struct {
	Port        LbPort
	ServiceName string
}

// Validate — exactly-one-of TCP/HTTP/HTTPS/GRPC + bound checks (interval,
// timeout, thresholds). Покрывает acceptance TGR-003..TGR-006.
func (h HealthCheck) Validate() error {
	probeErr := h.validateProbeOneOf()

	intervalErr := error(nil)
	if h.Interval < HealthIntervalMin || h.Interval > HealthIntervalMax {
		intervalErr = coreerrors.InvalidArgument().
			AddFieldViolation("health_check.interval",
				"health_check.interval must be in range [1s, 600s]").
			Err()
	}

	timeoutErr := error(nil)
	switch {
	case h.Timeout < HealthTimeoutMin:
		timeoutErr = coreerrors.InvalidArgument().
			AddFieldViolation("health_check.timeout",
				"health_check.timeout must be positive (>= 1ms)").
			Err()
	case h.Interval > 0 && time.Duration(h.Timeout) > time.Duration(h.Interval):
		// timeout не может превышать interval — иначе probe overlap'ит сам себя.
		timeoutErr = coreerrors.InvalidArgument().
			AddFieldViolation("health_check.timeout",
				"health_check.timeout must be <= health_check.interval").
			Err()
	}

	unhealthyErr := error(nil)
	if h.UnhealthyThreshold < HealthThresholdMin || h.UnhealthyThreshold > HealthThresholdMax {
		unhealthyErr = coreerrors.InvalidArgument().
			AddFieldViolation("health_check.unhealthy_threshold",
				"unhealthy_threshold must be in range [2, 10]").
			Err()
	}

	healthyErr := error(nil)
	if h.HealthyThreshold < HealthThresholdMin || h.HealthyThreshold > HealthThresholdMax {
		healthyErr = coreerrors.InvalidArgument().
			AddFieldViolation("health_check.healthy_threshold",
				"healthy_threshold must be in range [2, 10]").
			Err()
	}

	return multierr.Combine(
		h.Name.Validate(),
		probeErr,
		intervalErr,
		timeoutErr,
		unhealthyErr,
		healthyErr,
	)
}

// validateProbeOneOf — exactly-one-of TCP/HTTP/HTTPS/GRPC + port-range на
// выбранном probe. Acceptance TGR-003 / TGR-004 verbatim:
// `"health_check must specify exactly one of: tcp, http, https, grpc"`.
func (h HealthCheck) validateProbeOneOf() error {
	count := 0
	if h.TCP != nil {
		count++
	}
	if h.HTTP != nil {
		count++
	}
	if h.HTTPS != nil {
		count++
	}
	if h.GRPC != nil {
		count++
	}
	if count != 1 {
		return coreerrors.InvalidArgument().
			AddFieldViolation("health_check",
				"health_check must specify exactly one of: tcp, http, https, grpc").
			Err()
	}
	switch {
	case h.TCP != nil:
		return h.TCP.Port.Validate()
	case h.HTTP != nil:
		return h.HTTP.Port.Validate()
	case h.HTTPS != nil:
		return h.HTTPS.Port.Validate()
	case h.GRPC != nil:
		return h.GRPC.Port.Validate()
	}
	return nil
}
