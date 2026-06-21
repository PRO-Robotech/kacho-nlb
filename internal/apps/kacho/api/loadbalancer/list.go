package loadbalancer

import (
	"context"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/authzfilter"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// ListLoadBalancersUseCase — sync list с фильтром `project_id` (required) +
// optional `name=` (от proto request.Filter — пока поддерживаем точное равенство
// `name`) + cursor-based pagination (acceptance GWT-NLB-009 / GWT-NLB-010).
//
// Filter-grammar (YC-syntax `name="<value>"`) пока не парсится в одном file —
// для прямой совместимости с repo.ListByProject принимаем req.Filter как
// pass-through point (упрощённая семантика — equal-match name; полное парсинг
// перенесём в filter.Parse в Wave 7+ или через дополнительный helper). В тех
// тестах, где filter не передан, используется ListByProject.
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

	filter := kachorepo.LoadBalancerFilter{
		ProjectID: projectID,
		Filter:    req.GetFilter(),
	}
	if name := parseFilterName(req.GetFilter()); name != "" {
		filter.Name = name
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

// parseFilterName — минимальный YC-style filter parser: понимает
// `name="<value>"` (с кавычками или без), возвращает значение либо "".
// Полный grammar (AND-выражения, escaped quotes, multiple fields) — вне
// scope NLB MVP (см. design §3.2 — поддерживаем только `name=`).
func parseFilterName(filter string) string {
	const prefix1 = `name="`
	const prefix2 = `name=`
	switch {
	case len(filter) > len(prefix1) && filter[:len(prefix1)] == prefix1 &&
		filter[len(filter)-1] == '"':
		return filter[len(prefix1) : len(filter)-1]
	case len(filter) > len(prefix2) && filter[:len(prefix2)] == prefix2:
		v := filter[len(prefix2):]
		// strip optional surrounding quotes (covered above).
		return v
	}
	return ""
}
