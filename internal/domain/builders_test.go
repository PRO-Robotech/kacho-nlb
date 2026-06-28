// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

func TestNewLoadBalancer_Defaults(t *testing.T) {
	t.Parallel()
	lb := domain.NewLoadBalancer(
		"prj-x",
		"ru-central1",
		"edge-public",
		"edge L4",
		domain.LabelsFromMap(map[string]string{"env": "prod"}),
		domain.LBTypeExternal,
	)
	if !strings.HasPrefix(string(lb.ID), ids.PrefixLoadBalancer) {
		t.Fatalf("expected `nlb` id prefix, got %q", lb.ID)
	}
	if lb.Status != domain.LBStatusCreating {
		t.Errorf("expected CREATING, got %q", lb.Status)
	}
	if lb.SessionAffinity != domain.SessionAffinity5Tuple {
		t.Errorf("expected FIVE_TUPLE default, got %q", lb.SessionAffinity)
	}
	if !lb.CrossZoneEnabled {
		t.Error("CrossZoneEnabled default must be true")
	}
	if lb.DeletionProtection {
		t.Error("DeletionProtection default must be false")
	}
	if err := lb.Validate(); err != nil {
		t.Fatalf("built LB should validate: %v", err)
	}
}

func TestNewListener_Defaults(t *testing.T) {
	t.Parallel()
	lb := domain.NewLoadBalancer("prj-x", "ru-central1", "edge-public",
		"", domain.LbLabels{}, domain.LBTypeExternal)
	l := domain.NewListener(lb, "http", domain.ProtoTCP, 80, 8080, domain.IPVersionV4)
	if !strings.HasPrefix(string(l.ID), ids.PrefixListener) {
		t.Fatalf("expected `lst` id prefix, got %q", l.ID)
	}
	if l.RegionID != lb.RegionID {
		t.Errorf("RegionID denorm mismatch: lb=%q lst=%q", lb.RegionID, l.RegionID)
	}
	if l.LoadBalancerID != lb.ID {
		t.Errorf("LoadBalancerID denorm mismatch")
	}
	if l.Status != domain.ListenerStatusCreating {
		t.Errorf("expected CREATING, got %q", l.Status)
	}
	if l.ProxyProtocolV2 {
		t.Error("ProxyProtocolV2 default must be false")
	}
	if err := l.Validate(); err != nil {
		t.Fatalf("built Listener should validate: %v", err)
	}
}

func TestNewTargetGroup_Defaults(t *testing.T) {
	t.Parallel()
	tg := domain.NewTargetGroup("prj-x", "ru-central1", "backend-web",
		"", domain.LbLabels{})
	if !strings.HasPrefix(string(tg.ID), ids.PrefixTargetGroup) {
		t.Fatalf("expected `tgr` id prefix, got %q", tg.ID)
	}
	if tg.DeregistrationDelaySeconds != domain.DefaultDeregistrationDelay {
		t.Errorf("DeregistrationDelay default mismatch")
	}
	if tg.SlowStartSeconds != domain.DefaultSlowStart {
		t.Errorf("SlowStart default mismatch")
	}
	if tg.Status != domain.TargetGroupStatusActive {
		t.Errorf("expected ACTIVE, got %q", tg.Status)
	}
	if tg.Targets != nil {
		t.Error("Targets default must be nil")
	}
	// Note: built TG cannot be Validate'd as-is because HealthCheck is required
	// (no probe set) — caller of NewTargetGroup must attach a HC before Validate.
}

func TestNewDefaultHealthCheck(t *testing.T) {
	t.Parallel()
	for _, proto := range []domain.HealthCheckProto{
		domain.HealthCheckProtoTCP, domain.HealthCheckProtoHTTP,
		domain.HealthCheckProtoHTTPS, domain.HealthCheckProtoGRPC,
	} {
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			hc := domain.NewDefaultHealthCheck("hc-x", proto, 8080)
			if hc.Interval != domain.LbDuration(2*time.Second) {
				t.Errorf("Interval default mismatch")
			}
			if hc.Timeout != domain.LbDuration(time.Second) {
				t.Errorf("Timeout default mismatch")
			}
			if hc.UnhealthyThreshold != domain.DefaultUnhealthyThreshold {
				t.Errorf("UnhealthyThreshold default mismatch")
			}
			if hc.HealthyThreshold != domain.DefaultHealthyThreshold {
				t.Errorf("HealthyThreshold default mismatch")
			}
			if err := hc.Validate(); err != nil {
				t.Fatalf("built HC should validate: %v", err)
			}
		})
	}
}

func TestTruncateID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   domain.ResourceID
		want string
	}{
		{"nlb12345678extra", "nlb12345"},
		{"short", "short"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.in), func(t *testing.T) {
			t.Parallel()
			if got := domain.TruncateID(tc.in); got != tc.want {
				t.Errorf("TruncateID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
