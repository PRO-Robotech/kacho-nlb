package domain

import (
	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
	"go.uber.org/multierr"
)

// TargetGroup — domain entity TargetGroup (design §2.2).
//
// Targets — embedded child (физически живут в отдельной таблице `targets` с
// FK ON DELETE RESTRICT, но domain-модель удобнее держать flat для Validate
// и use-case-операций AddTargets/RemoveTargets).
//
// HealthCheck сериализуется JSONB-колонкой; embedded — потому что у TG ровно
// один HC (design §2.2).
type TargetGroup struct {
	ID                         ResourceID
	ProjectID                  ProjectID
	RegionID                   RegionID
	Name                       LbName
	Description                LbDescription
	Labels                     LbLabels
	Targets                    []Target
	HealthCheck                HealthCheck
	DeregistrationDelaySeconds int32
	SlowStartSeconds           int32
	Status                     TargetGroupStatus
}

// Validate — все семантически-нагруженные поля + cardinality лимит + bound checks.
// Покрывает acceptance TGR-005..TGR-008.
func (tg TargetGroup) Validate() error {
	deregErr := error(nil)
	if tg.DeregistrationDelaySeconds < DeregistrationDelayMin ||
		tg.DeregistrationDelaySeconds > DeregistrationDelayMax {
		deregErr = coreerrors.InvalidArgument().
			AddFieldViolation("deregistration_delay_seconds",
				"deregistration_delay_seconds must be in range [0, 3600]").
			Err()
	}
	slowErr := error(nil)
	if tg.SlowStartSeconds < SlowStartMin || tg.SlowStartSeconds > SlowStartMax {
		slowErr = coreerrors.InvalidArgument().
			AddFieldViolation("slow_start_seconds",
				"slow_start_seconds must be in range [0, 900]").
			Err()
	}
	cardErr := error(nil)
	if len(tg.Targets) > MaxTargetsPerGroup {
		cardErr = coreerrors.InvalidArgument().
			AddFieldViolation("targets",
				"too many targets (max 100)").
			Err()
	}

	// Per-target Validate. Останавливаемся на первой проблеме (early-exit)
	// — иначе error-message раздуется до 100*N FieldViolations.
	var perTargetErr error
	for i := range tg.Targets {
		if err := tg.Targets[i].Validate(); err != nil {
			perTargetErr = err
			break
		}
	}

	return multierr.Combine(
		tg.Name.Validate(),
		tg.Description.Validate(),
		ValidateLabels(tg.Labels),
		tg.Status.Validate(),
		tg.HealthCheck.Validate(),
		deregErr,
		slowErr,
		cardErr,
		perTargetErr,
	)
}
