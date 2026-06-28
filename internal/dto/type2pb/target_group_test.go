// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package type2pb

import (
	"testing"
	"time"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

func TestTargetGroup_Transfer_WithTargetsAndHC(t *testing.T) {
	hc := domain.HealthCheck{
		Name:               "hc-tcp",
		Interval:           domain.LbDuration(2 * time.Second),
		Timeout:            domain.LbDuration(1 * time.Second),
		UnhealthyThreshold: 3,
		HealthyThreshold:   2,
		TCP:                &domain.HealthCheckTCP{Port: 80},
	}
	rec := kachorepo.TargetGroupRecord{
		TargetGroup: domain.TargetGroup{
			ID:          "tgr01ABCDEF1234567xx",
			ProjectID:   "prj01ABCDEF1234567ll",
			RegionID:    "ru-central1",
			Name:        "my-tg",
			Description: "test tg",
			Labels:      domain.LabelsFromMap(map[string]string{"app": "web"}),
			Targets: []domain.Target{
				{InstanceID: option.MustNewOption(domain.InstanceID("epd0INST1")), Weight: 100},
				{NicID: option.MustNewOption(domain.NicID("e9b0NIC1")), Weight: 50},
			},
			HealthCheck:                hc,
			DeregistrationDelaySeconds: 60,
			SlowStartSeconds:           10,
			Status:                     domain.TargetGroupStatusActive,
		},
		CreatedAt: time.Date(2026, 5, 24, 0, 0, 0, 0, time.UTC),
	}
	var pb *lbv1.TargetGroup
	require.NoError(t, dto.Transfer(dto.FromTo(rec, &pb)))
	require.NotNil(t, pb)
	assert.Equal(t, "tgr01ABCDEF1234567xx", pb.Id)
	assert.Equal(t, lbv1.TargetGroup_ACTIVE, pb.Status)
	require.Len(t, pb.Targets, 2)
	assert.Equal(t, "epd0INST1", pb.Targets[0].GetInstanceId())
	assert.Equal(t, "e9b0NIC1", pb.Targets[1].GetNicId())
	require.NotNil(t, pb.HealthCheck)
	assert.Equal(t, "hc-tcp", pb.HealthCheck.Name)
	require.NotNil(t, pb.HealthCheck.GetTcpOptions())
	assert.Equal(t, int64(80), pb.HealthCheck.GetTcpOptions().Port)
	assert.Equal(t, int64(3), pb.HealthCheck.UnhealthyThreshold)
}

func TestTargetGroup_Transfer_NoTargetsZeroHC(t *testing.T) {
	rec := kachorepo.TargetGroupRecord{
		TargetGroup: domain.TargetGroup{
			ID:                         "tgr01ZERO123456789xx",
			ProjectID:                  "p1",
			RegionID:                   "r1",
			DeregistrationDelaySeconds: 300,
			Status:                     domain.TargetGroupStatusActive,
		},
		CreatedAt: time.Now(),
	}
	var pb *lbv1.TargetGroup
	require.NoError(t, dto.Transfer(dto.FromTo(rec, &pb)))
	assert.Empty(t, pb.Targets)
	assert.Nil(t, pb.HealthCheck, "zero HC → nil proto")
}

func TestTargetGroup_HTTPHealthCheck(t *testing.T) {
	hc := domain.HealthCheck{
		Name:               "hc-http",
		Interval:           domain.LbDuration(5 * time.Second),
		Timeout:            domain.LbDuration(2 * time.Second),
		UnhealthyThreshold: 2,
		HealthyThreshold:   2,
		HTTP:               &domain.HealthCheckHTTP{Port: 8080, Path: "/healthz"},
	}
	pb := healthCheckToPb(hc)
	require.NotNil(t, pb)
	require.NotNil(t, pb.GetHttpOptions())
	assert.Equal(t, int64(8080), pb.GetHttpOptions().Port)
	assert.Equal(t, "/healthz", pb.GetHttpOptions().Path)
	assert.Equal(t, 5*time.Second, pb.Interval.AsDuration())
}

func TestTargetGroup_HTTPSAndGRPCFallback(t *testing.T) {
	// HTTPS / GRPC варианты в proto-VR не существуют — тест фиксирует, что
	// transfer не паникует и возвращает pb без options-oneof (вместо TCP/HTTP).
	hc := domain.HealthCheck{
		Name:               "hc-https",
		Interval:           domain.LbDuration(2 * time.Second),
		Timeout:            domain.LbDuration(1 * time.Second),
		UnhealthyThreshold: 2,
		HealthyThreshold:   2,
		HTTPS:              &domain.HealthCheckHTTPS{Port: 443, Path: "/"},
	}
	pb := healthCheckToPb(hc)
	require.NotNil(t, pb)
	assert.Nil(t, pb.Options, "HTTPS вариант не имеет proto-эквивалента → options nil")
}

func TestTargetGroup_StatusUnknownFail(t *testing.T) {
	_, err := tgStatusToPb(domain.TargetGroupStatus("UNKNOWN"))
	require.Error(t, err)
}
