package pg_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// TestLB_CRUD — GWT-DB-001..004 (basic CRUD via Writer + Reader).
func TestLB_CRUD(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01TESTPRJ123456ll", "demo-lb-1")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		rec, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		assert.Equal(t, lb.ID, rec.ID)
		assert.False(t, rec.CreatedAt.IsZero())
		assert.False(t, rec.UpdatedAt.IsZero())
		require.NoError(t, w.Outbox().Emit(ctx, "nlb_load_balancer", string(lb.ID), string(lb.ProjectID), "CREATED", map[string]any{"id": string(lb.ID)}))
	})

	// Read via Reader (committed snapshot).
	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()
	got, err := rd.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	assert.Equal(t, "demo-lb-1", string(got.Name))
	assert.Equal(t, domain.LBTypeExternal, got.Type)
	val, ok := got.Labels.Get("test")
	require.True(t, ok)
	assert.Equal(t, domain.LbLabelVal("1"), val)
}

// TestLB_NotFound — Get на отсутствующий id → ErrNotFound.
func TestLB_NotFound(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()
	_, err = rd.LoadBalancers().Get(ctx, "nlb01NOTEXISTNOTEXIS")
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrNotFound), "want ErrNotFound, got %v", err)
}

// TestLB_DuplicateName_AlreadyExists — GWT-DB-005, NLB-009: partial UNIQUE
// (project_id, name) WHERE name<>''.
func TestLB_DuplicateName_AlreadyExists(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	a := newLB("prj01DUPP1234567890ll", "dup-name")
	b := newLB("prj01DUPP1234567890ll", "dup-name")

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, a)
		require.NoError(t, err)
	})

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, err = w.LoadBalancers().Insert(ctx, b)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrAlreadyExists), "want ErrAlreadyExists, got %v", err)
}

// TestLB_CheckViolation_BadStatus — CHECK status IN (...) → InvalidArg.
func TestLB_CheckViolation_BadStatus(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01CHKK1234567890ll", "chk-lb")
	lb.Status = "INVALID_STATUS"

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, err = w.LoadBalancers().Insert(ctx, lb)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrInvalidArg), "want ErrInvalidArg, got %v", err)
}

// TestLB_CheckViolation_BadName — CHECK name regex.
func TestLB_CheckViolation_BadName(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01CHKN1234567890ll", "Bad-Uppercase")

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, err = w.LoadBalancers().Insert(ctx, lb)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrInvalidArg), "want ErrInvalidArg for bad name, got %v", err)
}

// TestLB_LabelsTooMany_CheckViolation — GWT-DB-002: 65 labels → CHECK fail.
func TestLB_LabelsTooMany_CheckViolation(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	bigLabels := map[string]string{}
	for i := 0; i < 65; i++ {
		bigLabels[uniqueLabelKey(i)] = "x"
	}
	require.Len(t, bigLabels, 65, "test must produce 65 distinct keys")
	lb := newLB("prj01LBLS1234567890ll", "lbl-lb")
	lb.Labels = domain.LabelsFromMap(bigLabels)

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, err = w.LoadBalancers().Insert(ctx, lb)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrInvalidArg), "want ErrInvalidArg for >64 labels, got %v", err)
}

// uniqueLabelKey генерит уникальные label-keys конформные с
// kacho_labels_valid regex `^[a-z][-_./@0-9a-z]{0,62}$`. Двухсимвольный
// формат: первая буква 'a' + ascii letter ('a'+i%26) + decimal digit (i/26).
// Для i=0..64 даёт 65 уникальных ключей вида `a<letter><digit>`.
func uniqueLabelKey(i int) string {
	letter := byte('a' + i%26)
	digit := byte('0' + (i / 26))
	return string([]byte{'a', letter, digit})
}

// TestLB_Update_MutatesMutable — Update меняет name/description/labels, не type/region.
func TestLB_Update_MutatesMutable(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01UPDP1234567890ll", "u-lb")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	lb.Name = "u-lb-new"
	lb.Description = "updated"
	lb.Labels = domain.LabelsFromMap(map[string]string{"env": "prod"})

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		rec, err := w.LoadBalancers().Update(ctx, lb)
		require.NoError(t, err)
		assert.Equal(t, domain.LbName("u-lb-new"), rec.Name)
		assert.Equal(t, domain.LbDescription("updated"), rec.Description)
		val, ok := rec.Labels.Get("env")
		require.True(t, ok)
		assert.Equal(t, domain.LbLabelVal("prod"), val)
	})
}

// TestLB_SetStatusCAS — atomic CAS: правильный expected → updates; wrong expected → FailedPrecondition.
func TestLB_SetStatusCAS(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01CASS1234567890ll", "cas-lb")
	lb.Status = domain.LBStatusInactive
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	// CAS-hit: Inactive → Starting.
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		rec, err := w.LoadBalancers().SetStatusCAS(ctx, string(lb.ID),
			domain.LBStatusInactive, domain.LBStatusStarting)
		require.NoError(t, err)
		assert.Equal(t, domain.LBStatusStarting, rec.Status)
	})

	// CAS-miss: expected=Inactive (currently Starting) → FailedPrecondition.
	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, err = w.LoadBalancers().SetStatusCAS(ctx, string(lb.ID),
		domain.LBStatusInactive, domain.LBStatusActive)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition), "want ErrFailedPrecondition, got %v", err)
}

