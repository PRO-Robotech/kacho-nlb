// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain_test

import (
	"testing"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

func validLB() domain.LoadBalancer {
	return domain.LoadBalancer{
		ID:                 "nlb-x",
		ProjectID:          "prj-x",
		RegionID:           "ru-central1",
		Name:               "edge-public",
		Description:        "edge L4",
		Labels:             domain.LabelsFromMap(map[string]string{"env": "prod"}),
		Type:               domain.LBTypeExternal,
		Status:             domain.LBStatusCreating,
		SessionAffinity:    domain.SessionAffinity5Tuple,
		CrossZoneEnabled:   true,
		DeletionProtection: false,
	}
}

func TestLoadBalancer_Validate_HappyPath(t *testing.T) {
	t.Parallel()
	if err := validLB().Validate(); err != nil {
		t.Fatalf("happy-path: %v", err)
	}
}

func TestLoadBalancer_Validate_PropagatesNameError(t *testing.T) {
	t.Parallel()
	lb := validLB()
	lb.Name = "Edge_Public!"
	if err := lb.Validate(); err == nil {
		t.Fatal("expected error: invalid name regex")
	}
}

func TestLoadBalancer_Validate_PropagatesTypeError(t *testing.T) {
	t.Parallel()
	lb := validLB()
	lb.Type = "PUBLIC"
	if err := lb.Validate(); err == nil {
		t.Fatal("expected error: invalid type")
	}
}

func TestLoadBalancer_Validate_PropagatesSessionAffinityError(t *testing.T) {
	t.Parallel()
	lb := validLB()
	lb.SessionAffinity = "STICKY"
	if err := lb.Validate(); err == nil {
		t.Fatal("expected error: invalid session_affinity")
	}
}

// TestLoadBalancer_Validate_NetworkBinding — cross-field инвариант network_id ↔
// type: INTERNAL требует network_id, EXTERNAL его запрещает.
func TestLoadBalancer_Validate_NetworkBinding(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		lbType    domain.LBType
		networkID domain.NetworkID
		wantErr   bool
	}{
		{"external without network — ok", domain.LBTypeExternal, "", false},
		{"external with network — rejected", domain.LBTypeExternal, "enp-1", true},
		{"internal with network — ok", domain.LBTypeInternal, "enp-1", false},
		{"internal without network — rejected", domain.LBTypeInternal, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lb := validLB()
			lb.Type = tc.lbType
			lb.NetworkID = tc.networkID
			err := lb.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected network-binding error for type=%s network=%q", tc.lbType, tc.networkID)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestLoadBalancer_Validate_SecurityGroupBinding — cross-field инвариант
// security_group_ids ↔ type: непустой набор SG валиден только для INTERNAL
// (SG живут внутри VPC-сети, у EXTERNAL сети нет).
func TestLoadBalancer_Validate_SecurityGroupBinding(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		lbType  domain.LBType
		network domain.NetworkID
		sgs     []domain.SecurityGroupID
		wantErr bool
	}{
		{"external without sg — ok", domain.LBTypeExternal, "", nil, false},
		{"external with sg — rejected", domain.LBTypeExternal, "", []domain.SecurityGroupID{"sgp-1"}, true},
		{"internal with sg — ok", domain.LBTypeInternal, "enp-1", []domain.SecurityGroupID{"sgp-1"}, false},
		{"internal empty sg — ok", domain.LBTypeInternal, "enp-1", nil, false},
		{"internal empty-id sg — rejected", domain.LBTypeInternal, "enp-1", []domain.SecurityGroupID{""}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lb := validLB()
			lb.Type = tc.lbType
			lb.NetworkID = tc.network
			lb.SecurityGroupIDs = tc.sgs
			err := lb.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected sg-binding error for type=%s sgs=%v", tc.lbType, tc.sgs)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestLoadBalancer_Validate_SecurityGroupCardinality — набор SG ограничен сверху
// (raid-protection MaxSecurityGroupsPerLB).
func TestLoadBalancer_Validate_SecurityGroupCardinality(t *testing.T) {
	t.Parallel()
	lb := validLB()
	lb.Type = domain.LBTypeInternal
	lb.NetworkID = "enp-1"
	sgs := make([]domain.SecurityGroupID, domain.MaxSecurityGroupsPerLB+1)
	for i := range sgs {
		sgs[i] = domain.SecurityGroupID("sgp-" + string(rune('a'+i)))
	}
	lb.SecurityGroupIDs = sgs
	if err := lb.Validate(); err == nil {
		t.Fatal("expected cardinality error")
	}
}

func TestLoadBalancer_Equal(t *testing.T) {
	t.Parallel()
	a := validLB()
	b := validLB()
	if !a.Equal(b) {
		t.Fatal("identical LBs should be equal")
	}
	b.Name = "edge-private"
	if a.Equal(b) {
		t.Fatal("differing Name should make them unequal")
	}
	c := validLB()
	c.Type = domain.LBTypeInternal
	c.NetworkID = "enp-1"
	c.SecurityGroupIDs = []domain.SecurityGroupID{"sgp-1"}
	d := validLB()
	d.Type = domain.LBTypeInternal
	d.NetworkID = "enp-1"
	d.SecurityGroupIDs = []domain.SecurityGroupID{"sgp-1"}
	if !c.Equal(d) {
		t.Fatal("identical sg sets should be equal")
	}
	d.SecurityGroupIDs = []domain.SecurityGroupID{"sgp-2"}
	if c.Equal(d) {
		t.Fatal("differing sg set should make them unequal")
	}
}
