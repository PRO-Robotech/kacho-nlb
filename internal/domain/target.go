package domain

// Target — 4-way identity oneof: instance_id | nic_id | ip_ref | external_ip
// (design §2.5).
//
// TODO(KAC-147):
//   - exactly-one-of validation в Validate().
//   - partial UNIQUE NULLS NOT DISTINCT на DB-уровне (acceptance §0.2).
type Target struct {
	// TODO(KAC-147): fill in per design §2.5.
}
