package domain

// TODO(KAC-147): статусные enum'ы согласно design §3.4.
//
//   type LBStatus string
//   const (
//       LBStatusCreating  LBStatus = "CREATING"
//       LBStatusStarting  LBStatus = "STARTING"
//       LBStatusActive    LBStatus = "ACTIVE"
//       LBStatusStopping  LBStatus = "STOPPING"
//       LBStatusStopped   LBStatus = "STOPPED"
//       LBStatusDeleting  LBStatus = "DELETING"
//       LBStatusInactive  LBStatus = "INACTIVE"
//   )
//
//   type ListenerStatus string  // CREATING|ACTIVE|UPDATING|DELETING
//   type TargetGroupStatus string  // ACTIVE|DELETING
//   type TargetStatus string  // INITIAL|HEALTHY|UNHEALTHY|DRAINING|INACTIVE
//   type SessionAffinity string  // FIVE_TUPLE|CLIENT_IP_ONLY
//   type LBType string  // EXTERNAL|INTERNAL
