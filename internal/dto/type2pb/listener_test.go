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

func TestListener_Transfer(t *testing.T) {
	rec := kachorepo.ListenerRecord{
		Listener: domain.Listener{
			ID:               "lst01ABCDEF1234567xx",
			ProjectID:        "prj01ABCDEF1234567ll",
			LoadBalancerID:   "nlb01ABCDEF1234567xx",
			RegionID:         "ru-central1",
			Name:             "ext-lst",
			Description:      "ext listener",
			Labels:           domain.LabelsFromMap(map[string]string{"role": "edge"}),
			Protocol:         domain.ProtoTCP,
			Port:             443,
			TargetPort:       8443,
			IPVersion:        domain.IPVersionV4,
			AddressID:        option.MustNewOption(domain.AddressID("e9b01ADDRESS")),
			AllocatedAddress: "203.0.113.10",
			SubnetID:         option.ValueOf[domain.SubnetID]{},
			ProxyProtocolV2:  true,
			Status:           domain.ListenerStatusActive,
		},
		CreatedAt: time.Date(2026, 5, 24, 1, 2, 3, 0, time.UTC),
	}
	var pb *lbv1.Listener
	require.NoError(t, dto.Transfer(dto.FromTo(rec, &pb)))
	require.NotNil(t, pb)
	assert.Equal(t, "lst01ABCDEF1234567xx", pb.Id)
	assert.Equal(t, "nlb01ABCDEF1234567xx", pb.LoadBalancerId)
	assert.Equal(t, lbv1.Listener_TCP, pb.Protocol)
	assert.Equal(t, int64(443), pb.Port)
	assert.Equal(t, int64(8443), pb.TargetPort)
	assert.Equal(t, lbv1.IpVersion_IPV4, pb.IpVersion)
	assert.Equal(t, "e9b01ADDRESS", pb.AddressId)
	assert.Equal(t, "203.0.113.10", pb.AllocatedAddress)
	assert.Empty(t, pb.SubnetId, "SubnetID None → empty string in proto")
	assert.True(t, pb.ProxyProtocolV2)
	assert.Equal(t, lbv1.Listener_ACTIVE, pb.Status)
}

func TestListener_StatusMapping(t *testing.T) {
	tests := []struct {
		domain domain.ListenerStatus
		pb     lbv1.Listener_Status
	}{
		{domain.ListenerStatusCreating, lbv1.Listener_CREATING},
		{domain.ListenerStatusActive, lbv1.Listener_ACTIVE},
		{domain.ListenerStatusUpdating, lbv1.Listener_UPDATING},
		{domain.ListenerStatusDeleting, lbv1.Listener_DELETING},
	}
	for _, tc := range tests {
		got, err := listenerStatusToPb(tc.domain)
		require.NoError(t, err)
		assert.Equal(t, tc.pb, got, "for %s", tc.domain)
	}
}

func TestListener_ProtocolAndIPVersionUnknownFail(t *testing.T) {
	_, err := listenerProtocolToPb(domain.LbProto("HTTP"))
	require.Error(t, err)
	_, err = ipVersionToPb(domain.IPVersion("V42"))
	require.Error(t, err)
	_, err = listenerStatusToPb(domain.ListenerStatus("UNKNOWN"))
	require.Error(t, err)
}
