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
}
