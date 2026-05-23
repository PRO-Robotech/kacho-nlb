// Package compute — typed adapter-клиенты к kacho-compute (Clean Architecture
// outbound adapter, KAC-151).
//
// kacho-compute — owner Geography domain (Region/Zone) и Instance/Disk/Image/
// Snapshot. kacho-nlb валидирует:
//
//   - NetworkLoadBalancer.region_id → compute.RegionService.Get (sync precheck;
//     несуществующий region → InvalidArgument). Zone-композиция формируется
//     дополнительным compute.ZoneService.List + фильтр RegionId.
//   - TargetGroup.targets[].instance_id → compute.InstanceService.Get (TG.AddTargets
//     worker; extract primary NIC v4 address). Несуществующий instance →
//     InvalidArgument; tombstone instance → FailedPrecondition.
//
// Все adapter'ы возвращают sentinel-ошибки из `internal/domain`
// (`domain.ErrInvalidArg` / `domain.ErrFailedPrecondition` /
// `domain.ErrUnavailable`); service-слой работает только через port-интерфейсы.
package compute
