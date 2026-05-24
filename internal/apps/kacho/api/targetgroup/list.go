package targetgroup

import (
	"context"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// ListTargetGroupsUseCase — sync list filter by project_id (required) + optional
// `name=` filter (parsed from req.Filter, YC-style equality) + cursor-based
// pagination (acceptance GWT-TGR-016 / GWT-TGR-017).
type ListTargetGroupsUseCase struct {
	repo Repo
}

// NewListTargetGroupsUseCase конструктор.
func NewListTargetGroupsUseCase(repo Repo) *ListTargetGroupsUseCase {
	return &ListTargetGroupsUseCase{repo: repo}
}

// Execute — open reader → repo.List → DTO transfer per row.
func (u *ListTargetGroupsUseCase) Execute(
	ctx context.Context, req *lbv1.ListTargetGroupsRequest,
) (*lbv1.ListTargetGroupsResponse, error) {
	projectID := req.GetProjectId()
	if projectID == "" {
		return nil, errInvalidArg("project_id", "required")
	}
	filter := kachorepo.TargetGroupFilter{
		ProjectID: projectID,
		Filter:    req.GetFilter(),
	}
	if name := parseFilterName(req.GetFilter()); name != "" {
		filter.Name = name
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

// parseFilterName — минимальный YC-style filter parser: понимает `name="<value>"`
// (с кавычками или без). Возвращает "" если name= не задан.
func parseFilterName(filter string) string {
	const prefix1 = `name="`
	const prefix2 = `name=`
	switch {
	case len(filter) > len(prefix1) && filter[:len(prefix1)] == prefix1 &&
		filter[len(filter)-1] == '"':
		return filter[len(prefix1) : len(filter)-1]
	case len(filter) > len(prefix2) && filter[:len(prefix2)] == prefix2:
		return filter[len(prefix2):]
	}
	return ""
}
