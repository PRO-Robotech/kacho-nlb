package loadbalancer

// TODO(KAC-154): MoveLoadBalancerUseCase — смена project_id с FGA-tuple rewrite.
//   - precondition: destination project в том же regionID; same-region constraint остаётся.
//   - FGA: delete old tuples, write new under destination project.
