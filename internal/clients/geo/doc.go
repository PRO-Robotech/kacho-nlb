// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package geo — typed adapter-клиент к kacho-geo (Clean Architecture outbound
// adapter, kacho-geo).
//
// kacho-geo — leaf-owner Geography domain (Region/Zone), выделенный из
// kacho-compute (см. «extract Geography из kacho-compute»). kacho-nlb
// валидирует:
//
//   - NetworkLoadBalancer.region_id / TargetGroup.region_id → geo.RegionService.Get
//     (sync precheck на request-path; несуществующий region → InvalidArgument;
//     geo недоступен → Unavailable, fail-closed на мутации).
//
// Region-precheck — stateless pass-through: один geo.RegionService.Get-вызов,
// без кэша и без перечисления зон (kacho-nlb region-precheck зоны не листует;
// введение кэша — вне scope этого extract). Это намеренно отличается от прежнего
// compute-клиента, который дополнительно листовал ZoneService.List ради
// неиспользуемого consumer'ами поля Region.Zones.
//
// Все adapter'ы возвращают sentinel-ошибки из `internal/domain`
// (`domain.ErrInvalidArg` / `domain.ErrUnavailable`); service-слой работает
// только через port-интерфейсы.
package geo
