// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package targetgroup

import (
	"github.com/H-BF/corlib/pkg/option"
	"google.golang.org/protobuf/types/known/anypb"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// healthCheckFromPb — конвертер proto HealthCheck → domain HealthCheck. Proto
// несёт только tcp_options и http_options (см. health_check.proto); HTTPS и
// GRPC варианты в proto отсутствуют (известное ограничение, см.
// internal/dto/type2pb/health_check.go).
//
// Возвращает zero-value HealthCheck если pb==nil — caller'у тогда лучше отдать
// ошибку «health_check is required» вверх. Threshold/port поля int64 на wire;
// сужение в int32 guard'ится (domain.*FromProto) — иначе overflow-значение
// молча алиасит на валидный остаток и обходит HealthCheck.Validate (gosec G115).
func healthCheckFromPb(pb *lbv1.HealthCheck) (domain.HealthCheck, error) {
	if pb == nil {
		return domain.HealthCheck{}, nil
	}
	unhealthy, err := domain.HealthThresholdFromProto("unhealthy_threshold", pb.GetUnhealthyThreshold())
	if err != nil {
		return domain.HealthCheck{}, err
	}
	healthy, err := domain.HealthThresholdFromProto("healthy_threshold", pb.GetHealthyThreshold())
	if err != nil {
		return domain.HealthCheck{}, err
	}
	hc := domain.HealthCheck{
		Name:               domain.LbName(pb.GetName()),
		Interval:           domain.LbDuration(pb.GetInterval().AsDuration()),
		Timeout:            domain.LbDuration(pb.GetTimeout().AsDuration()),
		UnhealthyThreshold: unhealthy,
		HealthyThreshold:   healthy,
	}
	switch v := pb.GetOptions().(type) {
	case *lbv1.HealthCheck_TcpOptions_:
		port, err := domain.LbPortFromProto(v.TcpOptions.GetPort())
		if err != nil {
			return domain.HealthCheck{}, err
		}
		hc.TCP = &domain.HealthCheckTCP{Port: port}
	case *lbv1.HealthCheck_HttpOptions_:
		port, err := domain.LbPortFromProto(v.HttpOptions.GetPort())
		if err != nil {
			return domain.HealthCheck{}, err
		}
		hc.HTTP = &domain.HealthCheckHTTP{
			Port: port,
			Path: v.HttpOptions.GetPath(),
		}
	}
	return hc, nil
}

// targetFromPb — конвертер proto Target → domain Target. 4-way identity oneof.
// Validate в domain отлавливает «0 либо 2+ identities заданы», так что здесь
// просто mirror'им proto.
func targetFromPb(pb *lbv1.Target) domain.Target {
	if pb == nil {
		return domain.Target{}
	}
	t := domain.Target{Weight: domain.LbWeight(pb.GetWeight())}
	switch id := pb.GetIdentity().(type) {
	case *lbv1.Target_InstanceId:
		t.InstanceID = option.MustNewOption(domain.InstanceID(id.InstanceId))
	case *lbv1.Target_NicId:
		t.NicID = option.MustNewOption(domain.NicID(id.NicId))
	case *lbv1.Target_IpRef:
		t.IPRef = &domain.TargetIPRef{
			SubnetID: domain.SubnetID(id.IpRef.GetSubnetId()),
			Address:  domain.IPAddress(id.IpRef.GetAddress()),
		}
	case *lbv1.Target_ExternalIp:
		ext := &domain.TargetExternalIP{Address: domain.IPAddress(id.ExternalIp.GetAddress())}
		if z := id.ExternalIp.GetZoneId(); z != "" {
			ext.ZoneID = option.MustNewOption(domain.ZoneID(z))
		}
		t.ExternalIP = ext
	}
	return t
}

// targetsFromPb — конвертер repeated proto.Target → domain.Target.
func targetsFromPb(pbs []*lbv1.Target) []domain.Target {
	if len(pbs) == 0 {
		return nil
	}
	out := make([]domain.Target, 0, len(pbs))
	for _, pb := range pbs {
		out = append(out, targetFromPb(pb))
	}
	return out
}

// tgOutboxPayload — JSON-payload для outbox-emit. Минимальный snapshot (consumer
// делает Get(id) если нужна полная картина). Ключи — из единого источника истины
// kachorepo.LifecyclePayload (тот же набор литералов, что читает Subscribe-consumer).
func tgOutboxPayload(rec *kachorepo.TargetGroupRecord) map[string]any {
	if rec == nil {
		return nil
	}
	return kachorepo.LifecyclePayload{
		ID:        string(rec.ID),
		ProjectID: string(rec.ProjectID),
		RegionID:  string(rec.RegionID),
		Name:      string(rec.Name),
		Status:    string(rec.Status),
	}.Map()
}

// tgMovedPayload — MOVED-event outbox-payload. old_project_id — исходный project
// (canonical-ключ, который Subscribe-consumer читает в
// ResourceLifecycleEvent.OldProjectId для kacho-iam FGA-sync). Единый источник
// имён ключей — kachorepo.LifecyclePayload.
func tgMovedPayload(id, srcProject, dstProject string) map[string]any {
	return kachorepo.LifecyclePayload{
		ID:           id,
		OldProjectID: srcProject,
		NewProjectID: dstProject,
	}.Map()
}

// marshalTargetGroup — anypb.New(TargetGroup) для Operation.Response.
func marshalTargetGroup(rec *kachorepo.TargetGroupRecord) (*anypb.Any, error) {
	pb, err := tgRecordToProto(rec)
	if err != nil {
		return nil, err
	}
	return anypb.New(pb)
}
