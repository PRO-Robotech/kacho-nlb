package domain_test

import (
	"testing"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

func TestTarget_Validate_IdentityOneOf(t *testing.T) {
	t.Parallel()
	t.Run("no identity rejected (TGR-009)", func(t *testing.T) {
		t.Parallel()
		tg := domain.Target{Weight: 100}
		if err := tg.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("two identities rejected (TGR-010)", func(t *testing.T) {
		t.Parallel()
		tg := domain.Target{
			InstanceID: option.MustNewOption[domain.InstanceID]("epd-x"),
			ExternalIP: &domain.TargetExternalIP{Address: "203.0.113.50"},
			Weight:     100,
		}
		if err := tg.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("only instance_id OK", func(t *testing.T) {
		t.Parallel()
		tg := domain.Target{
			InstanceID: option.MustNewOption[domain.InstanceID]("epd-x"),
			Weight:     100,
		}
		if err := tg.Validate(); err != nil {
			t.Fatalf("%v", err)
		}
	})
	t.Run("only nic_id OK", func(t *testing.T) {
		t.Parallel()
		tg := domain.Target{
			NicID:  option.MustNewOption[domain.NicID]("enp-x"),
			Weight: 0, // drain-without-remove OK
		}
		if err := tg.Validate(); err != nil {
			t.Fatalf("%v", err)
		}
	})
	t.Run("only ip_ref OK", func(t *testing.T) {
		t.Parallel()
		tg := domain.Target{
			IPRef:  &domain.TargetIPRef{SubnetID: "e9b-sub1", Address: "10.0.0.5"},
			Weight: 100,
		}
		if err := tg.Validate(); err != nil {
			t.Fatalf("%v", err)
		}
	})
	t.Run("only external_ip OK", func(t *testing.T) {
		t.Parallel()
		tg := domain.Target{
			ExternalIP: &domain.TargetExternalIP{Address: "203.0.113.99"},
			Weight:     50,
		}
		if err := tg.Validate(); err != nil {
			t.Fatalf("%v", err)
		}
	})
}

func TestTarget_Validate_WeightBoundary(t *testing.T) {
	t.Parallel()
	t.Run("weight=-1 rejected (TGT-005)", func(t *testing.T) {
		t.Parallel()
		tg := domain.Target{
			ExternalIP: &domain.TargetExternalIP{Address: "203.0.113.99"},
			Weight:     -1,
		}
		if err := tg.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("weight=1001 rejected", func(t *testing.T) {
		t.Parallel()
		tg := domain.Target{
			ExternalIP: &domain.TargetExternalIP{Address: "203.0.113.99"},
			Weight:     1001,
		}
		if err := tg.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
}

// TestTarget_ExternalIP_Bogon — acceptance TGR-011 verbatim list +
// public/private IP allowed (design §2.5).
func TestTarget_ExternalIP_Bogon(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		ip      domain.IPAddress
		wantErr bool
	}{
		// allowed
		{"public IPv4 OK", "203.0.113.99", false},
		{"public IPv4 (8.8.8.8) OK", "8.8.8.8", false},
		{"private 10/8 OK (peering)", "10.0.0.5", false},
		{"private 172.16/12 OK", "172.16.0.5", false},
		{"private 192.168/16 OK", "192.168.1.5", false},
		{"public IPv6 OK", "2001:db8::1", false},
		// rejected bogons
		{"loopback 127.0.0.1 rejected", "127.0.0.1", true},
		{"loopback ::1 rejected", "::1", true},
		{"unspecified 0.0.0.0 rejected", "0.0.0.0", true},
		{"unspecified :: rejected", "::", true},
		{"link-local 169.254.x.x rejected", "169.254.1.1", true},
		{"link-local fe80:: rejected", "fe80::1", true},
		{"multicast 224.0.0.1 rejected", "224.0.0.1", true},
		{"multicast ff00:: rejected", "ff00::1", true},
		{"broadcast 255.255.255.255 rejected", "255.255.255.255", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tg := domain.Target{
				ExternalIP: &domain.TargetExternalIP{Address: tc.ip},
				Weight:     100,
			}
			err := tg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("ip=%q err=%v wantErr=%v", tc.ip, err, tc.wantErr)
			}
		})
	}
}

func TestTargetIPRef_Validate(t *testing.T) {
	t.Parallel()
	t.Run("empty subnet_id rejected", func(t *testing.T) {
		t.Parallel()
		r := domain.TargetIPRef{Address: "10.0.0.5"}
		if err := r.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("malformed address rejected", func(t *testing.T) {
		t.Parallel()
		r := domain.TargetIPRef{SubnetID: "e9b-x", Address: "not-ip"}
		if err := r.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestTargetExternalIP_Validate_Address(t *testing.T) {
	t.Parallel()
	t.Run("malformed rejected", func(t *testing.T) {
		t.Parallel()
		e := domain.TargetExternalIP{Address: "bogus"}
		if err := e.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("empty rejected", func(t *testing.T) {
		t.Parallel()
		e := domain.TargetExternalIP{Address: ""}
		if err := e.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
}
