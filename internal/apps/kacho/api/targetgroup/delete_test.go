package targetgroup

import (
	"context"
	"fmt"
	"testing"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// GWT-TGR-021 — Delete OK (no attached LB, no targets).
func TestDelete_Happy(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "del-happy")
	repo.seedTG(tg)
	opsRepo := newFakeOpsRepo()
	uc := NewDeleteTargetGroupUseCase(repo, opsRepo, nil)

	op, err := uc.Execute(context.Background(), &lbv1.DeleteTargetGroupRequest{
		TargetGroupId: string(tg.ID),
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)

	events := repo.outboxEvents()
	require.Len(t, events, 1)
	assert.Equal(t, kachopg.OutboxActionDeleted, events[0].Action)
}

// GWT-TGR-022 — Delete fails when attached to LB.
func TestDelete_HasAttachedLB(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "del-att")
	repo.seedTG(tg)
	repo.seedAttached("nlb-x", string(tg.ID))
	uc := NewDeleteTargetGroupUseCase(repo, newFakeOpsRepo(), nil)

	_, err := uc.Execute(context.Background(), &lbv1.DeleteTargetGroupRequest{
		TargetGroupId: string(tg.ID),
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(),
		"is attached to 1 load balancer(s); detach first")
}

// GWT-TGR-023 — Delete fails when targets exist.
func TestDelete_HasTargets(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "del-tgt")
	repo.seedTG(tg)
	tr := kachoTarget(string(tg.ID), domain.Target{
		InstanceID: option.MustNewOption(domain.InstanceID("epd0X1")),
		Weight:     100,
	})
	repo.seedTarget(string(tg.ID), &tr)
	uc := NewDeleteTargetGroupUseCase(repo, newFakeOpsRepo(), nil)

	_, err := uc.Execute(context.Background(), &lbv1.DeleteTargetGroupRequest{
		TargetGroupId: string(tg.ID),
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(),
		"has 1 target(s); remove them first via RemoveTargets")
}

// GWT-TGR-024 — concurrent AddTargets between precheck and DELETE → FK fallback
// FailedPrecondition (TOCTOU). Simulated via failOnDelete injected in fake.
func TestDelete_FKFallback_OnConcurrentAdd(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "del-fk")
	repo.seedTG(tg)
	repo.failOnDelete = fmt.Errorf("%w: TargetGroup %s has child targets (FK 23503)",
		kachorepo.ErrFailedPrecondition, tg.ID)
	opsRepo := newFakeOpsRepo()
	uc := NewDeleteTargetGroupUseCase(repo, opsRepo, nil)

	op, err := uc.Execute(context.Background(), &lbv1.DeleteTargetGroupRequest{
		TargetGroupId: string(tg.ID),
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error)
	require.Equal(t, int32(codes.FailedPrecondition), final.Error.Code)
}

func TestDelete_EmptyID(t *testing.T) {
	uc := NewDeleteTargetGroupUseCase(newFakeRepo(), newFakeOpsRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.DeleteTargetGroupRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestDelete_NotFound(t *testing.T) {
	uc := NewDeleteTargetGroupUseCase(newFakeRepo(), newFakeOpsRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.DeleteTargetGroupRequest{
		TargetGroupId: "tgr-missing",
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}
