// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/shared"
	"github.com/PRO-Robotech/kacho-nlb/internal/authzfilter"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// ListUseCase — sync list listeners фильтрованный по `load_balancer_id`
// . Cursor-based pagination через repo'шный
// `(created_at, id)` token (см. listener_repo.go).
//
// Поддерживаемые фильтры (per proto + design):
//   - load_balancer_id   — required (per proto annotation `(required) = true`)
//   - filter=`name="…"`  — optional name-equality filter через общий
//     shared.ParseNameFilter (kacho-corelib/filter.Parse, whitelist {"name"});
//     unknown-поле / unquoted / malformed → InvalidArgument.
type ListUseCase struct {
	repo  RepoFactory
	authz authzfilter.Filter
}

// NewListUseCase — конструктор. authz может быть nil (list-filter disabled / dev).
func NewListUseCase(repo RepoFactory, authz authzfilter.Filter) *ListUseCase {
	return &ListUseCase{repo: repo, authz: authz}
}

// Run выполняет List.
//
// Mapping:
//
//	req.LoadBalancerId == "" → InvalidArgument "load_balancer_id required"
//	repo error               → mapDomainErr (sentinel-aware)
func (u *ListUseCase) Run(ctx context.Context, req *lbv1.ListListenersRequest) (*lbv1.ListListenersResponse, error) {
	// project-scoped (parity with NLB/TG List). project_id is required;
	// load_balancer_id is an optional filter (restrict to one parent LB).
	projectID := req.GetProjectId()
	if projectID == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id required")
	}

	name, err := shared.ParseNameFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}

	filter := kachorepo.ListenerFilter{
		ProjectID:      projectID,
		LoadBalancerID: req.GetLoadBalancerId(),
		Name:           name,
	}

	// RBAC: per-object FGA filter (см. loadbalancer/list.go).
	dec, err := authzfilter.Resolve(ctx, u.authz,
		authzfilter.ResourceTypeListener, authzfilter.ActionListenerList)
	if err != nil {
		return nil, err
	}
	if !dec.IsBypass() {
		if dec.IsEmpty() {
			return &lbv1.ListListenersResponse{}, nil
		}
		filter.AllowedIDs = dec.IDs()
	}

	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer func() { _ = rd.Close() }()

	page, nextToken, err := rd.Listeners().List(ctx,
		filter,
		kachorepo.Pagination{
			PageSize:  req.GetPageSize(),
			PageToken: req.GetPageToken(),
		},
	)
	if err != nil {
		return nil, mapDomainErr(err)
	}

	resp := &lbv1.ListListenersResponse{NextPageToken: nextToken}
	for _, rec := range page {
		pb, err := listenerRecordToPb(rec)
		if err != nil {
			return nil, err
		}
		resp.Listeners = append(resp.Listeners, pb)
	}
	return resp, nil
}
