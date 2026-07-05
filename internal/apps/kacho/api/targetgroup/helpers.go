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
// ошибку «health_check is required» вверх.
func healthCheckFromPb(pb *lbv1.HealthCheck) domain.HealthCheck {
	if pb == nil {
		return domain.HealthCheck{}
	}
	hc := domain.HealthCheck{
		Name:               domain.LbName(pb.GetName()),
		Interval:           domain.LbDuration(pb.GetInterval().AsDuration()),
		Timeout:            domain.LbDuration(pb.GetTimeout().AsDuration()),
		UnhealthyThreshold: int32(pb.GetUnhealthyThreshold()),
		HealthyThreshold:   int32(pb.GetHealthyThreshold()),
	}
	switch v := pb.GetOptions().(type) {
	case *lbv1.HealthCheck_TcpOptions_:
		hc.TCP = &domain.HealthCheckTCP{Port: domain.LbPort(v.TcpOptions.GetPort())}
	case *lbv1.HealthCheck_HttpOptions_:
		hc.HTTP = &domain.HealthCheckHTTP{
			Port: domain.LbPort(v.HttpOptions.GetPort()),
			Path: v.HttpOptions.GetPath(),
		}
	}
	return hc
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
// делает Get(id) если нужна полная картина).
func tgOutboxPayload(rec *kachorepo.TargetGroupRecord) map[string]any {
	if rec == nil {
		return nil
	}
	return map[string]any{
		"id":         string(rec.ID),
		"project_id": string(rec.ProjectID),
		"region_id":  string(rec.RegionID),
		"name":       string(rec.Name),
		"status":     string(rec.Status),
	}
}

// marshalTargetGroup — anypb.New(TargetGroup) для Operation.Response.
func marshalTargetGroup(rec *kachorepo.TargetGroupRecord) (*anypb.Any, error) {
	pb, err := tgRecordToProto(rec)
	if err != nil {
		return nil, err
	}
	return anypb.New(pb)
}
