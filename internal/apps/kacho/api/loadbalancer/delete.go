package loadbalancer

// TODO(KAC-153): DeleteLoadBalancerUseCase.
//   - проверка deletion_protection=false.
//   - FK RESTRICT блокирует если есть Listener — verbatim "load balancer is in use".
//   - hard-delete (паритет VPC/Compute 1.0; no soft-delete).
//   - outbox-emit "DELETED" + FGA DeleteTuples (creator + hierarchy).
