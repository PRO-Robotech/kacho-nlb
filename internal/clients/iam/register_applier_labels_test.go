package iam_test

// register_applier_labels_test.go — epic-rsab T3 (D4): nlb эмитит labels+parent в
// RegisterResource (зеркало compute-β). Юнит-проверка applier'а: каждый tuple
// register/unregister должен форвардить FGARegisterIntent.Labels +
// ParentProjectID + ParentAccountID + SourceVersion в RegisterResourceRequest /
// UnregisterResourceRequest. Без Postgres — scripted fake-client recorder.
//
// T3-02 (nlb-side): selector для loadbalancer.targetGroups требует, чтобы nlb
// наполнял IAM resource_mirror метками+parent через расширенный payload Internal
// RegisterResource (не новое ребро — существующее nlb→iam, расширенный payload).

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	iampb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// recordingRegisterClient captures the RegisterResource / UnregisterResource
// requests so the test can assert the forwarded mirror fields.
type recordingRegisterClient struct {
	register   []*iampb.RegisterResourceRequest
	unregister []*iampb.UnregisterResourceRequest
}

func (c *recordingRegisterClient) RegisterResource(
	_ context.Context, in *iampb.RegisterResourceRequest, _ ...grpc.CallOption,
) (*iampb.RegisterResourceResponse, error) {
	c.register = append(c.register, in)
	return &iampb.RegisterResourceResponse{}, nil
}

func (c *recordingRegisterClient) UnregisterResource(
	_ context.Context, in *iampb.UnregisterResourceRequest, _ ...grpc.CallOption,
) (*iampb.UnregisterResourceResponse, error) {
	c.unregister = append(c.unregister, in)
	return &iampb.UnregisterResourceResponse{}, nil
}

// TestRegisterApplier_T3_ForwardsLabelsAndParentOnRegister — fga.register intent
// carrying labels + parent + source_version → applier forwards them verbatim into
// RegisterResourceRequest for every tuple in the set (T3-02 nlb-side mirror feed).
func TestRegisterApplier_T3_ForwardsLabelsAndParentOnRegister(t *testing.T) {
	rec := &recordingRegisterClient{}
	apply := iam.NewRegisterApplier(rec)

	src := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	intent := domain.FGARegisterIntent{
		Kind:            "TargetGroup",
		ResourceID:      "tgr-aaaaaaaaaaaaaaaaa",
		Labels:          map[string]string{"tier": "critical"},
		ParentProjectID: "prj-prod000000000000",
		ParentAccountID: "acc-aaaaaaaaaaaaaaaa",
		SourceVersion:   src,
		Tuples: []domain.FGATuple{
			domain.FGAProjectTuple(domain.FGAObjectTypeTargetGroup, "tgr-aaaaaaaaaaaaaaaaa", "prj-prod000000000000"),
		},
	}

	err := apply(context.Background(), domain.FGAEventRegister, intent)
	require.NoError(t, err)
	require.Len(t, rec.register, 1, "one RegisterResource per tuple")

	got := rec.register[0]
	assert.Equal(t, map[string]string{"tier": "critical"}, got.GetLabels(), "labels forwarded")
	assert.Equal(t, "prj-prod000000000000", got.GetParentProjectId(), "parent_project_id forwarded")
	assert.Equal(t, "acc-aaaaaaaaaaaaaaaa", got.GetParentAccountId(), "parent_account_id forwarded")
	require.NotNil(t, got.GetSourceVersion(), "source_version forwarded")
	assert.True(t, src.Equal(got.GetSourceVersion().AsTime()), "source_version value preserved")
}

// TestRegisterApplier_T3_ForwardsLabelsAndParentOnUnregister — symmetry: the
// mirror fields are carried on Unregister too (object + source_version drive the
// tombstone-version no-op in IAM; β-hardening parity with compute).
func TestRegisterApplier_T3_ForwardsLabelsAndParentOnUnregister(t *testing.T) {
	rec := &recordingRegisterClient{}
	apply := iam.NewRegisterApplier(rec)

	src := time.Date(2026, 6, 20, 11, 0, 0, 0, time.UTC)
	intent := domain.FGARegisterIntent{
		Kind:            "TargetGroup",
		ResourceID:      "tgr-bbbbbbbbbbbbbbbbb",
		ParentProjectID: "prj-prod000000000000",
		SourceVersion:   src,
		Tuples: []domain.FGATuple{
			domain.FGAProjectTuple(domain.FGAObjectTypeTargetGroup, "tgr-bbbbbbbbbbbbbbbbb", "prj-prod000000000000"),
		},
	}

	err := apply(context.Background(), domain.FGAEventUnregister, intent)
	require.NoError(t, err)
	require.Len(t, rec.unregister, 1)

	got := rec.unregister[0]
	assert.Equal(t, "prj-prod000000000000", got.GetParentProjectId(), "parent forwarded on unregister")
	require.NotNil(t, got.GetSourceVersion(), "source_version forwarded on unregister")
	assert.True(t, src.Equal(got.GetSourceVersion().AsTime()))
}

// TestRegisterApplier_T3_EmptyMirrorFieldsGraceful — legacy/empty payload (no
// labels, zero source_version) → applier still applies, with nil source_version
// (IAM treats as '-infinity', graceful back-compat).
func TestRegisterApplier_T3_EmptyMirrorFieldsGraceful(t *testing.T) {
	rec := &recordingRegisterClient{}
	apply := iam.NewRegisterApplier(rec)

	intent := domain.FGARegisterIntent{
		Kind:       "NetworkLoadBalancer",
		ResourceID: "nlb-ccccccccccccccccc",
		Tuples: []domain.FGATuple{
			domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, "nlb-ccccccccccccccccc", "prj-prod000000000000"),
		},
	}

	err := apply(context.Background(), domain.FGAEventRegister, intent)
	require.NoError(t, err)
	require.Len(t, rec.register, 1)
	assert.Nil(t, rec.register[0].GetSourceVersion(), "zero source_version → nil (graceful)")
	assert.Empty(t, rec.register[0].GetLabels())
}
