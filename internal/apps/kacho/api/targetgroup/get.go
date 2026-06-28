// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package targetgroup

import (
	"context"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// GetTargetGroupUseCase — sync read одного TG.
//
//	req.TargetGroupId == "" → InvalidArgument
//	repo ErrNotFound        → NotFound (текст ошибки по конвенции Kachō)
type GetTargetGroupUseCase struct {
	repo Repo
}

// NewGetTargetGroupUseCase конструктор.
func NewGetTargetGroupUseCase(repo Repo) *GetTargetGroupUseCase {
	return &GetTargetGroupUseCase{repo: repo}
}

// Execute — open reader → repo.Get → DTO transfer.
func (u *GetTargetGroupUseCase) Execute(
	ctx context.Context, req *lbv1.GetTargetGroupRequest,
) (*lbv1.TargetGroup, error) {
	id := req.GetTargetGroupId()
	if id == "" {
		return nil, errInvalidArg("target_group_id", "required")
	}
	if err := validateTargetGroupID(id); err != nil {
		return nil, err
	}
	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer func() { _ = rd.Close() }()

	rec, err := rd.TargetGroups().Get(ctx, id)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return tgRecordToProto(rec)
}
