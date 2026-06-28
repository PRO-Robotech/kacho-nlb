// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package type2pb — реализации DTO трансферов domain/repo → proto.
//
// Per-resource файлы:
//
//	time.go            — time.Time → *timestamppb.Timestamp (truncate to seconds)
//	loadbalancer.go    — LoadBalancerRecord → *lbv1.NetworkLoadBalancer
//	listener.go        — ListenerRecord → *lbv1.Listener
//	target_group.go    — TargetGroupRecord → *lbv1.TargetGroup (incl. inline Targets)
//	target.go          — TargetRecord → *lbv1.Target (4-way identity oneof)
//	health_check.go    — domain.HealthCheck → *lbv1.HealthCheck
//	operation.go       — *opv1.Operation → *opv1.Operation (identity pass-through;
//	                     зарегистрирован чтобы tests/handlers могли uniform-вызывать
//	                     dto.Transfer для всех output-типов)
//
// init каждого файла регистрирует трансфер в dto.RegTransfer.
package type2pb
