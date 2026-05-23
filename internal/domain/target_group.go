package domain

// TargetGroup — domain entity для TargetGroup ресурса.
//
// TODO(KAC-147): структура согласно design §2.2.
//   - ID, ProjectID, RegionID, Name, Description, Labels.
//   - Targets []Target (embedded child).
//   - HealthCheck (JSONB-stored).
//   - DeregistrationDelaySeconds, SlowStartSeconds.
//   - Status (ACTIVE | DELETING).
type TargetGroup struct {
	// TODO(KAC-147): fill in per design §2.2.
}
