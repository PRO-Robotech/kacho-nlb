// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package type2pb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

func TestNetworkLoadBalancer_Transfer(t *testing.T) {
	created := time.Date(2026, 5, 24, 12, 34, 56, 789, time.UTC)
	rec := kachorepo.LoadBalancerRecord{
		LoadBalancer: domain.LoadBalancer{
			ID:                    "nlb01ABCDEF1234567xx",
			ProjectID:             "prj01ABCDEF1234567ll",
			RegionID:              "ru-central1",
			Name:                  "demo-nlb",
			Description:           "first nlb",
			Labels:                domain.LabelsFromMap(map[string]string{"env": "prod"}),
			Type:                  domain.LBTypeInternal,
			PlacementType:         domain.PlacementRegional,
			DisabledAnnounceZones: []string{"ru-central1-b"},
			Status:                domain.LBStatusActive,
			SessionAffinity:       domain.SessionAffinity5Tuple,
			AddressIDV4:           "adr01ABCDEF1234567v4",
			VipOriginV4:           domain.VipOriginAuto,
			IPFamilies:            []domain.IPVersion{domain.IPVersionV4},
			DeletionProtection:    true,
		},
		CreatedAt: created,
	}
	var pb *lbv1.NetworkLoadBalancer
	require.NoError(t, dto.Transfer(dto.FromTo(rec, &pb)))
	require.NotNil(t, pb)
	assert.Equal(t, "nlb01ABCDEF1234567xx", pb.Id)
	assert.Equal(t, "prj01ABCDEF1234567ll", pb.ProjectId)
	assert.Equal(t, "ru-central1", pb.RegionId)
	assert.Equal(t, "demo-nlb", pb.Name)
	assert.Equal(t, "first nlb", pb.Description)
	assert.Equal(t, map[string]string{"env": "prod"}, pb.Labels)
	assert.Equal(t, lbv1.NetworkLoadBalancer_INTERNAL, pb.Type)
	assert.Equal(t, lbv1.NetworkLoadBalancer_REGIONAL, pb.PlacementType)
	assert.Equal(t, []string{"ru-central1-b"}, pb.DisabledAnnounceZones)
	assert.Equal(t, lbv1.NetworkLoadBalancer_ACTIVE, pb.Status)
	assert.Equal(t, lbv1.NetworkLoadBalancer_FIVE_TUPLE, pb.SessionAffinity)
	assert.Equal(t, "adr01ABCDEF1234567v4", pb.V4AddressId)
	assert.True(t, pb.DeletionProtection)
	// Timestamp — truncate до секунд (по конвенции Kachō).
	assert.Equal(t, created.Truncate(time.Second), pb.CreatedAt.AsTime())
}

// TestNetworkLoadBalancer_SessionAffinityMapping — domain SessionAffinity values
// map to the matching proto enum (1:1 after the proto↔DB alignment).
func TestNetworkLoadBalancer_SessionAffinityMapping(t *testing.T) {
	tests := []struct {
		domain domain.SessionAffinity
		pb     lbv1.NetworkLoadBalancer_SessionAffinity
	}{
		{domain.SessionAffinity5Tuple, lbv1.NetworkLoadBalancer_FIVE_TUPLE},
		{domain.SessionAffinityClientIPOnly, lbv1.NetworkLoadBalancer_CLIENT_IP_ONLY},
	}
	for _, tc := range tests {
		t.Run(string(tc.domain), func(t *testing.T) {
			got, err := lbAffinityToPb(tc.domain)
			require.NoError(t, err)
			assert.Equal(t, tc.pb, got)
		})
	}
}

// TestNetworkLoadBalancer_AddressIDsProjected — the public projection carries the
// resolved bound Address ids (v4_address_id / v6_address_id); source/network/
// vip_origin are NOT echoed.
func TestNetworkLoadBalancer_AddressIDsProjected(t *testing.T) {
	rec := kachorepo.LoadBalancerRecord{
		LoadBalancer: domain.LoadBalancer{
			ID: "nlb1", ProjectID: "p1", RegionID: "r1",
			Type: domain.LBTypeInternal, PlacementType: domain.PlacementRegional,
			Status: domain.LBStatusInactive, SessionAffinity: domain.SessionAffinity5Tuple,
			AddressIDV4: "adr01AAAAAA1234567v4", AddressIDV6: "adr01BBBBBB1234567v6",
			VipOriginV4: domain.VipOriginAuto, VipOriginV6: domain.VipOriginLinked,
			IPFamilies: []domain.IPVersion{domain.IPVersionV4, domain.IPVersionV6},
		},
		CreatedAt: time.Now(),
	}
	var pb *lbv1.NetworkLoadBalancer
	require.NoError(t, dto.Transfer(dto.FromTo(rec, &pb)))
	assert.Equal(t, "adr01AAAAAA1234567v4", pb.GetV4AddressId())
	assert.Equal(t, "adr01BBBBBB1234567v6", pb.GetV6AddressId())
}

