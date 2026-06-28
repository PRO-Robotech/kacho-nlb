// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain

import "github.com/PRO-Robotech/kacho-corelib/ids"

// Factory-builders для domain-сущностей. Inline-литералы domain-структур с
// magic-defaults (Status="CREATING", CrossZoneEnabled=true, SlowStart=0,
// DeregistrationDelay=300) в use-case-слое — запрещены.
// Все «как должна выглядеть свежесозданная сущность»-defaults живут
// здесь, в одном месте — единственном legal-источнике этих констант.

// NewLoadBalancer строит новую LoadBalancer-сущность с заданными tenant-полями
// (project/region/name/type/description/labels) и safe-defaults:
//   - ID: свежий `nlb`-prefixed crockford-base32 (corelib/ids);
//   - Status: CREATING;
//   - SessionAffinity: FIVE_TUPLE (default);
//   - CrossZoneEnabled: true (default);
//   - DeletionProtection: false.
//
// Caller обязан вызвать `lb.Validate` перед repo.Insert — builder не
// валидирует, чтобы service-слой собрал все ошибки за один проход.
func NewLoadBalancer(
	projectID ProjectID,
	regionID RegionID,
	name LbName,
	description LbDescription,
	labels LbLabels,
	lbType LBType,
) LoadBalancer {
	return LoadBalancer{
		ID:                 ResourceID(ids.NewID(ids.PrefixLoadBalancer)),
		ProjectID:          projectID,
		RegionID:           regionID,
		Name:               name,
		Description:        description,
		Labels:             labels,
		Type:               lbType,
		Status:             LBStatusCreating,
		SessionAffinity:    SessionAffinity5Tuple,
		CrossZoneEnabled:   true,
		DeletionProtection: false,
	}
}

// NewListener строит новую Listener-сущность с минимальным набором tenant-полей.
// Дополнительные поля (AddressID/SubnetID/DefaultTargetGroupID/Labels) caller
// устанавливает после builder'а — они не считаются "обязательными для конструкции".
//
// Defaults:
//   - ID: свежий `lst`-prefix;
//   - RegionID: денормализуется из LB (caller передаёт);
//   - Status: CREATING;
//   - ProxyProtocolV2: false.
func NewListener(
	lb LoadBalancer,
	name LbName,
	protocol LbProto,
	port LbPort,
	targetPort LbPort,
	ipVersion IPVersion,
) Listener {
	return Listener{
		ID:              ResourceID(ids.NewID(ids.PrefixListener)),
		ProjectID:       lb.ProjectID,
		LoadBalancerID:  lb.ID,
		RegionID:        lb.RegionID,
		Name:            name,
		Protocol:        protocol,
		Port:            port,
		TargetPort:      targetPort,
		IPVersion:       ipVersion,
		ProxyProtocolV2: false,
		Status:          ListenerStatusCreating,
	}
}

// NewTargetGroup строит новую TargetGroup с tenant-полями + safe-defaults:
//   - ID: свежий `tgr`-prefix;
//   - Targets: пустой (caller добавляет через AddTargets use-case);
//   - HealthCheck: caller задаёт (нет sensible default — это required-поле);
//   - DeregistrationDelaySeconds: DefaultDeregistrationDelay (300);
//   - SlowStartSeconds: DefaultSlowStart (0);
//   - Status: ACTIVE (TG нет staging-фазы).
func NewTargetGroup(
	projectID ProjectID,
	regionID RegionID,
	name LbName,
	description LbDescription,
	labels LbLabels,
) TargetGroup {
	return TargetGroup{
		ID:                         ResourceID(ids.NewID(ids.PrefixTargetGroup)),
		ProjectID:                  projectID,
		RegionID:                   regionID,
		Name:                       name,
		Description:                description,
		Labels:                     labels,
		Targets:                    nil,
		DeregistrationDelaySeconds: DefaultDeregistrationDelay,
		SlowStartSeconds:           DefaultSlowStart,
		Status:                     TargetGroupStatusActive,
	}
}

// NewDefaultHealthCheck — HC c safe-defaults для проб TCP/HTTP/HTTPS/GRPC.
// Caller передаёт probe-pointer уже сконструированным (TCP/HTTP/...).
// Defaults: Interval=2s, Timeout=1s, UnhealthyThreshold=2, HealthyThreshold=2.
func NewDefaultHealthCheck(name LbName, proto HealthCheckProto, port LbPort) HealthCheck {
	hc := HealthCheck{
		Name:               name,
		Interval:           DefaultHealthInterval,
		Timeout:            DefaultHealthTimeout,
		UnhealthyThreshold: DefaultUnhealthyThreshold,
		HealthyThreshold:   DefaultHealthyThreshold,
	}
	switch proto {
	case HealthCheckProtoTCP:
		hc.TCP = &HealthCheckTCP{Port: port}
	case HealthCheckProtoHTTP:
		hc.HTTP = &HealthCheckHTTP{Port: port}
	case HealthCheckProtoHTTPS:
		hc.HTTPS = &HealthCheckHTTPS{Port: port}
	case HealthCheckProtoGRPC:
		hc.GRPC = &HealthCheckGRPC{Port: port}
	}
	return hc
}

// TruncateID возвращает первые ShortIDLen символов id (или весь, если короче).
// Используется builder'ами derived-имён (`default-tg-<short>` и т.п.). Зеркалит
// kacho-vpc/internal/domain.TruncateID.
func TruncateID(id ResourceID) string {
	s := string(id)
	if len(s) > ShortIDLen {
		return s[:ShortIDLen]
	}
	return s
}
