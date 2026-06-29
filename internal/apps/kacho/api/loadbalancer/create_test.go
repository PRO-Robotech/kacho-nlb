// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/clients/geo"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// onlyLB returns the single LoadBalancer stored in the fake repo, failing if the
// count is not exactly one. Create generates a random id, so tests read the
// result through this helper instead of a known key.
func onlyLB(t *testing.T, repo *fakeRepo) domain.LoadBalancer {
	t.Helper()
	require.Len(t, repo.lbs, 1)
	for _, lb := range repo.lbs {
		return lb.LoadBalancer
	}
	return domain.LoadBalancer{}
}

// lbFieldViolations flattens the gRPC-status BadRequest field violations (corelib
// AddFieldViolation) into "field: description" lines for assert.Contains, since
// the verbatim text lives in details, not the top-level status message.
func lbFieldViolations(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return err.Error()
	}
	parts := []string{st.Message()}
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok {
			for _, v := range br.GetFieldViolations() {
				parts = append(parts, v.GetField()+": "+v.GetDescription())
			}
		}
	}
	return strings.Join(parts, " | ")
}

// TestCreateLoadBalancer_SessionAffinity — session_affinity from the request is
// persisted; UNSPECIFIED falls back to the FIVE_TUPLE default.
func TestCreateLoadBalancer_SessionAffinity(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		in   lbv1.NetworkLoadBalancer_SessionAffinity
		want domain.SessionAffinity
	}{
		"client_ip_only": {lbv1.NetworkLoadBalancer_CLIENT_IP_ONLY, domain.SessionAffinityClientIPOnly},
		"five_tuple":     {lbv1.NetworkLoadBalancer_FIVE_TUPLE, domain.SessionAffinity5Tuple},
		"unspecified":    {lbv1.NetworkLoadBalancer_SESSION_AFFINITY_UNSPECIFIED, domain.SessionAffinity5Tuple},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := newFakeRepo()
			opsRepo := newFakeOpsRepo()
			uc := newCreateUC(repo, opsRepo, &fakeProjectClient{}, &fakeRegionClient{})
			op, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
				ProjectId: "prj-a", RegionId: "ru-central1",
				Name: "edge-affinity", Type: lbv1.NetworkLoadBalancer_EXTERNAL,
				SessionAffinity: tc.in,
			})
			require.NoError(t, err)
			require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
			require.Equal(t, tc.want, onlyLB(t, repo).SessionAffinity)
		})
	}
}

// TestCreateLoadBalancer_SessionAffinityOutOfDomain — a numeric value outside
// {0,1,2} is rejected synchronously with the verbatim field message.
func TestCreateLoadBalancer_SessionAffinityOutOfDomain(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uc := newCreateUC(repo, newFakeOpsRepo(), &fakeProjectClient{}, &fakeRegionClient{})
	_, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-a", RegionId: "ru-central1",
		Name: "edge", Type: lbv1.NetworkLoadBalancer_EXTERNAL,
		SessionAffinity: lbv1.NetworkLoadBalancer_SessionAffinity(99),
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, lbFieldViolations(err),
		"session_affinity: session_affinity must be one of: FIVE_TUPLE, CLIENT_IP_ONLY")
	require.Empty(t, repo.lbs, "LB must not be persisted on out-of-domain session_affinity")
}

// TestCreateLoadBalancer_CrossZoneEnabled — an explicit cross_zone_enabled is
// honoured; an omitted field keeps the DB default (true).
func TestCreateLoadBalancer_CrossZoneEnabled(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		in   *bool
		want bool
	}{
		"explicit_false": {proto.Bool(false), false},
		"explicit_true":  {proto.Bool(true), true},
		"omitted":        {nil, true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := newFakeRepo()
			opsRepo := newFakeOpsRepo()
			uc := newCreateUC(repo, opsRepo, &fakeProjectClient{}, &fakeRegionClient{})
			op, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
				ProjectId: "prj-a", RegionId: "ru-central1",
				Name: "edge-cz", Type: lbv1.NetworkLoadBalancer_EXTERNAL,
				CrossZoneEnabled: tc.in,
			})
			require.NoError(t, err)
			require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
			require.Equal(t, tc.want, onlyLB(t, repo).CrossZoneEnabled)
		})
	}
}

func newCreateUC(repo *fakeRepo, opsRepo *fakeOpsRepo, pc ProjectClient, rc RegionClient) *CreateLoadBalancerUseCase {
	return NewCreateLoadBalancerUseCase(repo, opsRepo, pc, rc, &fakeNetworkClient{}, &fakeSecurityGroupClient{}, slog.Default())
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
		getFunc: func(ctx context.Context, regionID string) (*geo.Region, error) {
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

// TestCreateLoadBalancer_FGARegisterIntentEmitted — Create writes a
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
