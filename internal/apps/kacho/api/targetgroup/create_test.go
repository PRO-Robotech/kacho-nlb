package targetgroup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/fgawrite"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// mkCreateReq — минимальный валидный CreateTargetGroupRequest с HTTP HC.
func mkCreateReq(projectID, regionID, name string) *lbv1.CreateTargetGroupRequest {
	return &lbv1.CreateTargetGroupRequest{
		ProjectId:   projectID,
		RegionId:    regionID,
		Name:        name,
		Description: "test tg",
		Labels:      map[string]string{"tier": "web"},
		HealthCheck: &lbv1.HealthCheck{
			Name:               "hc-http",
			Interval:           durationpb.New(2 * time.Second),
			Timeout:            durationpb.New(1 * time.Second),
			UnhealthyThreshold: 2,
			HealthyThreshold:   2,
			Options: &lbv1.HealthCheck_HttpOptions_{
				HttpOptions: &lbv1.HealthCheck_HttpOptions{Port: 8080, Path: "/healthz"},
			},
		},
		DeregistrationDelaySeconds: 300,
		SlowStartSeconds:           30,
	}
}

// mkUC — констр+conv для CreateTargetGroupUseCase без peer-failures.
func mkUC(repo *fakeRepo, opsRepo *fakeOpsRepo) *CreateTargetGroupUseCase {
	return NewCreateTargetGroupUseCase(
		repo, opsRepo,
		&fakeProjectClient{}, &fakeRegionClient{},
		&fakeHierarchy{}, nil,
	)
}

// GWT-TGR-001 — Create OK.
func TestCreate_Happy(t *testing.T) {
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	uc := mkUC(repo, opsRepo)

	op, err := uc.Execute(context.Background(), mkCreateReq("prj-acme", "ru-central1", "backend-web"))
	require.NoError(t, err)
	require.NotNil(t, op)

	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nilf(t, final.Error, "operation error: %v", final.Error)
	require.NotNil(t, final.Response)

	// Outbox: exactly one CREATED row.
	events := repo.outboxEvents()
	require.Len(t, events, 1)
	assert.Equal(t, kachopg.OutboxResourceTargetGroup, events[0].ResourceType)
	assert.Equal(t, kachopg.OutboxActionCreated, events[0].Action)
}

// GWT-TGR-002 — empty targets allowed.
func TestCreate_EmptyTargetsAllowed(t *testing.T) {
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	uc := mkUC(repo, opsRepo)

	req := mkCreateReq("prj-acme", "ru-central1", "empty-tg")
	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
}

// GWT-TGR-003 — multiple HC types set → InvalidArgument.
func TestCreate_HealthCheck_MultipleSet_InvalidArg(t *testing.T) {
	repo := newFakeRepo()
	uc := mkUC(repo, newFakeOpsRepo())

	req := mkCreateReq("prj-acme", "ru-central1", "multi-hc")
	// proto HealthCheck.Options is oneof — нельзя задать 2 одновременно через
	// конструктор. Эмулируем "ни одного" → ErrTGR-004 same text.
	req.HealthCheck.Options = nil
	_, err := uc.Execute(context.Background(), req)
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, fieldViolationsText(err), "exactly one of: tcp, http, https, grpc")
}

// GWT-TGR-005 — interval out-of-bounds.
func TestCreate_HCInterval_OutOfBounds(t *testing.T) {
	repo := newFakeRepo()
	uc := mkUC(repo, newFakeOpsRepo())
	req := mkCreateReq("prj-acme", "ru-central1", "bad-interval")
	req.HealthCheck.Interval = durationpb.New(601 * time.Second)
	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// GWT-TGR-006 — thresholds out-of-bounds.
func TestCreate_Thresholds_OutOfBounds(t *testing.T) {
	repo := newFakeRepo()
	uc := mkUC(repo, newFakeOpsRepo())
	req := mkCreateReq("prj-acme", "ru-central1", "bad-thr")
	req.HealthCheck.UnhealthyThreshold = 11 // max=10
	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, fieldViolationsText(err), "unhealthy_threshold must be in range [2, 10]")
}

// GWT-TGR-007 — deregistration_delay out-of-bounds.
func TestCreate_DeregDelay_OutOfBounds(t *testing.T) {
	repo := newFakeRepo()
	uc := mkUC(repo, newFakeOpsRepo())
	req := mkCreateReq("prj-acme", "ru-central1", "bad-dreg")
	req.DeregistrationDelaySeconds = 3601
	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, fieldViolationsText(err), "deregistration_delay_seconds must be in range [0, 3600]")
}

// GWT-TGR-008 — slow_start out-of-bounds.
func TestCreate_SlowStart_OutOfBounds(t *testing.T) {
	repo := newFakeRepo()
	uc := mkUC(repo, newFakeOpsRepo())
	req := mkCreateReq("prj-acme", "ru-central1", "bad-slow")
	req.SlowStartSeconds = 901
	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, fieldViolationsText(err), "slow_start_seconds must be in range [0, 900]")
}

