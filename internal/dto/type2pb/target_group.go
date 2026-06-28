// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package type2pb

import (
	"fmt"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// targetGroup — трансфер kachorepo.TargetGroupRecord → *lbv1.TargetGroup.
//
// Inline-вставляет Target через target{}.toPb (Targets — embedded child).
// HealthCheck — через healthCheckToPb helper (не registry-transfer; см.
// health_check.go).
type targetGroup struct{}

func (targetGroup) toPb(rec kachorepo.TargetGroupRecord) (*lbv1.TargetGroup, error) {
	ts, err := timeObj{}.toPb(rec.CreatedAt)
	if err != nil {
		return nil, err
	}
	statusPb, err := tgStatusToPb(rec.Status)
	if err != nil {
		return nil, err
	}
	var targetsPb []*lbv1.Target
	if len(rec.Targets) > 0 {
		targetsPb = make([]*lbv1.Target, 0, len(rec.Targets))
		for _, t := range rec.Targets {
			// Wrap domain.Target в TargetRecord для прохода через target{}.toPb
			// (он принимает TargetRecord, чтобы один transfer работал и для
			// inline-targets в TG.Get, и для standalone Target-output из
			// AddTargets-response).
			tr := kachorepo.TargetRecord{Target: t}
			tpb, err := target{}.toPb(tr)
			if err != nil {
				return nil, fmt.Errorf("convert target: %w", err)
			}
			targetsPb = append(targetsPb, tpb)
		}
	}
	return &lbv1.TargetGroup{
		Id:                         string(rec.ID),
		ProjectId:                  string(rec.ProjectID),
		CreatedAt:                  ts,
		Name:                       string(rec.Name),
		Description:                string(rec.Description),
		Labels:                     domain.LabelsToMap(rec.Labels),
		RegionId:                   string(rec.RegionID),
		Targets:                    targetsPb,
		HealthCheck:                healthCheckToPb(rec.HealthCheck),
		DeregistrationDelaySeconds: rec.DeregistrationDelaySeconds,
		SlowStartSeconds:           rec.SlowStartSeconds,
		Status:                     statusPb,
	}, nil
}

func tgStatusToPb(s domain.TargetGroupStatus) (lbv1.TargetGroup_Status, error) {
	switch s {
	case domain.TargetGroupStatusActive:
		return lbv1.TargetGroup_ACTIVE, nil
	case domain.TargetGroupStatusDeleting:
		return lbv1.TargetGroup_DELETING, nil
	}
	return lbv1.TargetGroup_STATUS_UNSPECIFIED, fmt.Errorf("unknown TargetGroupStatus: %q", s)
}

func init() {
	dto.RegTransfer(dto.Fn2Face(targetGroup{}.toPb))
}
