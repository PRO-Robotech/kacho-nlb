package domain_test

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

func TestLbName_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		value   domain.LbName
		wantErr bool
	}{
		// happy paths: acceptance §3 NLB-004 boundary "abc" (3 chars) and 63 chars.
		{"min 3 chars OK", "abc", false},
		{"hyphenated lowercase OK", "edge-public", false},
		{"63 chars OK", domain.LbName("a" + strings.Repeat("b", 61) + "c"), false},
		// negative: NLB-003 (regex), NLB-004 (length/empty).
		{"empty rejected", "", true},
		{"2 chars rejected (regex min len 3)", "ab", true},
		{"64 chars rejected", domain.LbName("a" + strings.Repeat("b", 62) + "c"), true},
		{"uppercase rejected", "Edge", true},
		{"underscore rejected", "edge_public", true},
		{"exclamation rejected", "edge!", true},
		{"starts with digit rejected", "1edge", true},
		{"ends with hyphen rejected", "edge-", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.value.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("LbName(%q).Validate() err=%v wantErr=%v", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestLbDescription_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		value   domain.LbDescription
		wantErr bool
	}{
		{"empty OK", "", false},
		{"normal OK", "edge L4 entry-point", false},
		{"256 chars OK", domain.LbDescription(strings.Repeat("x", 256)), false},
		{"257 chars rejected", domain.LbDescription(strings.Repeat("x", 257)), true},
		// UTF-8 rune count, not byte count.
		{"256 multi-byte runes OK", domain.LbDescription(strings.Repeat("ё", 256)), false},
		{"257 multi-byte runes rejected", domain.LbDescription(strings.Repeat("ё", 257)), true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.value.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("LbDescription.Validate() err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestLbLabelKey_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		value   domain.LbLabelKey
		wantErr bool
	}{
		{"simple OK", "env", false},
		{"with hyphen OK", "a-b", false},
		{"63 chars OK", domain.LbLabelKey("a" + strings.Repeat("b", 62)), false},
		{"empty rejected", "", true},
		{"64 chars rejected", domain.LbLabelKey("a" + strings.Repeat("b", 63)), true},
		{"uppercase rejected", "Env", true},
		{"starts with digit rejected", "1env", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.value.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("LbLabelKey(%q).Validate() err=%v wantErr=%v", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestLbLabelVal_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		value   domain.LbLabelVal
		wantErr bool
	}{
		{"empty OK", "", false},
		{"normal OK", "prod", false},
		{"63 bytes OK", domain.LbLabelVal(strings.Repeat("x", 63)), false},
		{"64 bytes rejected", domain.LbLabelVal(strings.Repeat("x", 64)), true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.value.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("LbLabelVal.Validate() err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateLabels(t *testing.T) {
	t.Parallel()

	t.Run("empty OK", func(t *testing.T) {
		t.Parallel()
		var d domain.LbLabels
		if err := domain.ValidateLabels(d); err != nil {
			t.Fatalf("empty LbLabels: %v", err)
		}
	})

	t.Run("64 pairs OK", func(t *testing.T) {
		t.Parallel()
		var d domain.LbLabels
		for i := 0; i < 64; i++ {
			d.Put(domain.LbLabelKey("k"+itoa(i)), "v")
		}
		if err := domain.ValidateLabels(d); err != nil {
			t.Fatalf("64 pairs: %v", err)
		}
	})

	t.Run("65 pairs rejected", func(t *testing.T) {
		t.Parallel()
		var d domain.LbLabels
		for i := 0; i < 65; i++ {
			d.Put(domain.LbLabelKey("k"+itoa(i)), "v")
		}
		if err := domain.ValidateLabels(d); err == nil {
			t.Fatal("65 pairs: expected error")
		}
	})

	t.Run("bad key rejected", func(t *testing.T) {
		t.Parallel()
		d := domain.LabelsFromMap(map[string]string{"BAD": "v"})
		if err := domain.ValidateLabels(d); err == nil {
			t.Fatal("uppercase key: expected error")
		}
	})

	t.Run("bad value rejected", func(t *testing.T) {
		t.Parallel()
		d := domain.LabelsFromMap(map[string]string{"k": strings.Repeat("x", 64)})
		if err := domain.ValidateLabels(d); err == nil {
			t.Fatal("oversize value: expected error")
		}
	})
}

func TestLbPort_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		value   domain.LbPort
		wantErr bool
	}{
		{"1 OK (lower bound)", 1, false},
		{"65535 OK (upper bound)", 65535, false},
		{"80 OK", 80, false},
		{"0 rejected", 0, true},
		{"-1 rejected", -1, true},
		{"65536 rejected", 65536, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.value.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("LbPort(%d).Validate() err=%v wantErr=%v", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestLbProto_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		value   domain.LbProto
		wantErr bool
	}{
		{"TCP OK", domain.ProtoTCP, false},
		{"UDP OK", domain.ProtoUDP, false},
		{"HTTP rejected", "HTTP", true},
		{"lowercase rejected", "tcp", true},
		{"empty rejected", "", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.value.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("LbProto.Validate() err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestIPVersion_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		value   domain.IPVersion
		wantErr bool
	}{
		{"IPV4 OK", domain.IPVersionV4, false},
		{"IPV6 OK", domain.IPVersionV6, false},
		{"empty rejected", "", true},
		{"v4 rejected (lowercase)", "v4", true},
		{"unknown rejected", "IPV5", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.value.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("IPVersion(%q).Validate() err=%v wantErr=%v", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestIPAddress_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		value   domain.IPAddress
		wantErr bool
	}{
		{"public IPv4 OK", "203.0.113.42", false},
		{"private IPv4 OK", "10.0.0.5", false},
		{"public IPv6 OK", "2001:db8::1", false},
		{"loopback OK (bogon-check is per-target, not per-IP)", "127.0.0.1", false},
		{"empty rejected", "", true},
		{"garbage rejected", "not-an-ip", true},
		{"truncated rejected", "10.0.0", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.value.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("IPAddress(%q).Validate() err=%v wantErr=%v", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestLbWeight_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		value   domain.LbWeight
		wantErr bool
	}{
		{"0 OK (drain-without-remove)", 0, false},
		{"100 OK (default)", 100, false},
		{"1000 OK (upper bound)", 1000, false},
		{"-1 rejected", -1, true},
		{"1001 rejected", 1001, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.value.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("LbWeight(%d).Validate() err=%v wantErr=%v", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestLabelsFromMap_RoundTrip(t *testing.T) {
	t.Parallel()
	in := map[string]string{"env": "prod", "tier": "edge"}
	d := domain.LabelsFromMap(in)
	out := domain.LabelsToMap(d)
	if len(out) != 2 || out["env"] != "prod" || out["tier"] != "edge" {
		t.Fatalf("round-trip mismatch: %v", out)
	}
}

func TestLabelsToMap_EmptyReturnsNil(t *testing.T) {
	t.Parallel()
	var d domain.LbLabels
	if got := domain.LabelsToMap(d); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestLabelsEqual(t *testing.T) {
	t.Parallel()
	a := domain.LabelsFromMap(map[string]string{"env": "prod", "tier": "edge"})
	b := domain.LabelsFromMap(map[string]string{"tier": "edge", "env": "prod"})
	if !domain.LabelsEqual(a, b) {
		t.Fatal("expected equal regardless of insertion order")
	}
	c := domain.LabelsFromMap(map[string]string{"env": "stage"})
	if domain.LabelsEqual(a, c) {
		t.Fatal("expected not equal: different values")
	}
}

// itoa — крошечный strconv-free int→string чтобы не тащить лишний импорт
// в test-helpers.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
