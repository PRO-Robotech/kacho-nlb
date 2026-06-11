package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/clients/compute"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

func newCreateUC(repo *fakeRepo, opsRepo *fakeOpsRepo, pc ProjectClient, rc RegionClient) *CreateLoadBalancerUseCase {
	return NewCreateLoadBalancerUseCase(repo, opsRepo, pc, rc, slog.Default())
}

func TestCreateLoadBalancer_HappyPath(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	pc := &fakeProjectClient{}
	rc := &fakeRegionClient{}
	uc := newCreateUC(repo, opsRepo, pc, rc)

	op, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-acme", RegionId: "ru-central1",
		Name: "edge-public", Description: "test",
		Labels: map[string]string{"env": "prod"},
		Type:   lbv1.NetworkLoadBalancer_EXTERNAL,
	})
	require.NoError(t, err)
	require.False(t, op.Done)
	require.NotEmpty(t, op.ID)

	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.NotNil(t, final.Response)

	// LB is in repo with status INACTIVE.
	require.Len(t, repo.lbs, 1)
	for _, lb := range repo.lbs {
		require.Equal(t, domain.LBStatusInactive, lb.Status)
		require.Equal(t, "edge-public", string(lb.Name))
	}
	// Outbox CREATED event.
	evts := repo.outboxEvents()
	require.Len(t, evts, 1)
	require.Equal(t, "CREATED", evts[0].Action)
}

func TestCreateLoadBalancer_InvalidProjectID(t *testing.T) {
	t.Parallel()
	uc := newCreateUC(newFakeRepo(), newFakeOpsRepo(), nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
		Name: "edge", Type: lbv1.NetworkLoadBalancer_EXTERNAL,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCreateLoadBalancer_InvalidName(t *testing.T) {
	t.Parallel()
	uc := newCreateUC(newFakeRepo(), newFakeOpsRepo(), nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-a", RegionId: "ru-central1",
		Name: "Edge!", Type: lbv1.NetworkLoadBalancer_EXTERNAL,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCreateLoadBalancer_TypeUnspecified(t *testing.T) {
	t.Parallel()
	uc := newCreateUC(newFakeRepo(), newFakeOpsRepo(), nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-a", RegionId: "ru-central1", Name: "edge",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCreateLoadBalancer_DuplicateName(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	seedLB(t, repo, "prj-a", "edge")
	uc := newCreateUC(repo, newFakeOpsRepo(), nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-a", RegionId: "ru-central1", Name: "edge",
		Type: lbv1.NetworkLoadBalancer_EXTERNAL,
	})
	require.Equal(t, codes.AlreadyExists, status.Code(err))
}

func TestCreateLoadBalancer_ProjectNotFound(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	pc := &fakeProjectClient{
		getFunc: func(ctx context.Context, projectID string) (*iam.Project, error) {
			return nil, fmt.Errorf("%w: Project %s not found", domain.ErrNotFound, projectID)
		},
	}
	uc := newCreateUC(repo, opsRepo, pc, &fakeRegionClient{})
	op, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-missing", RegionId: "ru-central1",
		Name: "edge", Type: lbv1.NetworkLoadBalancer_EXTERNAL,
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error, "operation should have async error")
	require.Equal(t, int32(codes.InvalidArgument), final.Error.GetCode())
	// LB not persisted.
	require.Empty(t, repo.lbs)
}

func TestCreateLoadBalancer_RegionNotFound(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	rc := &fakeRegionClient{
		getFunc: func(ctx context.Context, regionID string) (*compute.Region, error) {
			return nil, fmt.Errorf("%w: Region %s not found", domain.ErrInvalidArg, regionID)
		},
	}
	uc := newCreateUC(repo, opsRepo, &fakeProjectClient{}, rc)
	op, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-a", RegionId: "ru-mars",
		Name: "edge", Type: lbv1.NetworkLoadBalancer_EXTERNAL,
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error)
}

// TestCreateLoadBalancer_FGARegisterIntentEmitted — SEC-D: Create writes a
// fga.register-intent (project-hierarchy + creator) into the writer-tx outbox,
// not a direct best-effort FGA call.
func TestCreateLoadBalancer_FGARegisterIntentEmitted(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	uc := newCreateUC(repo, opsRepo, &fakeProjectClient{}, &fakeRegionClient{})
	op, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-a", RegionId: "ru-central1",
		Name: "edge", Type: lbv1.NetworkLoadBalancer_EXTERNAL,
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)

	require.Len(t, repo.fga, 1, "expected one fga.register intent in writer-tx")
	ev := repo.fga[0]
	require.Equal(t, domain.FGAEventRegister, ev.EventType)
	require.Equal(t, "NetworkLoadBalancer", ev.Intent.Kind)
	// system principal (no auth in unit ctx) → project-hierarchy tuple only.
	require.NotEmpty(t, ev.Intent.Tuples)
	require.Equal(t, domain.FGARelationProject, ev.Intent.Tuples[0].Relation)
	require.Equal(t, "project:prj-a", ev.Intent.Tuples[0].SubjectID)
}

func TestCreateLoadBalancer_ProjectClientErrorMapped(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		peerErr  error
		wantCode codes.Code
	}{
		"unavailable":         {fmt.Errorf("%w: dial", domain.ErrUnavailable), codes.Unavailable},
		"invalid_arg":         {fmt.Errorf("%w: invalid project", domain.ErrInvalidArg), codes.InvalidArgument},
		"failed_precondition": {fmt.Errorf("%w: project deleted", domain.ErrFailedPrecondition), codes.FailedPrecondition},
		"generic":             {errors.New("boom"), codes.Internal},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opsRepo := newFakeOpsRepo()
			pc := &fakeProjectClient{getFunc: func(_ context.Context, _ string) (*iam.Project, error) {
				return nil, tc.peerErr
			}}
			uc := newCreateUC(newFakeRepo(), opsRepo, pc, &fakeRegionClient{})
			op, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
				ProjectId: "prj-a", RegionId: "ru-central1",
				Name: "edge", Type: lbv1.NetworkLoadBalancer_EXTERNAL,
			})
			require.NoError(t, err)
			final := awaitOpDone(t, opsRepo, op.ID)
			require.NotNil(t, final.Error)
			require.Equal(t, int32(tc.wantCode), final.Error.GetCode())
		})
	}
}
