// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

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

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/clients/compute"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// mkAddUC — конструктор AddTargetsUseCase с дефолтными happy peer-fakes.
func mkAddUC(repo *fakeRepo, opsRepo *fakeOpsRepo) *AddTargetsUseCase {
	return NewAddTargetsUseCase(repo, opsRepo,
		&fakeInstanceClient{}, &fakeNICClient{}, &fakeSubnetClient{}, nil,
	)
}

// AddTargets with all 4 identity variants in one request.
func TestAdd_AllFourIdentities(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "all-variants")
	repo.seedTG(tg)
	opsRepo := newFakeOpsRepo()
	uc := mkAddUC(repo, opsRepo)

	op, err := uc.Execute(context.Background(), &lbv1.AddTargetsRequest{
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
	require.Nilf(t, final.Error, "op error: %v", final.Error)

	events := repo.outboxEvents()
	require.Len(t, events, 1, "one UPDATED outbox")
	assert.Equal(t, kachorepo.OutboxActionUpdated, events[0].Action)
}

// idempotent re-add (ON CONFLICT DO NOTHING, no outbox).
func TestAdd_IdempotentReAdd_NoOutbox(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "idem")
	repo.seedTG(tg)
	tr := kachoTarget(string(tg.ID), domain.Target{
		InstanceID: option.MustNewOption(domain.InstanceID("epd-i1")),
		Weight:     100,
	})
	repo.seedTarget(string(tg.ID), &tr)

	opsRepo := newFakeOpsRepo()
	uc := mkAddUC(repo, opsRepo)
	op, err := uc.Execute(context.Background(), &lbv1.AddTargetsRequest{
		TargetGroupId: string(tg.ID),
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_InstanceId{InstanceId: "epd-i1"}, Weight: 100},
		},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Empty(t, repo.outboxEvents(), "no outbox emit when inserted=0")
}

// ip_ref outside subnet CIDR → InvalidArgument.
func TestAdd_IPRef_OutsideCIDR(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "out-cidr")
	repo.seedTG(tg)
	opsRepo := newFakeOpsRepo()
	uc := NewAddTargetsUseCase(repo, opsRepo,
		&fakeInstanceClient{}, &fakeNICClient{},
		&fakeSubnetClient{getFunc: func(_ context.Context, id string) (*vpc.Subnet, error) {
			return &vpc.Subnet{ID: id, ZoneID: "ru-central1-a", V4CIDRBlocks: []string{"10.0.0.0/24"}}, nil
		}},
		nil,
	)

	op, err := uc.Execute(context.Background(), &lbv1.AddTargetsRequest{
		TargetGroupId: string(tg.ID),
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_IpRef{IpRef: &lbv1.Target_InCloudIP{
				SubnetId: "e9b-sub1", Address: "10.1.0.5", // outside 10.0.0.0/24
			}}, Weight: 100},
		},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error)
	require.Equal(t, int32(codes.InvalidArgument), final.Error.Code)
	require.Contains(t, final.Error.Message, "10.1.0.5 is not in subnet")
	require.Contains(t, final.Error.Message, "10.0.0.0/24")
}

// weight out-of-bounds (BVA).
func TestAdd_Weight_OutOfBounds(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "weight-bva")
	repo.seedTG(tg)
	uc := mkAddUC(repo, newFakeOpsRepo())

	_, err := uc.Execute(context.Background(), &lbv1.AddTargetsRequest{
		TargetGroupId: string(tg.ID),
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_InstanceId{InstanceId: "epd-x"}, Weight: 1001},
		},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, fieldViolationsText(err), "weight must be in range [0, 1000]")
}

// instance region mismatch.
func TestAdd_InstanceRegionMismatch(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "inst-rmm") // region ru-central1
	repo.seedTG(tg)
	opsRepo := newFakeOpsRepo()
	uc := NewAddTargetsUseCase(repo, opsRepo,
		&fakeInstanceClient{getFunc: func(_ context.Context, id string) (*compute.Instance, error) {
			return &compute.Instance{ID: id, ZoneID: "ru-central2-a"}, nil
		}},
		&fakeNICClient{}, &fakeSubnetClient{}, nil,
	)

	op, err := uc.Execute(context.Background(), &lbv1.AddTargetsRequest{
		TargetGroupId: string(tg.ID),
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_InstanceId{InstanceId: "epd-i2"}, Weight: 100},
		},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error)
	require.Contains(t, final.Error.Message, "region 'ru-central2'")
	require.Contains(t, final.Error.Message, "target_group region 'ru-central1'")
}

