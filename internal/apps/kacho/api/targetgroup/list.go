// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package targetgroup

import (
	"context"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/shared"
	"github.com/PRO-Robotech/kacho-nlb/internal/authzfilter"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// ListTargetGroupsUseCase — sync list filter by project_id (required) + optional
// `name="<value>"` filter (через общий shared.ParseNameFilter —
// kacho-corelib/filter.Parse, whitelist {"name"}) + cursor-based pagination
// .
type ListTargetGroupsUseCase struct {
	repo  Repo
	authz authzfilter.Filter
}

// NewListTargetGroupsUseCase конструктор. authz может быть nil (disabled / dev).
func NewListTargetGroupsUseCase(repo Repo, authz authzfilter.Filter) *ListTargetGroupsUseCase {
	return &ListTargetGroupsUseCase{repo: repo, authz: authz}
}

// Execute — open reader → repo.List → DTO transfer per row.
//
// RBAC: per-object FGA filter (см. loadbalancer/list.go).
func (u *ListTargetGroupsUseCase) Execute(
	ctx context.Context, req *lbv1.ListTargetGroupsRequest,
) (*lbv1.ListTargetGroupsResponse, error) {
	projectID := req.GetProjectId()
	if projectID == "" {
		return nil, errInvalidArg("project_id", "required")
	}
	name, err := shared.ParseNameFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	filter := kachorepo.TargetGroupFilter{
		ProjectID: projectID,
		Filter:    req.GetFilter(),
		Name:      name,
	}

	dec, err := authzfilter.Resolve(ctx, u.authz,
		authzfilter.ResourceTypeTargetGroup, authzfilter.ActionTargetGroupList)
	if err != nil {
		return nil, err
	}
	if !dec.IsBypass() {
		if dec.IsEmpty() {
			return &lbv1.ListTargetGroupsResponse{}, nil
		}
		filter.AllowedIDs = dec.IDs()
	}

	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer func() { _ = rd.Close() }()

	recs, next, err := rd.TargetGroups().List(ctx, filter, kachorepo.Pagination{
		PageToken: req.GetPageToken(),
		PageSize:  req.GetPageSize(),
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	resp := &lbv1.ListTargetGroupsResponse{NextPageToken: next}
	resp.TargetGroups = make([]*lbv1.TargetGroup, 0, len(recs))
	for _, rec := range recs {
		pb, err := tgRecordToProto(rec)
		if err != nil {
			return nil, err
		}
		resp.TargetGroups = append(resp.TargetGroups, pb)
	}
	return resp, nil
}