// GWT-TGR-009 — target without identity.
func TestCreate_Target_NoIdentity_InvalidArg(t *testing.T) {
	repo := newFakeRepo()
	uc := mkUC(repo, newFakeOpsRepo())
	req := mkCreateReq("prj-acme", "ru-central1", "no-ident")
	req.Targets = []*lbv1.Target{{Weight: 100}}
	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, fieldViolationsText(err),
		"target must specify exactly one of: instance_id, nic_id, ip_ref, external_ip")
}

// GWT-TGR-011 — bogon external_ip variants.
func TestCreate_Target_BogonExternalIP(t *testing.T) {
	bogons := []struct {
		addr   string
		reason string
	}{
		{"127.0.0.1", "loopback"},
		{"0.0.0.0", "unspecified"},
		{"169.254.1.1", "link-local"},
		{"239.0.0.1", "multicast"}, // site-local multicast (224/4 is broader)
		{"255.255.255.255", "broadcast"},
	}
	for _, b := range bogons {
		t.Run(b.addr, func(t *testing.T) {
			repo := newFakeRepo()
			uc := mkUC(repo, newFakeOpsRepo())
			req := mkCreateReq("prj-acme", "ru-central1", "bg-"+strings.ReplaceAll(b.addr, ".", "-"))
			req.Targets = []*lbv1.Target{{
				Identity: &lbv1.Target_ExternalIp{ExternalIp: &lbv1.Target_ExternalIP{Address: b.addr}},
				Weight:   100,
			}}
			_, err := uc.Execute(context.Background(), req)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Contains(t, fieldViolationsText(err), b.reason)
		})
	}
}

// GWT-TGR-014 — duplicate name → AlreadyExists.
func TestCreate_DuplicateName_AlreadyExists(t *testing.T) {
	repo := newFakeRepo()
	repo.seedTG(makeTG("prj-acme", "backend-web"))
	uc := mkUC(repo, newFakeOpsRepo())

	_, err := uc.Execute(context.Background(), mkCreateReq("prj-acme", "ru-central1", "backend-web"))
	require.Equal(t, codes.AlreadyExists, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "already exists in project")
}

// Missing required fields.
func TestCreate_MissingFields_InvalidArg(t *testing.T) {
	uc := mkUC(newFakeRepo(), newFakeOpsRepo())
	for _, tc := range []struct {
		name string
		req  *lbv1.CreateTargetGroupRequest
	}{
		{"no project_id", &lbv1.CreateTargetGroupRequest{RegionId: "ru-central1", HealthCheck: &lbv1.HealthCheck{}}},
		{"no region_id", &lbv1.CreateTargetGroupRequest{ProjectId: "p1", HealthCheck: &lbv1.HealthCheck{}}},
		{"no health_check", &lbv1.CreateTargetGroupRequest{ProjectId: "p1", RegionId: "ru-central1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := uc.Execute(context.Background(), tc.req)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

// Worker peer-project NotFound → Operation.Error InvalidArgument.
func TestCreate_PeerProject_NotFound(t *testing.T) {
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	uc := NewCreateTargetGroupUseCase(repo, opsRepo,
		&fakeProjectClient{getFunc: func(_ context.Context, id string) (*projectIamProjection, error) {
			return nil, projectNotFound(id)
		}},
		&fakeRegionClient{}, &fakeHierarchy{}, nil,
	)

	op, err := uc.Execute(context.Background(), mkCreateReq("prj-x", "ru-central1", "peer-fail"))
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error, "expected worker error")
	require.Equal(t, int32(codes.InvalidArgument), final.Error.Code)
	require.Contains(t, final.Error.Message, "Project prj-x not found")
}

// FGA writer writes creator + project tuples (with subject in ctx).
func TestCreate_EmitsHierarchyTuples(t *testing.T) {
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	fga := &fakeHierarchy{}
	uc := NewCreateTargetGroupUseCase(repo, opsRepo,
		&fakeProjectClient{}, &fakeRegionClient{}, fga, nil,
	)

	// Inject principal via operations.WithPrincipal.
	ctx := contextWithUser("alice")
	op, err := uc.Execute(ctx, mkCreateReq("prj-fga", "ru-central1", "tg-fga"))
	require.NoError(t, err)
	awaitOpDone(t, opsRepo, op.ID)

	fga.mu.Lock()
	defer fga.mu.Unlock()
	require.NotEmpty(t, fga.rewriteCalls, "project tuple write expected")
	require.NotEmpty(t, fga.creatorCalls, "creator tuple write expected")
	require.Contains(t, fga.creatorCalls[0], "user:alice")
	require.Contains(t, fga.creatorCalls[0], fgawrite.RelationOwner)
}
