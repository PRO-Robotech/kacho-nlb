package listener

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/authzfilter"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// ListUseCase — sync list listeners фильтрованный по `load_balancer_id`
// (acceptance GWT-LST-017). Cursor-based pagination через repo'шный
// `(created_at, id)` token (см. listener_repo.go).
//
// Поддерживаемые фильтры (per proto + design):
//   - load_balancer_id   — required (per proto annotation `(required) = true`)
//   - filter="name=\"…\"" — optional simple name-equality filter (KAC-160 follow-up;
//     текущий парсер минимальный: ожидает ровно `name="<value>"` либо `name='<value>'`;
//     остальные форматы → InvalidArgument).
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
	// KAC-229: project-scoped (parity with NLB/TG List). project_id is required;
	// load_balancer_id is an optional filter (restrict to one parent LB).
	projectID := req.GetProjectId()
	if projectID == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id required")
	}

	name, err := parseNameFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}

	filter := kachorepo.ListenerFilter{
		ProjectID:      projectID,
		LoadBalancerID: req.GetLoadBalancerId(),
		Name:           name,
	}

	// RBAC sub-phase D §11: per-object FGA filter (см. loadbalancer/list.go).
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

// parseNameFilter — поддерживает только `name="<value>"` / `name='<value>'`.
// Пустой filter → пустая name (no filter). Любой другой формат → InvalidArgument
// с verbatim text (`"unsupported filter: <input>"`).
//
// Полноценный парсер (multi-field, AND, IN) — KAC-160 / `kacho-corelib/filter`
// follow-up; не входит в scope этого Wave.
func parseNameFilter(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	const prefix = "name="
	if !strings.HasPrefix(s, prefix) {
		return "", invalidFilterErr(raw)
	}
	v := strings.TrimSpace(s[len(prefix):])
	if len(v) < 2 {
		return "", invalidFilterErr(raw)
	}
	first, last := v[0], v[len(v)-1]
	if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
		return v[1 : len(v)-1], nil
	}
	return "", invalidFilterErr(raw)
}

func invalidFilterErr(raw string) error {
	return status.Error(codes.InvalidArgument, fmt.Sprintf(`unsupported filter: %s (supported: name="<value>")`, raw))
}
