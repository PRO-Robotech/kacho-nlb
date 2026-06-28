// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg_test

// fga_register_labels_integration_test.go — (nlb-side):
// nlb эмитит labels + parent в RegisterResource (зеркало compute). Проверяет
// ЗАПИСЬ расширенного intent в той же writer-tx, что и INSERT ресурса, и что
// emitter стампит монотонный source_version из DB-clock (now) в payload
// (hardening last-source-state-wins). drainer тут не запущен — изолируем emit.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// TestFGARegisterOutbox_T3_CreateIntentCarriesLabelsParentSourceVersion —
// nlb-side: Create TargetGroup с labels → fga.register intent payload несёт
// labels + parent_project_id + монотонный source_version (стампится emitter'ом из
// now внутри writer-tx). Это то, что register-drainer форвардит в IAM
// resource_mirror для γ-selector matchLabels.
func TestFGARegisterOutbox_T3_CreateIntentCarriesLabelsParentSourceVersion(t *testing.T) {
	tc := newTestCtx(t)
	ctx := context.Background()

	const projectID = "prj-t3aaaaaaaaaaaaaa"
	tg := newTG(projectID, "tg-critical")
	tg.Labels = domain.LabelsFromMap(map[string]string{"tier": "critical"})

	intent := domain.FGARegisterIntent{
		Kind:            "TargetGroup",
		ResourceID:      string(tg.ID),
		Labels:          map[string]string{"tier": "critical"},
		ParentProjectID: projectID,
		Tuples: []domain.FGATuple{
			domain.FGAProjectTuple(domain.FGAObjectTypeTargetGroup, string(tg.ID), projectID),
		},
	}

	commitWriter(t, tc.Repo, func(w kacho.RepositoryWriter) {
		_, err := w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
		require.NoError(t, w.FGARegisterOutbox().Emit(ctx, domain.FGAEventRegister, intent))
	})

	rows := queryRegisterRows(t, ctx, tc)
	require.Len(t, rows, 1)
	require.Equal(t, domain.FGAEventRegister, rows[0].eventType)

	decoded, err := decodeIntent(rows[0].payload)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"tier": "critical"}, decoded.Labels, "labels in payload")
	assert.Equal(t, projectID, decoded.ParentProjectID, "parent_project_id in payload")
	assert.False(t, decoded.SourceVersion.IsZero(),
		"emitter must stamp source_version from DB clock into payload (β-hardening)")
}

// TestFGARegisterOutbox_T3_UpdateLabelsEmitsRegisterIntent — Update(labels-mask)
// → fga.register intent re-emitted in the same writer-tx as the resource UPDATE,
// carrying the NEW labels. Keeps the IAM mirror current under label-change
// reconcile. A later mutation's source_version is strictly newer.
func TestFGARegisterOutbox_T3_UpdateLabelsEmitsRegisterIntent(t *testing.T) {
	tc := newTestCtx(t)
	ctx := context.Background()

	const projectID = "prj-t3bbbbbbbbbbbbbb"
	tg := newTG(projectID, "tg-upd")
	tg.Labels = domain.LabelsFromMap(map[string]string{"tier": "gold"})
	commitWriter(t, tc.Repo, func(w kacho.RepositoryWriter) {
		_, err := w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})

	// Simulate an Update(labels) worker step: UPDATE + register-intent in one tx.
	tg.Labels = domain.LabelsFromMap(map[string]string{"tier": "critical"})
	updIntent := domain.FGARegisterIntent{
		Kind:            "TargetGroup",
		ResourceID:      string(tg.ID),
		Labels:          map[string]string{"tier": "critical"},
		ParentProjectID: projectID,
		Tuples: []domain.FGATuple{
			domain.FGAProjectTuple(domain.FGAObjectTypeTargetGroup, string(tg.ID), projectID),
		},
	}
	commitWriter(t, tc.Repo, func(w kacho.RepositoryWriter) {
		_, err := w.TargetGroups().Update(ctx, tg)
		require.NoError(t, err)
		require.NoError(t, w.FGARegisterOutbox().Emit(ctx, domain.FGAEventRegister, updIntent))
	})

	rows := queryRegisterRows(t, ctx, tc)
	require.Len(t, rows, 1, "exactly one register intent from the Update flow")
	decoded, err := decodeIntent(rows[0].payload)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"tier": "critical"}, decoded.Labels,
		"Update intent carries the new labels")
	assert.False(t, decoded.SourceVersion.IsZero(), "Update intent stamped with source_version")
}
