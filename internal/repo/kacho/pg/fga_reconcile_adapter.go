// fga_reconcile_adapter.go — per-service adapters for the corelib
// outbox/reconciler (sub-phase 1.4 S3 backstop, D-6).
//
// The reconciler orchestrates re-drive / derive-from-state backfill / inverse-
// orphan GC; the DOMAIN knowledge (which tables hold tenant resources, their
// project_id, whether one still exists) is per-service and injected through
// reconciler.ResourceEnumerator + reconciler.TupleRegistry. This file implements
// those ports over the kacho_nlb resource tables (project-hierarchy only — every
// nlb resource carries project_id; no owner-self-grant backfill, D-4).
package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/kacho-corelib/outbox/reconciler"
)

// nlbResourceTables maps each outbox resource_kind to its kacho_nlb table. The
// kind labels match what the create use-cases write to fga_register_outbox
// ("NetworkLoadBalancer"/"Listener"/"TargetGroup"). All rows carry (id, project_id).
var nlbResourceTables = []struct {
	kind  string
	table string
}{
	{"NetworkLoadBalancer", "kacho_nlb.load_balancers"},
	{"Listener", "kacho_nlb.listeners"},
	{"TargetGroup", "kacho_nlb.target_groups"},
}

// FGAReconcileAdapter implements reconciler.ResourceEnumerator + TupleRegistry over
// the kacho_nlb resource tables + the register-outbox.
type FGAReconcileAdapter struct {
	pool  *pgxpool.Pool
	table string // full register-outbox table (kacho_nlb.fga_register_outbox)
}

// NewFGAReconcileAdapter constructs the per-service reconciler adapter. table is
// the full register-outbox table name the drainer/reconciler share.
func NewFGAReconcileAdapter(pool *pgxpool.Pool, table string) *FGAReconcileAdapter {
	return &FGAReconcileAdapter{pool: pool, table: table}
}

func nlbKindToTable(kind string) string {
	for _, rt := range nlbResourceTables {
		if rt.kind == kind {
			return rt.table
		}
	}
	return ""
}

// ListResources enumerates every live nlb resource as (kind, id, project_id) — the
// source of truth for derive-from-state backfill.
func (a *FGAReconcileAdapter) ListResources(ctx context.Context) ([]reconciler.ResourceRow, error) {
	var out []reconciler.ResourceRow
	for _, rt := range nlbResourceTables {
		rows, err := a.pool.Query(ctx,
			fmt.Sprintf(`SELECT id, project_id FROM %s`, rt.table)) //nolint:gosec // trusted literal
		if err != nil {
			return nil, fmt.Errorf("nlb reconcile enumerate %s: %w", rt.table, err)
		}
		for rows.Next() {
			var id, projectID string
			if err := rows.Scan(&id, &projectID); err != nil {
				rows.Close()
				return nil, fmt.Errorf("nlb reconcile scan %s: %w", rt.table, err)
			}
			out = append(out, reconciler.ResourceRow{Kind: rt.kind, ID: id, ProjectID: projectID})
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("nlb reconcile rows %s: %w", rt.table, err)
		}
	}
	return out, nil
}

// ResourceExists reports whether (kind,id) still exists — used by inverse-orphan
// GC to confirm a registered tuple's resource is gone before unregistering.
func (a *FGAReconcileAdapter) ResourceExists(ctx context.Context, kind, id string) (bool, error) {
	table := nlbKindToTable(kind)
	if table == "" {
		return false, nil
	}
	var exists bool
	if err := a.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1)`, table), //nolint:gosec // trusted literal
		id,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("nlb reconcile exists %s/%s: %w", kind, id, err)
	}
	return exists, nil
}

// ListRegistered derives the orphan-GC candidate set from the register-outbox:
// every (resource_kind, resource_id) whose LATEST intent is a delivered
// fga.register (sent_at NOT NULL). The reconciler then confirms absence +
// anti-race before any unregister. No direct FGA read (modules reach FGA only via
// kacho-iam).
func (a *FGAReconcileAdapter) ListRegistered(ctx context.Context) ([]reconciler.RegisteredTuple, error) {
	rows, err := a.pool.Query(ctx, fmt.Sprintf(`
		SELECT DISTINCT ON (resource_id) resource_kind, resource_id, event_type
		  FROM %s
		 WHERE resource_id <> '' AND sent_at IS NOT NULL
		 ORDER BY resource_id, id DESC`, a.table)) //nolint:gosec // trusted literal
	if err != nil {
		return nil, fmt.Errorf("nlb reconcile list-registered: %w", err)
	}
	defer rows.Close()
	var out []reconciler.RegisteredTuple
	for rows.Next() {
		var kind, id, eventType string
		if err := rows.Scan(&kind, &id, &eventType); err != nil {
			return nil, fmt.Errorf("nlb reconcile list-registered scan: %w", err)
		}
		if eventType != "fga.register" {
			continue
		}
		out = append(out, reconciler.RegisteredTuple{Kind: kind, ID: id})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("nlb reconcile list-registered rows: %w", err)
	}
	return out, nil
}

var (
	_ reconciler.ResourceEnumerator = (*FGAReconcileAdapter)(nil)
	_ reconciler.TupleRegistry      = (*FGAReconcileAdapter)(nil)
)
