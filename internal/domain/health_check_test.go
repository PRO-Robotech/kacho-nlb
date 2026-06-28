// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain_test

import (
	"testing"
	"time"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

func validHC() domain.HealthCheck {
	return domain.HealthCheck{
		Name:               "hc-ok",
		Interval:           domain.LbDuration(2 * time.Second),
		Timeout:            domain.LbDuration(1 * time.Second),
		UnhealthyThreshold: 2,
		HealthyThreshold:   2,
		TCP:                &domain.HealthCheckTCP{Port: 8080},
	}
}

func TestHealthCheck_Validate_HappyPath(t *testing.T) {
	t.Parallel()
	if err := validHC().Validate(); err != nil {
		t.Fatalf("happy-path: %v", err)
	}
}

func TestHealthCheck_Validate_OneOfProbe(t *testing.T) {
	t.Parallel()
	t.Run("no probe rejected (TGR-004)", func(t *testing.T) {
		t.Parallel()
		hc := validHC()
		hc.TCP = nil
		if err := hc.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("two probes rejected (TGR-003)", func(t *testing.T) {
		t.Parallel()
		hc := validHC()
		hc.HTTP = &domain.HealthCheckHTTP{Port: 8080, Path: "/"}
		if err := hc.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("HTTP probe OK", func(t *testing.T) {
		t.Parallel()
		hc := validHC()
		hc.TCP = nil
		hc.HTTP = &domain.HealthCheckHTTP{Port: 8080, Path: "/healthz"}
		if err := hc.Validate(); err != nil {
			t.Fatalf("HTTP probe: %v", err)
		}
	})
	t.Run("HTTPS probe OK", func(t *testing.T) {
		t.Parallel()
		hc := validHC()
		hc.TCP = nil
		hc.HTTPS = &domain.HealthCheckHTTPS{Port: 8443, Path: "/healthz"}
		if err := hc.Validate(); err != nil {
			t.Fatalf("HTTPS probe: %v", err)
		}
	})
	t.Run("GRPC probe OK", func(t *testing.T) {
		t.Parallel()
		hc := validHC()
		hc.TCP = nil
		hc.GRPC = &domain.HealthCheckGRPC{Port: 9090, ServiceName: "svc"}
		if err := hc.Validate(); err != nil {
			t.Fatalf("GRPC probe: %v", err)
		}
	})
	t.Run("probe port=0 rejected", func(t *testing.T) {
		t.Parallel()
		hc := validHC()
		hc.TCP = &domain.HealthCheckTCP{Port: 0}
		if err := hc.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestHealthCheck_Validate_Interval(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		interval domain.LbDuration
		wantErr  bool
	}{
		{"1s OK (lower bound)", domain.LbDuration(time.Second), false},
		{"2s OK", domain.LbDuration(2 * time.Second), false},
		{"600s OK (upper bound)", domain.LbDuration(600 * time.Second), false},
		{"0s rejected (TGR-005)", 0, true},
		{"601s rejected (TGR-005)", domain.LbDuration(601 * time.Second), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hc := validHC()
			hc.Interval = tc.interval
			// timeout не должен превышать interval — выставим минимум.
			hc.Timeout = domain.LbDuration(time.Millisecond)
			err := hc.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestHealthCheck_Validate_Timeout(t *testing.T) {
	t.Parallel()
	t.Run("timeout > interval rejected", func(t *testing.T) {
		t.Parallel()
		hc := validHC()
		hc.Interval = domain.LbDuration(time.Second)
		hc.Timeout = domain.LbDuration(2 * time.Second)
		if err := hc.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("timeout == interval OK", func(t *testing.T) {
		t.Parallel()
		hc := validHC()
		hc.Interval = domain.LbDuration(2 * time.Second)
		hc.Timeout = domain.LbDuration(2 * time.Second)
		if err := hc.Validate(); err != nil {
			t.Fatalf("timeout==interval: %v", err)
		}
	})
	t.Run("zero timeout rejected", func(t *testing.T) {
		t.Parallel()
		hc := validHC()
		hc.Timeout = 0
		if err := hc.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestHealthCheck_Validate_Thresholds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name               string
		unhealthy, healthy int32
		wantErr            bool
	}{
		{"2,2 OK (lower bound)", 2, 2, false},
		{"10,10 OK (upper bound)", 10, 10, false},
		{"unhealthy=1 rejected (TGR-006)", 1, 2, true},
		{"unhealthy=11 rejected", 11, 2, true},
		{"healthy=1 rejected", 2, 1, true},
		{"healthy=11 rejected", 2, 11, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hc := validHC()
			hc.UnhealthyThreshold = tc.unhealthy
			hc.HealthyThreshold = tc.healthy
			err := hc.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
