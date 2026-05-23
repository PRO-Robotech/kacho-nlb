package type2pb

import (
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// target — трансфер kachorepo.TargetRecord → *lbv1.Target. 4-way oneof identity
// (InstanceID / NicID / IpRef / ExternalIp).
type target struct{}

func (target) toPb(rec kachorepo.TargetRecord) (*lbv1.Target, error) {
	out := &lbv1.Target{Weight: int32(rec.Weight)}
	switch {
	case func() bool { _, ok := rec.InstanceID.Maybe(); return ok }():
		v, _ := rec.InstanceID.Maybe()
		out.Identity = &lbv1.Target_InstanceId{InstanceId: string(v)}
	case func() bool { _, ok := rec.NicID.Maybe(); return ok }():
		v, _ := rec.NicID.Maybe()
		out.Identity = &lbv1.Target_NicId{NicId: string(v)}
	case rec.IPRef != nil:
		out.Identity = &lbv1.Target_IpRef{
			IpRef: &lbv1.Target_InCloudIP{
				SubnetId: string(rec.IPRef.SubnetID),
				Address:  string(rec.IPRef.Address),
			},
		}
	case rec.ExternalIP != nil:
		ext := &lbv1.Target_ExternalIP{Address: string(rec.ExternalIP.Address)}
		if v, ok := rec.ExternalIP.ZoneID.Maybe(); ok {
			ext.ZoneId = string(v)
		}
		out.Identity = &lbv1.Target_ExternalIp{ExternalIp: ext}
	}
	return out, nil
}

func init() {
	dto.RegTransfer(dto.Fn2Face(target{}.toPb))
}
