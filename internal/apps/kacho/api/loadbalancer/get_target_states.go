package loadbalancer

// TODO(KAC-156): GetTargetStatesUseCase.
//   - sync read (НЕ операция; design §3.1 contract).
//   - детерминированный computed-ramp HC-state (acceptance §0.1):
//     INITIAL пока age < HC.interval × HC.healthy_threshold, затем HEALTHY;
//     DRAINING / INACTIVE — по таблице состояний LB и target.
