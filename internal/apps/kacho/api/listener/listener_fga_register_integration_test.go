package listener_test

// listener_fga_register_integration_test.go — sub-phase T3.1 (#113), scenario
// T3.1-REVOKE-04 (acceptance §3.2). Verifies the consumer-side mirror-feed for
// nlb.listener through the REAL Create/Update use-cases (testcontainers Postgres):
//
//   - Create: listenerRegisterIntent must carry the resource's labels (was a
//     bare intent WITHOUT labels — the double-bug; selector would not match even
//     a freshly created listener, mirror.labels empty).
//   - Update (labels-in-mask): the Update worker must emit a mirror.upsert
//     RegisterResource carrying the CURRENT labels in the SAME writer-tx as the
//     listener UPDATE (G-2/G-3/G-4). Full label removal ⇒ upsert with labels={}
//     (NOT Unregister — the resource still lives), which stales the label selector.
//
// Semantics anchor: T3.1-REVOKE-04 (with the "after Create visibility appears"
// sub-step — Create must emit labels, otherwise the revoke below would be a
// false-green over an empty mirror).
//
// File path note: acceptance §3.2 names this `internal/repo/listener_fga_register_
// integration_test.go`. nlb's established layout exercises use-cases from their own
// package (`internal/apps/kacho/api/listener/`, see integration_test.go); placing
// the test here runs the ACTUAL Create/Update emit path (not a hand-built intent),
// which is the faithful TDD-red anchor. The load-bearing function name from §3.2
// is preserved verbatim.

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/listener"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// registerIntentRow — decoded fga_register_outbox row scoped to one resource.
type registerIntentRow struct {
	eventType string
	intent    domain.FGARegisterIntent
}

// queryListenerRegisterRows reads fga_register_outbox rows for one resource_id,
// decoding the JSON payload into the domain intent (mirror of pkg pg helper).
func queryListenerRegisterRows(t *testing.T, ic *integrationCtx, resourceID string) []registerIntentRow {
	t.Helper()
	rows, err := ic.pool.Query(context.Background(), `
        SELECT event_type, payload
          FROM kacho_nlb.fga_register_outbox
         WHERE resource_id = $1
         ORDER BY id`, resourceID)
	require.NoError(t, err)
	defer rows.Close()
	var out []registerIntentRow
	for rows.Next() {
		var ev string
		var payload []byte
		require.NoError(t, rows.Scan(&ev, &payload))
		var intent domain.FGARegisterIntent
		require.NoError(t, json.Unmarshal(payload, &intent))
		out = append(out, registerIntentRow{eventType: ev, intent: intent})
	}
	require.NoError(t, rows.Err())
	return out
}

// TestListenerRepo_T31Revoke04_CreateEmitsLabels_UpdateRevokes — T3.1-REVOKE-04.
//
// Double-bug fix verification for nlb.listener (acceptance §0.1, §0.2 G-1):
//
//	(a) Create  → mirror-feed intent carries the listener's labels (visibility
//	    появляется). Was bare intent without Labels → RED.
//	(b) Update(labels→{}) → a mirror.upsert RegisterResource intent is emitted
//	    with labels={} (revoke). Was no emit at all → RED.
func TestListenerRepo_T31Revoke04_CreateEmitsLabels_UpdateRevokes(t *testing.T) {
	t.Parallel()
	ic := newIntegrationCtx(t)
	const projectID = "prj01T31REVOKE040001"
	lb := ic.seedLB(t, projectID, "ru-central1", domain.LBTypeExternal, "lb-revoke04")
	internalAddrs := &recordingInternalAddrs{}
	createUC := listener.NewCreateUseCase(ic.repo, ic.opsRepo, nil, internalAddrs, nil, slog.Default())
	updateUC := listener.NewUpdateUseCase(ic.repo, ic.opsRepo, slog.Default())

	// --- Create with labels={"lsn":"treska"} ---
	ctx := context.Background()
	op, err := createUC.Run(ctx, &lbv1.CreateListenerRequest{
		LoadBalancerId: string(lb.ID),
		Name:           "lsn-treska",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		Labels:         map[string]string{"lsn": "treska"},
		AddressSpec:    autoSpecIntegration(""),
	})
	require.NoError(t, err)
	awaitOpDoneIntegration(t, ic.opsRepo, op.ID, 5*time.Second)
	gotOp, err := ic.opsRepo.Get(context.Background(), op.ID)
	require.NoError(t, err)
	require.True(t, gotOp.Done)
	require.Nil(t, gotOp.Error, "Create operation must succeed; got %v", gotOp.Error)

	// Discover inserted listener_id.
	rd, err := ic.repo.Reader(context.Background())
	require.NoError(t, err)
	page, _, err := rd.Listeners().ListByLB(context.Background(), string(lb.ID), kachorepo.Pagination{})
	require.NoError(t, err)
	_ = rd.Close()
	require.Len(t, page, 1)
	listenerID := string(page[0].ID)

	// (a) Create-emit MUST carry labels (RED until listenerRegisterIntent sets Labels).
	createRows := queryListenerRegisterRows(t, ic, listenerID)
	require.NotEmpty(t, createRows, "Create must emit at least one fga.register intent for the listener")
	createReg := lastRegister(t, createRows)
	assert.Equal(t, map[string]string{"lsn": "treska"}, createReg.intent.Labels,
		"Create mirror-feed must carry the listener's labels (visibility appears) — was bare intent without labels")
	assert.Equal(t, projectID, createReg.intent.ParentProjectID,
		"Create mirror-feed must carry parent_project_id for γ containment")
	assert.False(t, createReg.intent.SourceVersion.IsZero(),
		"emitter must stamp a monotonic source_version into the Create intent")

	createRowCount := len(createRows)

	// --- Update labels→{} (full removal) ---
	updOp, err := updateUC.Run(ctx, &lbv1.UpdateListenerRequest{
		ListenerId: listenerID,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"labels"}},
		Labels:     map[string]string{}, // empty → revoke
	})
	require.NoError(t, err)
	awaitOpDoneIntegration(t, ic.opsRepo, updOp.ID, 5*time.Second)
	gotUpd, err := ic.opsRepo.Get(context.Background(), updOp.ID)
	require.NoError(t, err)
	require.True(t, gotUpd.Done)
	require.Nil(t, gotUpd.Error, "Update operation must succeed; got %v", gotUpd.Error)

	// (b) Update-emit MUST add a new mirror.upsert intent with labels={} (revoke).
	updRows := queryListenerRegisterRows(t, ic, listenerID)
	require.Greater(t, len(updRows), createRowCount,
		"labels-in-mask Update must emit a new mirror.upsert intent (revoke) — was no emit at all")
	updReg := lastRegister(t, updRows)
	assert.Empty(t, updReg.intent.Labels,
		"label-removal Update must emit mirror.upsert with empty labels (upsert, NOT Unregister) — stales the label selector")
	assert.Equal(t, domain.FGAEventRegister, updReg.eventType,
		"label-removal stays a RegisterResource (mirror.upsert), never UnregisterResource — the listener still lives")
	assert.False(t, updReg.intent.SourceVersion.IsZero(),
		"Update intent must be stamped with a fresh source_version")
}

// lastRegister returns the latest RegisterResource (mirror.upsert) row, skipping
// any unregister rows — the assertion target is the most recent register feed.
func lastRegister(t *testing.T, rows []registerIntentRow) registerIntentRow {
	t.Helper()
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].eventType == domain.FGAEventRegister {
			return rows[i]
		}
	}
	t.Fatalf("no RegisterResource row found among %d fga_register_outbox rows", len(rows))
	return registerIntentRow{}
}
