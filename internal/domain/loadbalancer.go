package domain

import "go.uber.org/multierr"

// LoadBalancer — domain entity NetworkLoadBalancer (design §2.2).
//
// Поля семантически-нагруженных колонок — newtypes со встроенным Validate().
// `CreatedAt` сюда НЕ входит — это DB-managed (DEFAULT now()), живёт в
// repo-сущности (evgeniy §H.1).
type LoadBalancer struct {
	ID                 ResourceID
	ProjectID          ProjectID
	RegionID           RegionID
	Name               LbName
	Description        LbDescription
	Labels             LbLabels
	Type               LBType
	Status             LBStatus
	SessionAffinity    SessionAffinity
	CrossZoneEnabled   bool
	DeletionProtection bool
}

// Validate проверяет все семантически-нагруженные поля LoadBalancer
// (acceptance §3 NLB-003..NLB-005). multierr.Combine агрегирует ошибки;
// nil — если всё валидно.
func (lb LoadBalancer) Validate() error {
	return multierr.Combine(
		lb.Name.Validate(),
		lb.Description.Validate(),
		ValidateLabels(lb.Labels),
		lb.Type.Validate(),
		lb.Status.Validate(),
		lb.SessionAffinity.Validate(),
	)
}

// Equal — deep equality по domain-полям (для noop-detection в Update-flow).
// `CreatedAt` сюда не входит (он в repo-leaf). evgeniy §D.10.
func (lb LoadBalancer) Equal(other LoadBalancer) bool {
	return lb.ID == other.ID &&
		lb.ProjectID == other.ProjectID &&
		lb.RegionID == other.RegionID &&
		lb.Name == other.Name &&
		lb.Description == other.Description &&
		LabelsEqual(lb.Labels, other.Labels) &&
		lb.Type == other.Type &&
		lb.Status == other.Status &&
		lb.SessionAffinity == other.SessionAffinity &&
		lb.CrossZoneEnabled == other.CrossZoneEnabled &&
		lb.DeletionProtection == other.DeletionProtection
}
