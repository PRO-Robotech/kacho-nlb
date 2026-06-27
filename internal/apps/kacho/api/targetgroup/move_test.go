package targetgroup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// GWT-TGR-025 — Move OK (no attached LB).
func TestMove_Happy(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-src", "movable")
	repo.seedTG(tg)
	opsRepo := newFakeOpsRepo()
	uc := NewMoveTargetGroupUseCase(repo, opsRepo, &fakeProjectClient{}, nil)

	op, err := uc.Execute(context.Background(), &lbv1.MoveTargetGroupRequest{
		TargetGroupId:        string(tg.ID),
		DestinationProjectId: "prj-dst",
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)

	events := repo.outboxEvents()
	// MOVED + UPDATED
	require.Len(t, events, 2)
	assert.Equal(t, kachopg.OutboxActionMoved, events[0].Action)
	assert.Equal(t, kachopg.OutboxActionUpdated, events[1].Action)

	// SEC-D: project-rewrite = register(dst) + unregister(src) intents in writer-tx.
	require.Len(t, repo.fga, 2)
	assert.Equal(t, domain.FGAEventRegister, repo.fga[0].EventType)
	assert.Equal(t, "project:prj-dst", repo.fga[0].Intent.Tuples[0].SubjectID)
	assert.Equal(t, domain.FGAEventUnregister, repo.fga[1].EventType)
	assert.Equal(t, "project:prj-src", repo.fga[1].Intent.Tuples[0].SubjectID)
}

// Same-project destination → InvalidArgument verbatim.
func TestMove_SameProject_InvalidArg(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-x", "same-proj")
	repo.seedTG(tg)
	uc := NewMoveTargetGroupUseCase(repo, newFakeOpsRepo(), &fakeProjectClient{}, nil)

	_, err := uc.Execute(context.Background(), &lbv1.MoveTargetGroupRequest{
		TargetGroupId:        string(tg.ID),
		DestinationProjectId: "prj-x",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "destination project is the same as source")
}

// GWT-TGR-026 — attached to LB → FailedPrecondition verbatim.
func TestMove_HasAttachedLB(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-y", "attached")
	repo.seedTG(tg)
	repo.seedAttached("nlb-1", string(tg.ID))
	uc := NewMoveTargetGroupUseCase(repo, newFakeOpsRepo(), &fakeProjectClient{}, nil)

	_, err := uc.Execute(context.Background(), &lbv1.MoveTargetGroupRequest{
		TargetGroupId:        string(tg.ID),
		DestinationProjectId: "prj-z",
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(),
		"is attached to 1 load balancer(s); detach before moving")
}

// Destination project peer NotFound → InvalidArgument with verbatim.
func TestMove_DestProjectNotFound(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-src", "peer-nf")
	repo.seedTG(tg)
	uc := NewMoveTargetGroupUseCase(repo, newFakeOpsRepo(),
		&fakeProjectClient{getFunc: func(_ context.Context, id string) (*iam.Project, error) {
			return nil, projectNotFound(id)
		}}, nil)

	_, err := uc.Execute(context.Background(), &lbv1.MoveTargetGroupRequest{
		TargetGroupId:        string(tg.ID),
		DestinationProjectId: "prj-doesnt-exist",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "Project prj-doesnt-exist not found")
}

func TestMove_MissingFields(t *testing.T) {
	uc := NewMoveTargetGroupUseCase(newFakeRepo(), newFakeOpsRepo(), &fakeProjectClient{}, nil)
	for _, tc := range []struct {
		name string
		req  *lbv1.MoveTargetGroupRequest
	}{
		{"no id", &lbv1.MoveTargetGroupRequest{DestinationProjectId: "p"}},
		{"no dst", &lbv1.MoveTargetGroupRequest{TargetGroupId: "tgr-x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := uc.Execute(context.Background(), tc.req)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestMove_NotFound(t *testing.T) {
	uc := NewMoveTargetGroupUseCase(newFakeRepo(), newFakeOpsRepo(), &fakeProjectClient{}, nil)
	_, err := uc.Execute(context.Background(), &lbv1.MoveTargetGroupRequest{
		TargetGroupId:        "tgr-missing",
		DestinationProjectId: "prj-dst",
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}
