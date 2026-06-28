// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package compute — typed adapter-клиент к kacho-compute (Clean Architecture
// outbound adapter).
//
// kacho-compute — owner Instance/Disk/Image/Snapshot. Geography (Region/Zone)
// вынесена в отдельный leaf-сервис kacho-geo (kacho-geo) — region-
// валидация теперь живёт в `internal/clients/geo`. kacho-nlb зовёт у compute:
//
//   - TargetGroup.targets.instance_id → compute.InstanceService.Get (TG.AddTargets
//     worker; extract primary NIC v4 address). Несуществующий instance →
//     InvalidArgument; tombstone instance → FailedPrecondition. Это НЕ geography-
//     ребро — instance-resolve остаётся на kacho-compute.
//
// Все adapter'ы возвращают sentinel-ошибки из `internal/domain`
// (`domain.ErrInvalidArg` / `domain.ErrFailedPrecondition` /
// `domain.ErrUnavailable`); service-слой работает только через port-интерфейсы.
package compute
