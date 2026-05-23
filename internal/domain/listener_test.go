package domain_test

import (
	"testing"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

func validListener() domain.Listener {
	return domain.Listener{
		ID:             "lst-x",
		ProjectID:      "prj-x",
		LoadBalancerID: "nlb-x",
		RegionID:       "ru-central1",
		Name:           "http",
		Description:    "",
		Labels:         domain.LbLabels{},
		Protocol:       domain.ProtoTCP,
		Port:           80,
		TargetPort:     8080,
		IPVersion:      domain.IPVersionV4,
		Status:         domain.ListenerStatusCreating,
	}
}

func TestListener_Validate_HappyPath(t *testing.T) {
	t.Parallel()
	if err := validListener().Validate(); err != nil {
		t.Fatalf("happy-path: %v", err)
	}
}

func TestListener_Validate_PortBoundary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		port    domain.LbPort
		wantErr bool
	}{
		{"port=1 OK", 1, false},
		{"port=65535 OK", 65535, false},
		{"port=0 rejected (LST-008)", 0, true},
		{"port=65536 rejected", 65536, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := validListener()
			l.Port = tc.port
			err := l.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestListener_Validate_ProtocolMustBeL4(t *testing.T) {
	t.Parallel()
	l := validListener()
	l.Protocol = "HTTP"
	if err := l.Validate(); err == nil {
		t.Fatal("expected error: HTTP not allowed at L4 listener (LST-009)")
	}
}

func TestListener_Validate_AllocatedAddressOnlyIfSet(t *testing.T) {
	t.Parallel()
	t.Run("empty allocated_address OK on Create", func(t *testing.T) {
		t.Parallel()
		l := validListener()
		l.AllocatedAddress = ""
		if err := l.Validate(); err != nil {
			t.Fatalf("%v", err)
		}
	})
	t.Run("set allocated_address validated", func(t *testing.T) {
		t.Parallel()
		l := validListener()
		l.AllocatedAddress = "203.0.113.42"
		if err := l.Validate(); err != nil {
			t.Fatalf("%v", err)
		}
	})
	t.Run("malformed allocated_address rejected", func(t *testing.T) {
		t.Parallel()
		l := validListener()
		l.AllocatedAddress = "not-ip"
		if err := l.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestListener_Equal_OptionFieldsAware(t *testing.T) {
	t.Parallel()
	a := validListener()
	b := validListener()
	a.AddressID = option.MustNewOption[domain.AddressID]("e9b-addr1")
	b.AddressID = option.MustNewOption[domain.AddressID]("e9b-addr1")
	if !a.Equal(b) {
		t.Fatal("equal AddressID should compare equal")
	}
	b.AddressID = option.MustNewOption[domain.AddressID]("e9b-addr2")
	if a.Equal(b) {
		t.Fatal("differing AddressID must compare unequal")
	}
	c := validListener()
	d := validListener()
	c.SubnetID = option.MustNewOption[domain.SubnetID]("e9b-sub1")
	// d has no SubnetID (some vs none)
	if c.Equal(d) {
		t.Fatal("some-vs-none must compare unequal")
	}
}