// NIC region mismatch (via parent subnet).
func TestAdd_NICRegionMismatch(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "nic-rmm")
	repo.seedTG(tg)
	opsRepo := newFakeOpsRepo()
	uc := NewAddTargetsUseCase(repo, opsRepo,
		&fakeInstanceClient{},
		&fakeNICClient{getFunc: func(_ context.Context, id string) (*vpc.NetworkInterface, error) {
			return &vpc.NetworkInterface{ID: id, SubnetID: "e9b-other"}, nil
		}},
		&fakeSubnetClient{getFunc: func(_ context.Context, id string) (*vpc.Subnet, error) {
			return &vpc.Subnet{ID: id, ZoneID: "ru-central2-b", V4CIDRBlocks: []string{"10.0.0.0/24"}}, nil
		}},
		nil,
	)

	op, err := uc.Execute(context.Background(), &lbv1.AddTargetsRequest{
		TargetGroupId: string(tg.ID),
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_NicId{NicId: "enp-other"}, Weight: 100},
		},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error)
	require.Contains(t, final.Error.Message, "region 'ru-central2'")
}

// ip_ref subnet region mismatch.
func TestAdd_IPRefSubnetRegionMismatch(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "ipref-rmm")
	repo.seedTG(tg)
	opsRepo := newFakeOpsRepo()
	uc := NewAddTargetsUseCase(repo, opsRepo,
		&fakeInstanceClient{}, &fakeNICClient{},
		&fakeSubnetClient{getFunc: func(_ context.Context, id string) (*vpc.Subnet, error) {
			return &vpc.Subnet{ID: id, ZoneID: "ru-central2-c", V4CIDRBlocks: []string{"10.0.0.0/24"}}, nil
		}},
		nil,
	)

	op, err := uc.Execute(context.Background(), &lbv1.AddTargetsRequest{
		TargetGroupId: string(tg.ID),
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_IpRef{IpRef: &lbv1.Target_InCloudIP{
				SubnetId: "e9b-other", Address: "10.0.0.5",
			}}, Weight: 100},
		},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error)
	require.Contains(t, final.Error.Message, "region 'ru-central2'")
}

// empty list → InvalidArgument.
func TestAdd_EmptyList(t *testing.T) {
	uc := mkAddUC(newFakeRepo(), newFakeOpsRepo())
	_, err := uc.Execute(context.Background(), &lbv1.AddTargetsRequest{
		TargetGroupId: "tgr-x",
		Targets:       nil,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "at least one target is required")
}

// TG in DELETING → FailedPrecondition.
func TestAdd_TGDeleting_FailedPrecondition(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "deleting")
	tg.Status = domain.TargetGroupStatusDeleting
	repo.seedTG(tg)
	opsRepo := newFakeOpsRepo()
	uc := mkAddUC(repo, opsRepo)

	op, err := uc.Execute(context.Background(), &lbv1.AddTargetsRequest{
		TargetGroupId: string(tg.ID),
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_InstanceId{InstanceId: "epd-i1"}, Weight: 100},
		},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error)
	require.Equal(t, int32(codes.FailedPrecondition), final.Error.Code)
	require.Contains(t, final.Error.Message, "target group is being deleted")
}

// (instance peer NotFound): instance not found at peer.
func TestAdd_InstanceNotFound_Verbatim(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "inst-nf")
	repo.seedTG(tg)
	opsRepo := newFakeOpsRepo()
	uc := NewAddTargetsUseCase(repo, opsRepo,
		&fakeInstanceClient{getFunc: func(_ context.Context, id string) (*compute.Instance, error) {
			return nil, fmt.Errorf("%w: Instance %s not found", domain.ErrInvalidArg, id)
		}},
		&fakeNICClient{}, &fakeSubnetClient{}, nil,
	)

	op, err := uc.Execute(context.Background(), &lbv1.AddTargetsRequest{
		TargetGroupId: string(tg.ID),
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_InstanceId{InstanceId: "epd-doesnt-exist"}, Weight: 100},
		},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error)
	require.Equal(t, int32(codes.InvalidArgument), final.Error.Code)
	require.Contains(t, final.Error.Message, "target[0].instance_id 'epd-doesnt-exist' not found")
}

// EmptyTGID → InvalidArgument.
func TestAdd_EmptyTGID(t *testing.T) {
	uc := mkAddUC(newFakeRepo(), newFakeOpsRepo())
	_, err := uc.Execute(context.Background(), &lbv1.AddTargetsRequest{
		Targets: []*lbv1.Target{{Identity: &lbv1.Target_InstanceId{InstanceId: "x"}, Weight: 100}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// Two identities set on single target → InvalidArgument (domain Validate).
func TestAdd_TwoIdentities_OneTarget_InvalidArg(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "two-ids")
	repo.seedTG(tg)
	uc := mkAddUC(repo, newFakeOpsRepo())

	// proto allows only oneof — но мы можем эмулировать через 2-field record вручную:
	// proto-builder сам пропускает второй oneof заглушку. Полная проверка домена
	// делается в TestCreate_Target_NoIdentity_InvalidArg уже (0 identities). Здесь
	// просто проверим path: один target с identity = nil, должен упасть в Validate.
	_, err := uc.Execute(context.Background(), &lbv1.AddTargetsRequest{
		TargetGroupId: string(tg.ID),
		Targets:       []*lbv1.Target{{Weight: 100}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, fieldViolationsText(err), "exactly one of")
}
