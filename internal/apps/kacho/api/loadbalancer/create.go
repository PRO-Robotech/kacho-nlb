package loadbalancer

// TODO(KAC-153): CreateLoadBalancerUseCase.
//
//   - принимает domain.LoadBalancer на входе (через NewLoadBalancer builder).
//   - валидирует через lb.Validate() (multierr.Combine на все newtype'ы).
//   - открывает write-TX, insert + outbox-emit "nlb_load_balancer:<id> CREATED".
//   - возвращает operation.Operation (async LRO).
//   - в worker'е: cross-service validation (region, project через iam.ProjectService.Get),
//     repo.LoadBalancers().Insert, FGA WriteCreatorTuple, outbox-emit "ACTIVE".
