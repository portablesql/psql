package psql_test

import (
	"context"
	"errors"
	"testing"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// === IsDuplicate tests ===

func TestIsDuplicate(t *testing.T) {
	t.Run("nil error", func(t *testing.T) {
		assert.False(t, psql.IsDuplicate(nil))
	})

	t.Run("generic error", func(t *testing.T) {
		assert.False(t, psql.IsDuplicate(errors.New("something went wrong")))
	})

	// NOTE: SQLite-specific duplicate detection tests removed — they require
	// the SQLite dialect to be registered (via the psql-sqlite driver).
}

// === CaseInsensitive Like tests ===

func TestCaseInsensitiveLike(t *testing.T) {
	ctx := context.Background()

	t.Run("standalone EscapeValue", func(t *testing.T) {
		il := &psql.Like{Field: psql.F("name"), Like: "john%", CaseInsensitive: true}
		result := il.EscapeValue()
		assert.Contains(t, result, `"name"`)
		assert.Contains(t, result, "LIKE")
		assert.Contains(t, result, "'john%'")
		assert.Contains(t, result, "ESCAPE '\\'")
	})

	t.Run("CILike helper", func(t *testing.T) {
		il := psql.CILike(psql.F("name"), "john%")
		result := il.EscapeValue()
		assert.Contains(t, result, `"name"`)
		assert.Contains(t, result, "LIKE")
		assert.Contains(t, result, "'john%'")
	})

	t.Run("in WHERE clause", func(t *testing.T) {
		query := psql.B().Select().From("users").
			Where(map[string]any{"name": psql.Like{Like: "john%", CaseInsensitive: true}})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "LIKE")
		assert.Contains(t, sql, "'john%'")
	})

	t.Run("NOT case-insensitive Like", func(t *testing.T) {
		query := psql.B().Select().From("users").
			Where(map[string]any{"name": &psql.Not{V: psql.Like{Like: "test%", CaseInsensitive: true}}})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "NOT LIKE")
	})
}

// === FOR UPDATE SKIP LOCKED / NOWAIT tests ===

func TestForUpdateSkipLocked(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select().From("jobs").
		Where(map[string]any{"status": "pending"}).
		SetSkipLocked()
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "FOR UPDATE")
	assert.Contains(t, sql, "SKIP LOCKED")
	assert.NotContains(t, sql, "NOWAIT")
}

func TestForUpdateNoWait(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select().From("jobs").
		Where(map[string]any{"status": "pending"}).
		SetNoWait()
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "FOR UPDATE")
	assert.Contains(t, sql, "NOWAIT")
	assert.NotContains(t, sql, "SKIP LOCKED")
}

func TestSetForUpdate(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select().From("jobs").SetForUpdate()
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "FOR UPDATE")
	assert.NotContains(t, sql, "SKIP LOCKED")
	assert.NotContains(t, sql, "NOWAIT")
}

func TestFetchLockSkipLocked(t *testing.T) {
	assert.True(t, psql.FetchLockSkipLocked.Lock)
	assert.True(t, psql.FetchLockSkipLocked.SkipLocked)
}

func TestFetchLockNoWait(t *testing.T) {
	assert.True(t, psql.FetchLockNoWait.Lock)
	assert.True(t, psql.FetchLockNoWait.NoWait)
}

// === Incr/Decr/SetRaw tests ===

func TestIncrement(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Update("counters").
		Set(map[string]any{"views": psql.Incr(1)}).
		Where(map[string]any{"id": 42})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"views"="views"+(1)`)
	assert.Contains(t, sql, `WHERE ("id"=42)`)
}

func TestDecrement(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Update("inventory").
		Set(map[string]any{"stock": psql.Decr(5)}).
		Where(map[string]any{"item_id": 10})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"stock"="stock"-(5)`)
}

func TestSetRaw(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Update("users").
		Set(map[string]any{"updated_at": &psql.SetRaw{SQL: "NOW()"}}).
		Where(map[string]any{"id": 1})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"updated_at"=NOW()`)
}

func TestIncrRenderArgs(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Update("counters").
		Set(map[string]any{"views": psql.Incr(1)}).
		Where(map[string]any{"id": 42})
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"views"="views"+(?)`)
	assert.Contains(t, args, 1)
}

// === IN with typed slices tests ===

func TestINTypedSliceInt(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select().From("users").
		Where(map[string]any{"id": []int{1, 2, 3}})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"id" IN(1,2,3)`)
}

func TestINTypedSliceInt64(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select().From("orders").
		Where(map[string]any{"total": []int64{100, 200, 300}})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"total" IN(100,200,300)`)
}

