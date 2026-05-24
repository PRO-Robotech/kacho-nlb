package targetgroup

import (
	"context"
	"testing"
	"time"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// GWT-TGT-011 — Phase A (immediate DRAINING-mark + outbox UPDATED + done<500ms).
func TestRemove_PhaseAMarksDraining(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "drain-now")
	repo.seedTG(tg)
	// Seed 2 targets — remove one, leave the other.
	t1 := kachoTarget(string(tg.ID), domain.Target{
		InstanceID: option.MustNewOption(domain.InstanceID("epd-i1")),
		Weight:     100,
	})
	t2 := kachoTarget(string(tg.ID), domain.Target{
		ExternalIP: &domain.TargetExternalIP{Address: "203.0.113.99"},
		Weight:     50,
	})
	repo.seedTarget(string(tg.ID), &t1)
	repo.seedTarget(string(tg.ID), &t2)

	opsRepo := newFakeOpsRepo()
	uc := NewRemoveTargetsUseCase(repo, opsRepo, nil)

	start := time.Now()
	op, err := uc.Execute(context.Background(), &lbv1.RemoveTargetsRequest{
		TargetGroupId: string(tg.ID),
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_InstanceId{InstanceId: "epd-i1"}, Weight: 100},
		},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Less(t, time.Since(start).Milliseconds(), int64(500),
		"Phase A latency must be <500ms")

	// Inspect repo: t1 should be DRAINING, t2 ACTIVE.
	t1Got := repo.targets[string(tg.ID)][t1.ID]
	require.Equal(t, "DRAINING", t1Got.Status)
	require.NotNil(t, t1Got.DrainStartedAt)
	t2Got := repo.targets[string(tg.ID)][t2.ID]
	require.Equal(t, "ACTIVE", t2Got.Status)

	// Outbox UPDATED.
	events := repo.outboxEvents()
	require.Len(t, events, 1)
	assert.Equal(t, kachopg.OutboxActionUpdated, events[0].Action)
}

// GWT-TGT-012 — identity not in TG → no-op (no outbox, no error).
func TestRemove_NonExistentIdentity_NoOp(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "no-match")
	repo.seedTG(tg)
	opsRepo := newFakeOpsRepo()
	uc := NewRemoveTargetsUseCase(repo, opsRepo, nil)

	op, err := uc.Execute(context.Background(), &lbv1.RemoveTargetsRequest{
		TargetGroupId: string(tg.ID),
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_InstanceId{InstanceId: "epd-doesnt-exist"}, Weight: 100},
		},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Empty(t, repo.outboxEvents(), "no outbox emit when affected=0")
}

// Remove with all 4 identity types matched.
func TestRemove_All4Identities(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "all4-rm")
	repo.seedTG(tg)

	for _, dt := range []domain.Target{
		{InstanceID: option.MustNewOption(domain.InstanceID("epd-i1")), Weight: 100},
		{NicID: option.MustNewOption(domain.NicID("enp-nic1")), Weight: 100},
		{IPRef: &domain.TargetIPRef{SubnetID: "e9b-sub1", Address: "10.0.0.5"}, Weight: 50},
		{ExternalIP: &domain.TargetExternalIP{Address: "203.0.113.99"}, Weight: 100},
	} {
		tr := kachoTarget(string(tg.ID), dt)
		repo.seedTarget(string(tg.ID), &tr)
	}

	opsRepo := newFakeOpsRepo()
	uc := NewRemoveTargetsUseCase(repo, opsRepo, nil)
	op, err := uc.Execute(context.Background(), &lbv1.RemoveTargetsRequest{
		TargetGroupId: string(tg.ID),
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_InstanceId{InstanceId: "epd-i1"}, Weight: 100},
			{Identity: &lbv1.Target_NicId{NicId: "enp-nic1"}, Weight: 100},
			{Identity: &lbv1.Target_IpRef{IpRef: &lbv1.Target_InCloudIP{
				SubnetId: "e9b-sub1", Address: "10.0.0.5",
			}}, Weight: 50},
			{Identity: &lbv1.Target_ExternalIp{ExternalIp: &lbv1.Target_ExternalIP{
				Address: "203.0.113.99",
			}}, Weight: 100},
		},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)

	// All 4 targets DRAINING.
	for _, rec := range repo.targets[string(tg.ID)] {
		assert.Equal(t, "DRAINING", rec.Status)
	}
}

// Already DRAINING — no re-mark, no outbox.
func TestRemove_AlreadyDraining_NoOp(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "already-d")
	repo.seedTG(tg)
	tr := kachoTarget(string(tg.ID), domain.Target{
		InstanceID: option.MustNewOption(domain.InstanceID("epd-i1")),
		Weight:     100,
	})
	tr.Status = "DRAINING"
	now := time.Now().UTC()
	tr.DrainStartedAt = &now
	repo.seedTarget(string(tg.ID), &tr)

	opsRepo := newFakeOpsRepo()
	uc := NewRemoveTargetsUseCase(repo, opsRepo, nil)
	op, err := uc.Execute(context.Background(), &lbv1.RemoveTargetsRequest{
		TargetGroupId: string(tg.ID),
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_InstanceId{InstanceId: "epd-i1"}, Weight: 100},
		},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Empty(t, repo.outboxEvents(), "no outbox when already DRAINING (affected=0)")
}

// Empty Targets list → InvalidArgument.
func TestRemove_EmptyList(t *testing.T) {
	uc := NewRemoveTargetsUseCase(newFakeRepo(), newFakeOpsRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.RemoveTargetsRequest{
		TargetGroupId: "tgr-x",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "at least one target is required")
}

// Missing TG id → InvalidArgument.
func TestRemove_EmptyTGID(t *testing.T) {
	uc := NewRemoveTargetsUseCase(newFakeRepo(), newFakeOpsRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.RemoveTargetsRequest{
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_InstanceId{InstanceId: "x"}, Weight: 100},
		},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TG not found → NotFound in worker (operation error).
func TestRemove_TGNotFound(t *testing.T) {
	opsRepo := newFakeOpsRepo()
	uc := NewRemoveTargetsUseCase(newFakeRepo(), opsRepo, nil)
	op, err := uc.Execute(context.Background(), &lbv1.RemoveTargetsRequest{
		TargetGroupId: "tgr-missing",
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_InstanceId{InstanceId: "x"}, Weight: 100},
		},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error)
	require.Equal(t, int32(codes.NotFound), final.Error.Code)
}
