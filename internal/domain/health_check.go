package domain

// HealthCheck — desired configuration HC (TCP/HTTP/HTTPS/gRPC). В control-plane
// only фазе НЕ исполняется (acceptance §0.1); GetTargetStates возвращает
// детерминированный computed-ramp (`INITIAL` → `HEALTHY` после
// `interval × healthy_threshold`).
//
// TODO(KAC-147): структура полей согласно design §2.2.
type HealthCheck struct {
	// TODO(KAC-147): Type, Interval, Timeout, HealthyThreshold, UnhealthyThreshold,
	// TCPOptions / HTTPOptions / HTTPSOptions / GRPCOptions oneof (per design).
}