func TestINTypedSliceEmpty(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select().From("users").
		Where(map[string]any{"id": []int{}})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "FALSE")
}

func TestINTypedSliceNot(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select().From("users").
		Where(map[string]any{"id": &psql.Not{V: []int{1, 2}}})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"id" NOT IN(1,2)`)
}

// === Any wrapper tests ===

func TestAnyInWhere(t *testing.T) {
	ctx := context.Background()

	// Without parameterized queries (default engine), falls back to IN
	query := psql.B().Select().From("users").
		Where(map[string]any{"id": &psql.Any{Values: []int{1, 2, 3}}})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"id" IN(1,2,3)`)
}

func TestAnyNotInWhere(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select().From("users").
		Where(map[string]any{"id": &psql.Not{V: &psql.Any{Values: []int{1, 2}}}})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"id" NOT IN(1,2)`)
}

func TestAnyEmpty(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select().From("users").
		Where(map[string]any{"id": &psql.Any{Values: []int{}}})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "FALSE")
}

// === Raw in WHERE tests ===

func TestRawInWhere(t *testing.T) {
	ctx := context.Background()

	// Raw as a standalone WHERE value
	query := psql.B().Select().From("users").
		Where(psql.Raw(`"age" > 18`))
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"age" > 18`)
}

func TestRawInWhereRenderArgs(t *testing.T) {
	ctx := context.Background()

	// Raw in parameterized mode should still be injected verbatim
	query := psql.B().Select().From("users").
		Where(psql.Raw(`"active" = TRUE`))
	sql, _, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"active" = TRUE`)
}

// === Subquery in WHERE tests ===

func TestSubqueryScalar(t *testing.T) {
	ctx := context.Background()

	sub := psql.B().Select("user_id").From("orders").Limit(1)
	query := psql.B().Select().From("users").
		Where(map[string]any{"id": sub})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"id"=(SELECT "user_id" FROM "orders" LIMIT 1)`)
}

func TestSubqueryIN(t *testing.T) {
	ctx := context.Background()

	sub := psql.B().Select("user_id").From("orders")
	query := psql.B().Select().From("users").
		Where(map[string]any{"id": &psql.SubIn{Sub: sub}})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"id" IN (SELECT "user_id" FROM "orders")`)
}

func TestSubqueryNotIN(t *testing.T) {
	ctx := context.Background()

	sub := psql.B().Select("user_id").From("orders")
	query := psql.B().Select().From("users").
		Where(map[string]any{"id": &psql.Not{V: &psql.SubIn{Sub: sub}}})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"id" NOT IN (SELECT "user_id" FROM "orders")`)
}

func TestSubqueryRenderArgs(t *testing.T) {
	ctx := context.Background()

	sub := psql.B().Select("user_id").From("orders").Where(map[string]any{"status": "active"})
	query := psql.B().Select().From("users").
		Where(map[string]any{"id": &psql.SubIn{Sub: sub}})
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "IN (SELECT")
	assert.Len(t, args, 1) // "active" should be parameterized
}

// === Upsert builder tests ===

func TestBuilderOnConflictDoNothing(t *testing.T) {
	ctx := context.Background()

	// Default engine (MySQL/Unknown) → INSERT IGNORE
	q := psql.B().Insert(map[string]any{"id": 1, "name": "test"}).DoNothing()
	q.Table("users")
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "INSERT")
	assert.Contains(t, sql, "IGNORE")
}

func TestBuilderOnConflictDoUpdate(t *testing.T) {
	ctx := context.Background()

	// Default engine (MySQL/Unknown) → ON DUPLICATE KEY UPDATE
	q := psql.B().Insert(map[string]any{"id": 1, "name": "test"}).
		OnConflict("id").
		DoUpdate(map[string]any{"name": "test"})
	q.Table("users")
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "INSERT")
	assert.Contains(t, sql, "ON DUPLICATE KEY UPDATE")
	assert.Contains(t, sql, `"name"='test'`)
}

// === RunQueryT typing tests (compile-time only, no DB) ===

// Just verify the generic functions compile and have correct signatures
func TestRunQueryTCompiles(t *testing.T) {
	type User struct {
		ID   int    `sql:"id,key=PRIMARY"`
		Name string `sql:"name"`
	}

	// These function references just verify compilation
	_ = psql.RunQueryT[User]
	_ = psql.RunQueryTOne[User]
}
