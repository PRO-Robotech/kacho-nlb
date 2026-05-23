package domain

// Listener — domain entity для Listener ресурса.
//
// TODO(KAC-147): структура согласно design §2.2.
//   - ID, ProjectID, LoadBalancerID, RegionID (denorm).
//   - Name, Description, Labels, Protocol, Port, TargetPort, IPVersion.
//   - AddressID (option, BYO), AllocatedAddress, SubnetID (option, required for INTERNAL).
//   - ProxyProtocolV2, DefaultTargetGroupID, Status.
type Listener struct {
	// TODO(KAC-147): fill in per design §2.2.
}
