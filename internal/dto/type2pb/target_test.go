package type2pb

import (
	"testing"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

func TestTarget_4WayIdentityOneOf(t *testing.T) {
	tests := []struct {
		name        string
		rec         kachorepo.TargetRecord
		expectClass any
		assertion   func(t *testing.T, pb *lbv1.Target)
	}{
		{
			name: "instance_id",
			rec: kachorepo.TargetRecord{
				Target: domain.Target{
					InstanceID: option.MustNewOption(domain.InstanceID("epd0I1")),
					Weight:     100,
				},
			},
			assertion: func(t *testing.T, pb *lbv1.Target) {
				_, ok := pb.Identity.(*lbv1.Target_InstanceId)
				assert.True(t, ok, "identity is InstanceId")
				assert.Equal(t, "epd0I1", pb.GetInstanceId())
			},
		},
		{
			name: "nic_id",
			rec: kachorepo.TargetRecord{
				Target: domain.Target{
					NicID:  option.MustNewOption(domain.NicID("e9b0NIC")),
					Weight: 200,
				},
			},
			assertion: func(t *testing.T, pb *lbv1.Target) {
				_, ok := pb.Identity.(*lbv1.Target_NicId)
				assert.True(t, ok)
				assert.Equal(t, "e9b0NIC", pb.GetNicId())
			},
		},
		{
			name: "ip_ref",
			rec: kachorepo.TargetRecord{
				Target: domain.Target{
					IPRef: &domain.TargetIPRef{
						SubnetID: "e9b0SUB",
						Address:  "10.0.0.5",
					},
					Weight: 50,
				},
			},
			assertion: func(t *testing.T, pb *lbv1.Target) {
				r, ok := pb.Identity.(*lbv1.Target_IpRef)
				require.True(t, ok)
				assert.Equal(t, "e9b0SUB", r.IpRef.SubnetId)
				assert.Equal(t, "10.0.0.5", r.IpRef.Address)
			},
		},
		{
			name: "external_ip_with_zone",
			rec: kachorepo.TargetRecord{
				Target: domain.Target{
					ExternalIP: &domain.TargetExternalIP{
						Address: "203.0.113.99",
						ZoneID:  option.MustNewOption(domain.ZoneID("ru-central1-a")),
					},
					Weight: 75,
				},
			},
			assertion: func(t *testing.T, pb *lbv1.Target) {
				e, ok := pb.Identity.(*lbv1.Target_ExternalIp)
				require.True(t, ok)
				assert.Equal(t, "203.0.113.99", e.ExternalIp.Address)
				assert.Equal(t, "ru-central1-a", e.ExternalIp.ZoneId)
			},
		},
		{
			name: "external_ip_no_zone",
			rec: kachorepo.TargetRecord{
				Target: domain.Target{
					ExternalIP: &domain.TargetExternalIP{Address: "198.51.100.5"},
					Weight:     10,
				},
			},
			assertion: func(t *testing.T, pb *lbv1.Target) {
				e, ok := pb.Identity.(*lbv1.Target_ExternalIp)
				require.True(t, ok)
				assert.Equal(t, "198.51.100.5", e.ExternalIp.Address)
				assert.Empty(t, e.ExternalIp.ZoneId)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var pb *lbv1.Target
			require.NoError(t, dto.Transfer(dto.FromTo(tc.rec, &pb)))
			require.NotNil(t, pb)
			assert.Equal(t, int32(tc.rec.Weight), pb.Weight)
			tc.assertion(t, pb)
		})
	}
}
