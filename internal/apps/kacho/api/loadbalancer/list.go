package loadbalancer

import (
	"context"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/shared"
	"github.com/PRO-Robotech/kacho-nlb/internal/authzfilter"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// ListLoadBalancersUseCase — sync list с фильтром `project_id` (required) +
// optional `name="<value>"` (от proto request.Filter, через общий
// shared.ParseNameFilter — kacho-corelib/filter.Parse, whitelist {"name"}) +
// cursor-based pagination (acceptance GWT-NLB-009 / GWT-NLB-010).
type ListLoadBalancersUseCase struct {
	repo  Repo
	authz authzfilter.Filter
}

// NewListLoadBalancersUseCase конструктор. authz может быть nil
// (list-filter disabled / dev) → нефильтрованный project-scoped passthrough.
func NewListLoadBalancersUseCase(repo Repo, authz authzfilter.Filter) *ListLoadBalancersUseCase {
	return &ListLoadBalancersUseCase{repo: repo, authz: authz}
}

// Execute — open reader, repo.List, DTO transfer per row.
//
// RBAC sub-phase D §11: per-object FGA filter. subject из ctx → iam ListObjects
// (relation viewer) → пересечение в SQL (filter.AllowedIDs), pagination ПОСЛЕ
// фильтра (D-46). Пустой грант → пустой ответ (no-leak). iam недоступен →
// Unavailable (fail-closed, D-47).
func (u *ListLoadBalancersUseCase) Execute(
	ctx context.Context, req *lbv1.ListNetworkLoadBalancersRequest,
) (*lbv1.ListNetworkLoadBalancersResponse, error) {
	projectID := req.GetProjectId()
	if projectID == "" {
		return nil, errInvalidArg("project_id", "required")
	}

	name, err := shared.ParseNameFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	filter := kachorepo.LoadBalancerFilter{
		ProjectID: projectID,
		Filter:    req.GetFilter(),
		Name:      name,
	}

	dec, err := authzfilter.Resolve(ctx, u.authz,
		authzfilter.ResourceTypeLoadBalancer, authzfilter.ActionLoadBalancerList)
	if err != nil {
		return nil, err
	}
	if !dec.IsBypass() {
		if dec.IsEmpty() {
			return &lbv1.ListNetworkLoadBalancersResponse{}, nil
		}
		filter.AllowedIDs = dec.IDs()
	}

	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer func() { _ = rd.Close() }()

	recs, next, err := rd.LoadBalancers().List(ctx, filter, kachorepo.Pagination{
		PageToken: req.GetPageToken(),
		PageSize:  req.GetPageSize(),
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}

	resp := &lbv1.ListNetworkLoadBalancersResponse{NextPageToken: next}
	resp.NetworkLoadBalancers = make([]*lbv1.NetworkLoadBalancer, 0, len(recs))
	for _, rec := range recs {
		pb, err := lbRecordToProto(rec)
		if err != nil {
			return nil, err
		}
		resp.NetworkLoadBalancers = append(resp.NetworkLoadBalancers, pb)
	}
	return resp, nil
}
