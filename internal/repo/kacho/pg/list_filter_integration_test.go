// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// RBAC — repo-layer per-object filter.
//
// Каждый List<Resource>Filter получает поле AllowedIDs string (FGA-allow-set
// от iam ListObjects). Семантика (зеркало kacho-compute disk_repo.go):
//   - AllowedIDs == nil  → фильтр не применяется (bypass / нет authz).
//   - len(AllowedIDs)==0 → пустой результат (0 строк, без SQL-выборки) — пустой
//     грант не должен возвращать все строки (no-leak).
//   - len>0              → `WHERE id = ANY($allowed)` ВНУТРИ SQL, ДО LIMIT, чтобы
//     keyset-пагинация была плотной по отфильтрованному набору.
//
// Эти тесты ссылаются на ещё-не-существующее поле AllowedIDs → RED до GREEN.

// AllowedIDs subset → ровно перечисленные LB.
func TestLB_List_AllowedIDsSubset(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	const project = "prj01LFLB123456789all"
	a := newLB(project, "lf-lb-a")
	b := newLB(project, "lf-lb-b")
	c := newLB(project, "lf-lb-c")
	for _, l := range []*domain.LoadBalancer{a, b, c} {
		lb := l
		commitWriter(t, repo, func(w kacho.RepositoryWriter) {
			_, err := w.LoadBalancers().Insert(ctx, lb)
			require.NoError(t, err)
		})
	}

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()

	got, _, err := rd.LoadBalancers().List(ctx,
		kacho.LoadBalancerFilter{ProjectID: project, AllowedIDs: []string{string(a.ID), string(c.ID)}},
		kacho.Pagination{})
	require.NoError(t, err)
	require.Len(t, got, 2)
	ids := map[string]bool{}
	for _, r := range got {
		ids[string(r.ID)] = true
	}
	assert.True(t, ids[string(a.ID)])
	assert.True(t, ids[string(c.ID)])
	assert.False(t, ids[string(b.ID)])
}

// Empty AllowedIDs (non-nil) → 0 rows (no-leak), even though rows exist.
func TestLB_List_EmptyAllowedIDsReturnsNone(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	const project = "prj01LFEMPTY12345all0"
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, newLB(project, "lf-empty"))
		require.NoError(t, err)
	})

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()

	got, next, err := rd.LoadBalancers().List(ctx,
		kacho.LoadBalancerFilter{ProjectID: project, AllowedIDs: []string{}},
		kacho.Pagination{})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Empty(t, next)

	// Control: nil AllowedIDs → returns the row (no filter applied).
	gotAll, _, err := rd.LoadBalancers().List(ctx,
		kacho.LoadBalancerFilter{ProjectID: project, AllowedIDs: nil},
		kacho.Pagination{})
	require.NoError(t, err)
	require.Len(t, gotAll, 1)
}

// pagination АФТЕР фильтра — N accessible из M общих, плотные страницы.
func TestLB_List_PaginationAfterFilter(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	const project = "prj01LFPAGE12345all00"
	const total = 12
	const accessibleN = 5
	var allowed []string
	for i := 0; i < total; i++ {
		lb := newLB(project, fmt.Sprintf("lf-page-%02d", i))
		commitWriter(t, repo, func(w kacho.RepositoryWriter) {
			_, err := w.LoadBalancers().Insert(ctx, lb)
			require.NoError(t, err)
		})
		if i < accessibleN {
			allowed = append(allowed, string(lb.ID))
		}
	}

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()

	// page-size 2 over 5 accessible → 2 + 2 + 1, no holes from raw-then-drop.
	seen := map[string]bool{}
	token := ""
	pages := 0
	for {
		got, next, err := rd.LoadBalancers().List(ctx,
			kacho.LoadBalancerFilter{ProjectID: project, AllowedIDs: allowed},
			kacho.Pagination{PageSize: 2, PageToken: token})
		require.NoError(t, err)
		pages++
		require.LessOrEqual(t, len(got), 2)
		for _, r := range got {
			seen[string(r.ID)] = true
		}
		if next == "" {
			break
		}
		token = next
		require.LessOrEqual(t, pages, 10) // guard against infinite loop
	}
	assert.Len(t, seen, accessibleN)
	assert.Equal(t, 3, pages) // 2+2+1 — dense over the filtered set, not the raw 12
}

// TG: AllowedIDs subset.
func TestTG_List_AllowedIDsSubset(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	const project = "prj01LFTG123456789al0"
	a := newTG(project, "lf-tg-a")
	b := newTG(project, "lf-tg-b")
	for _, tg := range []*domain.TargetGroup{a, b} {
		v := tg
		commitWriter(t, repo, func(w kacho.RepositoryWriter) {
			_, err := w.TargetGroups().Insert(ctx, v)
			require.NoError(t, err)
		})
	}

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()

	got, _, err := rd.TargetGroups().List(ctx,
		kacho.TargetGroupFilter{ProjectID: project, AllowedIDs: []string{string(a.ID)}},
		kacho.Pagination{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, a.ID, got[0].ID)
}

// Listener: AllowedIDs subset (project-scoped).
func TestListener_List_AllowedIDsSubset(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01LFLST123456789a0", "lf-lst-lb")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})
	a := newListener(lb.ID, string(lb.ProjectID), "lf-lst-a", 80)
	b := newListener(lb.ID, string(lb.ProjectID), "lf-lst-b", 81)
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.Listeners().Insert(ctx, a)
		require.NoError(t, err)
		_, err = w.Listeners().Insert(ctx, b)
		require.NoError(t, err)
	})

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()

	got, _, err := rd.Listeners().List(ctx,
		kacho.ListenerFilter{ProjectID: string(lb.ProjectID), AllowedIDs: []string{string(a.ID)}},
		kacho.Pagination{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, a.ID, got[0].ID)
}
