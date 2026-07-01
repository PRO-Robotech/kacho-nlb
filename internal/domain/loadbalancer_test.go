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

// TestLoadBalancer_Validate_PlacementType — placement пустой либо ZONAL/REGIONAL;
// прочее отвергается. Coupling placement с type проверяется в use-case (не здесь).
func TestLoadBalancer_Validate_PlacementType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		placement domain.PlacementType
		wantErr   bool
	}{
		{"empty ok", domain.PlacementUnspecified, false},
		{"zonal ok", domain.PlacementZonal, false},
		{"regional ok", domain.PlacementRegional, false},
		{"garbage rejected", "SOMEWHERE", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lb := validLB()
			lb.Type = domain.LBTypeInternal
			lb.PlacementType = tc.placement
			err := lb.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected placement error for %q", tc.placement)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
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
	c.PlacementType = domain.PlacementRegional
	c.DisabledAnnounceZones = []string{"ru-central1-b"}
	d := validLB()
	d.Type = domain.LBTypeInternal
	d.PlacementType = domain.PlacementRegional
	d.DisabledAnnounceZones = []string{"ru-central1-b"}
	if !c.Equal(d) {
		t.Fatal("identical placement + drain sets should be equal")
	}
	d.DisabledAnnounceZones = []string{"ru-central1-a"}
	if c.Equal(d) {
		t.Fatal("differing drain set should make them unequal")
	}
}