// TestLB_Delete_FK_RESTRICT — GWT-DB-010 / NLB-045: нельзя удалить LB с
// зависимыми Listener'ами.
func TestLB_Delete_FK_RESTRICT_Listeners(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01DELP1234567890ll", "del-lb")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		l := newListener(lb.ID, string(lb.ProjectID), "del-lst", 8080)
		_, err = w.Listeners().Insert(ctx, l)
		require.NoError(t, err)
	})

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	err = w.LoadBalancers().Delete(ctx, string(lb.ID))
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition), "want ErrFailedPrecondition (FK 23503), got %v", err)
}

// TestLB_HasListeners — EXISTS-check для Delete-precheck.
func TestLB_HasListeners(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01HASS1234567890ll", "has-lb")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	has, err := rd.LoadBalancers().HasListeners(ctx, string(lb.ID))
	require.NoError(t, err)
	assert.False(t, has)
	_ = rd.Close()

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		l := newListener(lb.ID, string(lb.ProjectID), "h-lst", 9090)
		_, err := w.Listeners().Insert(ctx, l)
		require.NoError(t, err)
	})

	rd2, _ := repo.Reader(ctx)
	defer func() { _ = rd2.Close() }()
	has, err = rd2.LoadBalancers().HasListeners(ctx, string(lb.ID))
	require.NoError(t, err)
	assert.True(t, has)
}

// TestLB_OutboxTransactional — rollback writer'а не оставляет outbox-row.
func TestLB_OutboxTransactional(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01OUTB1234567890ll", "outbox-lb")
	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	_, err = w.LoadBalancers().Insert(ctx, lb)
	require.NoError(t, err)
	require.NoError(t, w.Outbox().Emit(ctx, "nlb_load_balancer", string(lb.ID),
		string(lb.ProjectID), "CREATED", map[string]any{"id": string(lb.ID)}))
	w.Abort() // rollback

	// Reader не видит ни LB, ни outbox.
	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	_, err = rd.LoadBalancers().Get(ctx, string(lb.ID))
	assert.True(t, errors.Is(err, kacho.ErrNotFound))
	// Outbox row тоже не вставлен — нет наблюдаемого pubsub эффекта.
}

// TestLB_List_Pagination — keyset pagination через PageToken.
func TestLB_List_Pagination(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	const project = "prj01LIST1234567890ll"
	for i := 0; i < 5; i++ {
		lb := newLB(project, "list-lb-"+string(rune('a'+i)))
		commitWriter(t, repo, func(w kacho.RepositoryWriter) {
			_, err := w.LoadBalancers().Insert(ctx, lb)
			require.NoError(t, err)
		})
	}

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	page1, nextToken, err := rd.LoadBalancers().List(ctx,
		kacho.LoadBalancerFilter{ProjectID: project}, kacho.Pagination{PageSize: 2})
	require.NoError(t, err)
	require.Len(t, page1, 2)
	require.NotEmpty(t, nextToken)

	page2, nextToken2, err := rd.LoadBalancers().List(ctx,
		kacho.LoadBalancerFilter{ProjectID: project},
		kacho.Pagination{PageSize: 2, PageToken: nextToken})
	require.NoError(t, err)
	require.Len(t, page2, 2)
	require.NotEmpty(t, nextToken2)

	page3, nextToken3, err := rd.LoadBalancers().List(ctx,
		kacho.LoadBalancerFilter{ProjectID: project},
		kacho.Pagination{PageSize: 2, PageToken: nextToken2})
	require.NoError(t, err)
	require.Len(t, page3, 1)
	require.Empty(t, nextToken3)
}

// TestLB_StatusRecomputeTrigger — INSERT listener + AttachTG → LB.status
// INACTIVE → ACTIVE; DELETE listener → ACTIVE → INACTIVE (GWT-DB-004).
func TestLB_StatusRecomputeTrigger(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01TRGS1234567890ll", "trig-lb")
	lb.Status = domain.LBStatusInactive
	tg := newTG(string(lb.ProjectID), "trig-tg")

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
		l := newListener(lb.ID, string(lb.ProjectID), "trig-lst", 7777)
		_, err = w.Listeners().Insert(ctx, l)
		require.NoError(t, err)
		_, _, err = w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg.ID), 100)
		require.NoError(t, err)
	})

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	got, err := rd.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	assert.Equal(t, domain.LBStatusActive, got.Status, "trigger lb_status_recompute → ACTIVE")
}

// TestLB_ConcurrentInsertSameName — partial UNIQUE race. Две goroutine
// одновременно вставляют LB с одним (project_id, name); ровно одна успешна.
func TestLB_ConcurrentInsertSameName(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	const project = "prj01RACE1234567890ll"
	const name = "race-lb"

	var wg sync.WaitGroup
	var successes, conflicts int
	var mu sync.Mutex
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lb := newLB(project, name)
			w, err := repo.Writer(ctx)
			if err != nil {
				return
			}
			defer w.Abort()
			_, err = w.LoadBalancers().Insert(ctx, lb)
			if err != nil {
				mu.Lock()
				if errors.Is(err, kacho.ErrAlreadyExists) {
					conflicts++
				}
				mu.Unlock()
				return
			}
			if err := w.Commit(); err != nil {
				mu.Lock()
				if errors.Is(err, kacho.ErrAlreadyExists) {
					conflicts++
				}
				mu.Unlock()
				return
			}
			mu.Lock()
			successes++
			mu.Unlock()
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, successes, "exactly one Insert succeeds")
	assert.Equal(t, 1, conflicts, "the other gets ErrAlreadyExists")
}
