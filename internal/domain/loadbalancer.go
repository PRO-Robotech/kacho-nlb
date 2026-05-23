package domain

// LoadBalancer — domain entity для NetworkLoadBalancer ресурса.
//
// TODO(KAC-147): структура полей согласно design §2.2 (Section 2.2).
//   - ID, ProjectID, RegionID, Name, Description, Labels.
//   - Type (EXTERNAL|INTERNAL), Status, SessionAffinity.
//   - CrossZoneEnabled (default true), DeletionProtection.
//   - метод Validate() через multierr.Combine.
//   - builder NewLoadBalancer(...).
type LoadBalancer struct {
	// TODO(KAC-147): fill in per design §2.2.
}