// TestNetworkLoadBalancer_PlacementProjected — placement_type + disabled_announce_zones
// projected; EXTERNAL → placement UNSPECIFIED + empty drain.
func TestNetworkLoadBalancer_PlacementProjected(t *testing.T) {
	internal := kachorepo.LoadBalancerRecord{
		LoadBalancer: domain.LoadBalancer{
			ID: "nlb1", ProjectID: "p1", RegionID: "r1",
			Type: domain.LBTypeInternal, PlacementType: domain.PlacementRegional,
			DisabledAnnounceZones: []string{"r1-b"},
			Status:                domain.LBStatusInactive, SessionAffinity: domain.SessionAffinity5Tuple,
		},
		CreatedAt: time.Now(),
	}
	var pbI *lbv1.NetworkLoadBalancer
	require.NoError(t, dto.Transfer(dto.FromTo(internal, &pbI)))
	assert.Equal(t, lbv1.NetworkLoadBalancer_REGIONAL, pbI.GetPlacementType())
	assert.Equal(t, []string{"r1-b"}, pbI.GetDisabledAnnounceZones())

	external := kachorepo.LoadBalancerRecord{
		LoadBalancer: domain.LoadBalancer{
			ID: "nlb2", ProjectID: "p1", RegionID: "r1",
			Type: domain.LBTypeExternal, Status: domain.LBStatusInactive,
			SessionAffinity: domain.SessionAffinity5Tuple,
		},
		CreatedAt: time.Now(),
	}
	var pbE *lbv1.NetworkLoadBalancer
	require.NoError(t, dto.Transfer(dto.FromTo(external, &pbE)))
	assert.Equal(t, lbv1.NetworkLoadBalancer_PLACEMENT_TYPE_UNSPECIFIED, pbE.GetPlacementType())
	assert.Empty(t, pbE.GetDisabledAnnounceZones())
}

func TestNetworkLoadBalancer_StatusMapping(t *testing.T) {
	tests := []struct {
		domain domain.LBStatus
		pb     lbv1.NetworkLoadBalancer_Status
	}{
		{domain.LBStatusCreating, lbv1.NetworkLoadBalancer_CREATING},
		{domain.LBStatusStarting, lbv1.NetworkLoadBalancer_STARTING},
		{domain.LBStatusActive, lbv1.NetworkLoadBalancer_ACTIVE},
		{domain.LBStatusStopping, lbv1.NetworkLoadBalancer_STOPPING},
		{domain.LBStatusStopped, lbv1.NetworkLoadBalancer_STOPPED},
		{domain.LBStatusDeleting, lbv1.NetworkLoadBalancer_DELETING},
		{domain.LBStatusInactive, lbv1.NetworkLoadBalancer_INACTIVE},
	}
	for _, tc := range tests {
		t.Run(string(tc.domain), func(t *testing.T) {
			got, err := lbStatusToPb(tc.domain)
			require.NoError(t, err)
			assert.Equal(t, tc.pb, got)
		})
	}
}

func TestNetworkLoadBalancer_UnknownEnumsFail(t *testing.T) {
	_, err := lbStatusToPb(domain.LBStatus("UNKNOWN"))
	require.Error(t, err)
	_, err = lbTypeToPb(domain.LBType("UNKNOWN"))
	require.Error(t, err)
	_, err = lbAffinityToPb(domain.SessionAffinity("UNKNOWN"))
	require.Error(t, err)
}

func TestNetworkLoadBalancer_NilLabels(t *testing.T) {
	rec := kachorepo.LoadBalancerRecord{
		LoadBalancer: domain.LoadBalancer{
			ID:              "nlb1",
			ProjectID:       "p1",
			RegionID:        "r1",
			Type:            domain.LBTypeInternal,
			Status:          domain.LBStatusInactive,
			SessionAffinity: domain.SessionAffinity5Tuple,
		},
		CreatedAt: time.Now(),
	}
	var pb *lbv1.NetworkLoadBalancer
	require.NoError(t, dto.Transfer(dto.FromTo(rec, &pb)))
	// Empty labels — proto должен иметь nil map (паритет LabelsToMap).
	assert.Nil(t, pb.Labels)
}
