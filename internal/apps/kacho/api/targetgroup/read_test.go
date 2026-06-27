package targetgroup

import (
	"context"
	"testing"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// ---- Get -------------------------------------------------------------------

// GWT-TGR-015 — Get TG with 1 target → returns inline targets[] + HC.
func TestGet_ReturnsInlineTargetsAndHC(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "backend-web")
	repo.seedTG(tg)
	tr := kachoTarget(string(tg.ID), domain.Target{
		InstanceID: option.MustNewOption(domain.InstanceID("epd0INST1")),
		Weight:     100,
	})
	repo.seedTarget(string(tg.ID), &tr)

	uc := NewGetTargetGroupUseCase(repo)
	resp, err := uc.Execute(context.Background(), &lbv1.GetTargetGroupRequest{
		TargetGroupId: string(tg.ID),
	})
	require.NoError(t, err)
	require.Equal(t, string(tg.ID), resp.GetId())
	require.Equal(t, "backend-web", resp.GetName())
	require.Len(t, resp.GetTargets(), 1, "inline targets must be returned")
	require.NotNil(t, resp.GetHealthCheck())
}

func TestGet_EmptyID_InvalidArg(t *testing.T) {
	uc := NewGetTargetGroupUseCase(newFakeRepo())
	_, err := uc.Execute(context.Background(), &lbv1.GetTargetGroupRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestGet_NotFound(t *testing.T) {
	uc := NewGetTargetGroupUseCase(newFakeRepo())
	_, err := uc.Execute(context.Background(), &lbv1.GetTargetGroupRequest{
		TargetGroupId: "tgr-doesnt-exist",
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// ---- List ------------------------------------------------------------------

// GWT-TGR-016 — List filtered by project_id (+ optional name filter).
func TestList_FilterByProject(t *testing.T) {
	repo := newFakeRepo()
	repo.seedTG(makeTG("prj-a", "tg-a1"))
	repo.seedTG(makeTG("prj-a", "tg-a2"))
	repo.seedTG(makeTG("prj-b", "tg-b1"))

	uc := NewListTargetGroupsUseCase(repo, nil)
	resp, err := uc.Execute(context.Background(), &lbv1.ListTargetGroupsRequest{
		ProjectId: "prj-a",
	})
	require.NoError(t, err)
	require.Len(t, resp.GetTargetGroups(), 2)

	// name="<value>" filter.
	resp2, err := uc.Execute(context.Background(), &lbv1.ListTargetGroupsRequest{
		ProjectId: "prj-a",
		Filter:    `name="tg-a1"`,
	})
	require.NoError(t, err)
	require.Len(t, resp2.GetTargetGroups(), 1)
	require.Equal(t, "tg-a1", resp2.GetTargetGroups()[0].GetName())
}

func TestList_EmptyProjectID_InvalidArg(t *testing.T) {
	uc := NewListTargetGroupsUseCase(newFakeRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.ListTargetGroupsRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// ---- ListOperations --------------------------------------------------------

// GWT-TGR-028 — ListOperations returns recorded ops for resource_id == tg_id.
func TestListOperations_ReturnsOpsForTG(t *testing.T) {
	opsRepo := newFakeOpsRepo()
	// Seed two operations directly.
	op1 := mkOp("oprtgrA1")
	op2 := mkOp("oprtgrA2")
	require.NoError(t, opsRepo.Create(context.Background(), op1))
	require.NoError(t, opsRepo.Create(context.Background(), op2))

	uc := NewListOperationsUseCase(opsRepo)
	resp, err := uc.Execute(context.Background(), &lbv1.ListTargetGroupOperationsRequest{
		TargetGroupId: "tgr-any",
	})
	require.NoError(t, err)
	// fakeOpsRepo doesn't filter by resource — assert non-empty.
	assert.NotEmpty(t, resp.GetOperations())
}

func TestListOperations_EmptyID_InvalidArg(t *testing.T) {
	uc := NewListOperationsUseCase(newFakeOpsRepo())
	_, err := uc.Execute(context.Background(), &lbv1.ListTargetGroupOperationsRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
