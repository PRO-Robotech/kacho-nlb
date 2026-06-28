// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain_test

import (
	"testing"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

func TestLBType_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		value   domain.LBType
		wantErr bool
	}{
		{"EXTERNAL OK", domain.LBTypeExternal, false},
		{"INTERNAL OK", domain.LBTypeInternal, false},
		{"empty rejected", "", true},
		{"unknown rejected", "PUBLIC", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.value.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("LBType(%q).Validate() err=%v wantErr=%v", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestLBStatus_Validate(t *testing.T) {
	t.Parallel()
	good := []domain.LBStatus{
		domain.LBStatusCreating, domain.LBStatusStarting, domain.LBStatusActive,
		domain.LBStatusStopping, domain.LBStatusStopped, domain.LBStatusDeleting,
		domain.LBStatusInactive,
	}
	for _, s := range good {
		if err := s.Validate(); err != nil {
			t.Errorf("LBStatus(%q) unexpectedly invalid: %v", s, err)
		}
	}
	bad := []domain.LBStatus{"", "DELETED", "active"}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("LBStatus(%q): expected error", s)
		}
	}
}

func TestSessionAffinity_Validate(t *testing.T) {
	t.Parallel()
	if err := domain.SessionAffinity5Tuple.Validate(); err != nil {
		t.Errorf("FIVE_TUPLE: %v", err)
	}
	if err := domain.SessionAffinityClientIPOnly.Validate(); err != nil {
		t.Errorf("CLIENT_IP_ONLY: %v", err)
	}
	if err := domain.SessionAffinity("UNKNOWN").Validate(); err == nil {
		t.Error("expected error on unknown")
	}
}

func TestListenerStatus_Validate(t *testing.T) {
	t.Parallel()
	good := []domain.ListenerStatus{
		domain.ListenerStatusCreating, domain.ListenerStatusActive,
		domain.ListenerStatusUpdating, domain.ListenerStatusDeleting,
	}
	for _, s := range good {
		if err := s.Validate(); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	if err := domain.ListenerStatus("").Validate(); err == nil {
		t.Error("empty: expected error")
	}
}

func TestTargetGroupStatus_Validate(t *testing.T) {
	t.Parallel()
	if err := domain.TargetGroupStatusActive.Validate(); err != nil {
		t.Errorf("ACTIVE: %v", err)
	}
	if err := domain.TargetGroupStatusDeleting.Validate(); err != nil {
		t.Errorf("DELETING: %v", err)
	}
	if err := domain.TargetGroupStatus("").Validate(); err == nil {
		t.Error("empty: expected error")
	}
}

func TestTargetHealthStatus_Validate(t *testing.T) {
	t.Parallel()
	good := []domain.TargetHealthStatus{
		domain.TargetHealthInitial, domain.TargetHealthHealthy,
		domain.TargetHealthUnhealthy, domain.TargetHealthDraining,
		domain.TargetHealthInactive,
	}
	for _, s := range good {
		if err := s.Validate(); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	if err := domain.TargetHealthStatus("FROZEN").Validate(); err == nil {
		t.Error("unknown: expected error")
	}
}

func TestHealthCheckProto_Validate(t *testing.T) {
	t.Parallel()
	good := []domain.HealthCheckProto{
		domain.HealthCheckProtoTCP, domain.HealthCheckProtoHTTP,
		domain.HealthCheckProtoHTTPS, domain.HealthCheckProtoGRPC,
	}
	for _, p := range good {
		if err := p.Validate(); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
	if err := domain.HealthCheckProto("UDP").Validate(); err == nil {
		t.Error("UDP-as-healthcheck: expected error")
	}
}
