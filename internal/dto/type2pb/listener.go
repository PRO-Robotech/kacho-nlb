package type2pb

import (
	"fmt"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// listener — трансфер kachorepo.ListenerRecord → *lbv1.Listener.
type listener struct{}

func (listener) toPb(rec kachorepo.ListenerRecord) (*lbv1.Listener, error) {
	ts, err := timeObj{}.toPb(rec.CreatedAt)
	if err != nil {
		return nil, err
	}
	protoPb, err := listenerProtocolToPb(rec.Protocol)
	if err != nil {
		return nil, err
	}
	ipVerPb, err := ipVersionToPb(rec.IPVersion)
	if err != nil {
		return nil, err
	}
	statusPb, err := listenerStatusToPb(rec.Status)
	if err != nil {
		return nil, err
	}
	addressID := ""
	if v, ok := rec.AddressID.Maybe(); ok {
		addressID = string(v)
	}
	subnetID := ""
	if v, ok := rec.SubnetID.Maybe(); ok {
		subnetID = string(v)
	}
	defaultTGID := ""
	if v, ok := rec.DefaultTargetGroupID.Maybe(); ok {
		defaultTGID = string(v)
	}
	return &lbv1.Listener{
		Id:                   string(rec.ID),
		ProjectId:            string(rec.ProjectID),
		LoadBalancerId:       string(rec.LoadBalancerID),
		RegionId:             string(rec.RegionID),
		CreatedAt:            ts,
		Name:                 string(rec.Name),
		Description:          string(rec.Description),
		Labels:               domain.LabelsToMap(rec.Labels),
		Protocol:             protoPb,
		Port:                 int64(rec.Port),
		TargetPort:           int64(rec.TargetPort),
		IpVersion:            ipVerPb,
		AddressId:            addressID,
		AllocatedAddress:     string(rec.AllocatedAddress),
		SubnetId:             subnetID,
		ProxyProtocolV2:      rec.ProxyProtocolV2,
		DefaultTargetGroupId: defaultTGID,
		Status:               statusPb,
	}, nil
}

func listenerProtocolToPb(p domain.LbProto) (lbv1.Listener_Protocol, error) {
	switch p {
	case domain.ProtoTCP:
		return lbv1.Listener_TCP, nil
	case domain.ProtoUDP:
		return lbv1.Listener_UDP, nil
	}
	return lbv1.Listener_PROTOCOL_UNSPECIFIED, fmt.Errorf("unknown LbProto: %q", p)
}

func ipVersionToPb(v domain.IPVersion) (lbv1.IpVersion, error) {
	switch v {
	case domain.IPVersionV4:
		return lbv1.IpVersion_IPV4, nil
	case domain.IPVersionV6:
		return lbv1.IpVersion_IPV6, nil
	}
	return lbv1.IpVersion_IP_VERSION_UNSPECIFIED, fmt.Errorf("unknown IPVersion: %q", v)
}

func listenerStatusToPb(s domain.ListenerStatus) (lbv1.Listener_Status, error) {
	switch s {
	case domain.ListenerStatusCreating:
		return lbv1.Listener_CREATING, nil
	case domain.ListenerStatusActive:
		return lbv1.Listener_ACTIVE, nil
	case domain.ListenerStatusUpdating:
		return lbv1.Listener_UPDATING, nil
	case domain.ListenerStatusDeleting:
		return lbv1.Listener_DELETING, nil
	}
	return lbv1.Listener_STATUS_UNSPECIFIED, fmt.Errorf("unknown ListenerStatus: %q", s)
}

func init() {
	dto.RegTransfer(dto.Fn2Face(listener{}.toPb))
}
