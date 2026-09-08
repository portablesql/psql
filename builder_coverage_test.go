package psql_test

import (
	"context"
	"testing"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuilderCoverageJoinWithConditions tests that Join methods render correct
// SQL with ON conditions using full table.field references.
func TestBuilderCoverageJoinWithConditions(t *testing.T) {
	ctx := context.Background()

	t.Run("generic Join method", func(t *testing.T) {
		query := psql.B().
			Select("id", "name").
			From("t1").
			Join("CROSS", "t2")
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT "id","name" FROM "t1" CROSS JOIN "t2"`, sql)
	})

	t.Run("left join with ON condition", func(t *testing.T) {
		query := psql.B().
			Select(psql.F("t1", "name"), psql.F("t2", "value")).
			From("t1").
			LeftJoin("t2", psql.Equal(psql.F("t1.id"), psql.F("t2.t1_id")))
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `LEFT JOIN "t2" ON`)
		assert.Contains(t, sql, `"t1"."id"="t2"."t1_id"`)
	})

	t.Run("inner join with WHERE", func(t *testing.T) {
		query := psql.B().
			Select().
			From("orders").
			InnerJoin("users", psql.Equal(psql.F("orders.user_id"), psql.F("users.id"))).
			Where(map[string]any{"orders.status": "complete"})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `INNER JOIN "users" ON`)
		assert.Contains(t, sql, `"orders"."user_id"="users"."id"`)
		assert.Contains(t, sql, `WHERE`)
		assert.Contains(t, sql, `"orders"."status"='complete'`)
	})

	t.Run("right join", func(t *testing.T) {
		query := psql.B().
			Select().
			From("a").
			RightJoin("b", psql.Equal(psql.F("a.id"), psql.F("b.a_id")))
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `RIGHT JOIN "b" ON`)
		assert.Contains(t, sql, `"a"."id"="b"."a_id"`)
	})

	t.Run("join without condition", func(t *testing.T) {
		query := psql.B().
			Select().
			From("t1").
			InnerJoin("t2")
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `INNER JOIN "t2"`)
		// Should not contain ON since no condition was provided
		assert.NotContains(t, sql, "ON")
	})
}

// TestBuilderCoverageGroupByHaving tests GROUP BY with HAVING clause together.
func TestBuilderCoverageGroupByHaving(t *testing.T) {
	ctx := context.Background()

	t.Run("group by with having", func(t *testing.T) {
		query := psql.B().
			Select("department", psql.Raw("COUNT(*) as cnt")).
			From("employees").
			GroupByFields("department").
			Having(psql.Gt(psql.Raw("COUNT(*)"), 10))
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `SELECT "department",COUNT(*) as cnt`)
		assert.Contains(t, sql, `GROUP BY "department"`)
		assert.Contains(t, sql, `HAVING (COUNT(*)>10)`)
	})

	t.Run("group by with having map condition", func(t *testing.T) {
		query := psql.B().
			Select("status", psql.Raw("SUM(amount) as total")).
			From("orders").
			GroupByFields("status").
			Having(map[string]any{"status": "active"})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `GROUP BY "status"`)
		assert.Contains(t, sql, `HAVING ("status"='active')`)
	})

	t.Run("group by with field reference", func(t *testing.T) {
		query := psql.B().
			Select(psql.Raw("COUNT(*)")).
			From("users").
			GroupByFields(psql.F("role"))
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `GROUP BY "role"`)
	})
}

// TestBuilderCoverageSetDistinct tests the SetDistinct method.
func TestBuilderCoverageSetDistinct(t *testing.T) {
	ctx := context.Background()

	t.Run("distinct with fields", func(t *testing.T) {
		query := psql.B().Select("email").From("users").SetDistinct()
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT DISTINCT "email" FROM "users"`, sql)
	})

	t.Run("distinct with no fields", func(t *testing.T) {
		query := psql.B().Select().From("users").SetDistinct()
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT DISTINCT * FROM "users"`, sql)
	})

	t.Run("distinct with where", func(t *testing.T) {
		query := psql.B().Select("email").From("users").
			SetDistinct().
			Where(map[string]any{"active": true})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT DISTINCT")
		assert.Contains(t, sql, `WHERE ("active"=TRUE)`)
	})
}

// TestBuilderCoverageAlsoSelect tests the AlsoSelect method.
func TestBuilderCoverageAlsoSelect(t *testing.T) {
	ctx := context.Background()

	t.Run("add field", func(t *testing.T) {
		query := psql.B().Select("id").From("users").AlsoSelect(psql.F("email"))
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT "id","email" FROM "users"`, sql)
	})

	t.Run("add multiple fields", func(t *testing.T) {
		query := psql.B().Select("id").From("users").
			AlsoSelect(psql.F("name"), psql.F("email"))
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT "id","name","email" FROM "users"`, sql)
	})

	t.Run("add raw field", func(t *testing.T) {
		query := psql.B().Select("id").From("users").
			AlsoSelect(psql.Raw("COUNT(*) as cnt"))
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT "id",COUNT(*) as cnt FROM "users"`, sql)
	})

	t.Run("error on non-select", func(t *testing.T) {
		query := psql.B().Delete().From("users").AlsoSelect(psql.F("name"))
		_, err := query.Render(ctx)
		assert.Error(t, err)
	})
}

// TestBuilderCoverageReplace tests the Replace method.
func TestBuilderCoverageReplace(t *testing.T) {
	ctx := context.Background()

	t.Run("replace with set", func(t *testing.T) {
		q := &psql.QueryBuilder{Query: "REPLACE"}
		q.Table("products")
		q.FieldsSet = append(q.FieldsSet, map[string]any{"id": 1, "name": "widget"})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "REPLACE")
		assert.Contains(t, sql, `"products"`)
		assert.Contains(t, sql, "SET")
		assert.Contains(t, sql, `"id"=1`)
		assert.Contains(t, sql, `"name"='widget'`)
	})
}

// TestBuilderCoverageInsertInto tests Insert + Into combination.
func TestBuilderCoverageInsertInto(t *testing.T) {
	ctx := context.Background()

	t.Run("insert into with map", func(t *testing.T) {
		q := psql.B().Insert(map[string]any{"name": "Alice", "age": 30})
		q.Table("users")
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "INSERT")
		assert.Contains(t, sql, "INTO")
		assert.Contains(t, sql, `"users"`)
		assert.Contains(t, sql, "SET")
	})

	t.Run("insert with multiple maps", func(t *testing.T) {
		q := psql.B().
			Insert(map[string]any{"id": 1}).
			Set(map[string]any{"name": "Bob"})
		q.Table("users")
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "INSERT")
		assert.Contains(t, sql, `"users"`)
	})
}

// TestBuilderCoverageOnConflictDoUpdate tests INSERT ... ON CONFLICT ... DO UPDATE.
func TestBuilderCoverageOnConflictDoUpdate(t *testing.T) {
	ctx := context.Background()

	t.Run("on conflict do update default engine", func(t *testing.T) {
		q := psql.B().
			Insert(map[string]any{"id": 1, "name": "test"}).
			OnConflict("id").
			DoUpdate(map[string]any{"name": "updated"})
		q.Table("items")
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		// Default engine is MySQL-like, uses ON DUPLICATE KEY UPDATE
		assert.Contains(t, sql, "INSERT")
		assert.Contains(t, sql, "ON DUPLICATE KEY UPDATE")
		assert.Contains(t, sql, `"name"='updated'`)
	})

	t.Run("on conflict with multiple columns", func(t *testing.T) {
		q := psql.B().
			Insert(map[string]any{"a": 1, "b": 2, "val": "x"}).
			OnConflict("a", "b").
			DoUpdate(map[string]any{"val": "y"})
		q.Table("data")
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "INSERT")
		assert.Contains(t, sql, "ON DUPLICATE KEY UPDATE")
		assert.Contains(t, sql, `"val"='y'`)
	})
}

// TestBuilderCoverageOnConflictDoNothing tests INSERT ... ON CONFLICT DO NOTHING.
func TestBuilderCoverageOnConflictDoNothing(t *testing.T) {
	ctx := context.Background()

	t.Run("do nothing default engine", func(t *testing.T) {
		q := psql.B().
			Insert(map[string]any{"id": 1, "name": "test"}).
			DoNothing()
		q.Table("items")
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		// Default engine (MySQL-like) uses INSERT IGNORE
		assert.Contains(t, sql, "INSERT")
		assert.Contains(t, sql, "IGNORE")
	})

	t.Run("do nothing via InsertIgnore flag", func(t *testing.T) {
		q := &psql.QueryBuilder{Query: "INSERT", InsertIgnore: true}
		q.Table("items")
		q.FieldsSet = append(q.FieldsSet, map[string]any{"id": 1})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "INSERT")
		assert.Contains(t, sql, "IGNORE")
	})
}

// TestBuilderCoverageSetForUpdate tests the SetForUpdate method.
func TestBuilderCoverageSetForUpdate(t *testing.T) {
	ctx := context.Background()

	t.Run("for update via method", func(t *testing.T) {
		query := psql.B().Select().From("users").
			Where(map[string]any{"id": 1}).
			SetForUpdate()
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `FROM "users"`)
		assert.Contains(t, sql, "FOR UPDATE")
		assert.NotContains(t, sql, "SKIP LOCKED")
		assert.NotContains(t, sql, "NOWAIT")
	})

	t.Run("for update with limit", func(t *testing.T) {
		query := psql.B().Select().From("jobs").
			SetForUpdate().
			Limit(1)
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "LIMIT 1")
		assert.Contains(t, sql, "FOR UPDATE")
	})
}

// TestBuilderCoverageSetSkipLocked tests the SetSkipLocked method.
func TestBuilderCoverageSetSkipLocked(t *testing.T) {
	ctx := context.Background()

	t.Run("skip locked sets ForUpdate", func(t *testing.T) {
		query := psql.B().Select().From("tasks").SetSkipLocked()
		assert.True(t, query.ForUpdate, "SetSkipLocked should set ForUpdate to true")
		assert.True(t, query.SkipLocked, "SetSkipLocked should set SkipLocked to true")

		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "FOR UPDATE")
		assert.Contains(t, sql, "SKIP LOCKED")
	})

	t.Run("skip locked with where and limit", func(t *testing.T) {
		query := psql.B().Select("id").From("queue").
			Where(map[string]any{"status": "pending"}).
			OrderBy(psql.S("created_at", "ASC")).
			Limit(5).
			SetSkipLocked()
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `WHERE ("status"='pending')`)
		assert.Contains(t, sql, "LIMIT 5")
		assert.Contains(t, sql, "FOR UPDATE")
		assert.Contains(t, sql, "SKIP LOCKED")
	})
}

// TestBuilderCoverageSetNoWait tests the SetNoWait method.
func TestBuilderCoverageSetNoWait(t *testing.T) {
	ctx := context.Background()

	t.Run("nowait sets ForUpdate", func(t *testing.T) {
		query := psql.B().Select().From("locks").SetNoWait()
		assert.True(t, query.ForUpdate, "SetNoWait should set ForUpdate to true")
		assert.True(t, query.NoWait, "SetNoWait should set NoWait to true")

		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "FOR UPDATE")
		assert.Contains(t, sql, "NOWAIT")
		assert.NotContains(t, sql, "SKIP LOCKED")
	})
}

// TestBuilderCoverageLimitWithOffset tests Limit with two arguments.
func TestBuilderCoverageLimitWithOffset(t *testing.T) {
	ctx := context.Background()

	t.Run("limit with offset", func(t *testing.T) {
		query := psql.B().Select().From("users").Limit(10, 5)
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		// Limit(offset, count) renders LIMIT count OFFSET offset on every engine
		assert.Equal(t, `SELECT * FROM "users" LIMIT 5 OFFSET 10`, sql)
	})

	t.Run("limit without offset", func(t *testing.T) {
		query := psql.B().Select().From("users").Limit(10)
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users" LIMIT 10`, sql)
	})

	t.Run("limit clear", func(t *testing.T) {
		query := psql.B().Select().From("users").Limit(10, 5).Limit()
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users"`, sql)
	})

	t.Run("limit panic on too many args", func(t *testing.T) {
		assert.Panics(t, func() {
			psql.B().Select().From("users").Limit(1, 2, 3)
		})
	})
}

// TestBuilderCoverageInsertColsVals tests that INSERT renders correct
// column/value pairs via renderInsertColsVals (indirectly through Render).
func TestBuilderCoverageInsertColsVals(t *testing.T) {
	ctx := context.Background()

	t.Run("insert with map fields default engine", func(t *testing.T) {
		q := psql.B().Insert(map[string]any{
			"name":  "Alice",
			"email": "alice@example.com",
		})
		q.Table("users")
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		// Default engine uses MySQL SET syntax
		assert.Contains(t, sql, "INSERT")
		assert.Contains(t, sql, "INTO")
		assert.Contains(t, sql, "SET")
		assert.Contains(t, sql, `"email"='alice@example.com'`)
		assert.Contains(t, sql, `"name"='Alice'`)
	})
}

// TestBuilderCoverageErrorPropagation tests that errors from errorf propagate
// correctly through Render.
func TestBuilderCoverageErrorPropagation(t *testing.T) {
	ctx := context.Background()

	t.Run("unsupported table type", func(t *testing.T) {
		q := psql.B().Select()
		q.Table(12345)
		_, err := q.Render(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported type")
	})

	t.Run("error stops rendering", func(t *testing.T) {
		q := psql.B().Select()
		q.Table(struct{}{})
		_, err := q.Render(ctx)
		assert.Error(t, err)
	})

	t.Run("also select on non-select errors", func(t *testing.T) {
		q := psql.B().Update("users").AlsoSelect(psql.F("name"))
		_, err := q.Render(ctx)
		assert.Error(t, err)
	})
}

// TestBuilderCoverageSubIn tests the SubIn and EscapeValue on QueryBuilder.
func TestBuilderCoverageSubIn(t *testing.T) {
	ctx := context.Background()

	t.Run("SubIn in where clause", func(t *testing.T) {
		sub := psql.B().Select("user_id").From("orders").
			Where(psql.Gt(psql.F("amount"), 100))
		query := psql.B().Select().From("users").
			Where(map[string]any{"id": &psql.SubIn{Sub: sub}})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"id" IN (SELECT "user_id" FROM "orders"`)
		assert.Contains(t, sql, `"amount">100`)
	})

	t.Run("SubIn renders as subquery", func(t *testing.T) {
		sub := psql.B().Select("id").From("users")
		query := psql.B().Select().From("orders").
			Where(map[string]any{"user_id": &psql.SubIn{Sub: sub}})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"user_id" IN (SELECT "id" FROM "users")`)
	})

	t.Run("SubIn with NOT", func(t *testing.T) {
		sub := psql.B().Select("blocked_id").From("blocklist")
		query := psql.B().Select().From("users").
			Where(map[string]any{"id": &psql.Not{V: &psql.SubIn{Sub: sub}}})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"id" NOT IN (SELECT "blocked_id" FROM "blocklist")`)
	})
}

// TestBuilderCoverageApply tests the Apply method with scopes.
func TestBuilderCoverageApply(t *testing.T) {
	ctx := context.Background()

	activeScope := psql.Scope(func(q *psql.QueryBuilder) *psql.QueryBuilder {
		return q.Where(map[string]any{"active": true})
	})

	limitScope := psql.Scope(func(q *psql.QueryBuilder) *psql.QueryBuilder {
		return q.Limit(10)
	})

	t.Run("apply single scope", func(t *testing.T) {
		query := psql.B().Select().From("users").Apply(activeScope)
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `WHERE ("active"=TRUE)`)
	})

	t.Run("apply multiple scopes", func(t *testing.T) {
		query := psql.B().Select().From("users").Apply(activeScope, limitScope)
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `WHERE ("active"=TRUE)`)
		assert.Contains(t, sql, "LIMIT 10")
	})
}

// TestBuilderCoverageRenderArgs tests RenderArgs with various query types.
func TestBuilderCoverageRenderArgs(t *testing.T) {
	ctx := context.Background()

	t.Run("select with join args", func(t *testing.T) {
		query := psql.B().
			Select().From("users").
			LeftJoin("orders", psql.Equal(psql.F("users.id"), psql.F("orders.user_id"))).
			Where(map[string]any{"users.status": "active"})
		sql, args, err := query.RenderArgs(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `LEFT JOIN "orders"`)
		assert.Contains(t, sql, "WHERE")
		assert.Len(t, args, 1)
	})

	t.Run("insert with args default engine", func(t *testing.T) {
		q := psql.B().Insert(map[string]any{"name": "test", "value": 42})
		q.Table("data")
		sql, args, err := q.RenderArgs(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "INSERT")
		assert.Contains(t, sql, "SET")
		assert.Len(t, args, 2)
	})

	t.Run("distinct with args", func(t *testing.T) {
		query := psql.B().Select("name").From("users").
			SetDistinct().
			Where(map[string]any{"active": true})
		sql, args, err := query.RenderArgs(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT DISTINCT")
		assert.Len(t, args, 1)
	})

	t.Run("for update with args", func(t *testing.T) {
		query := psql.B().Select().From("jobs").
			Where(map[string]any{"status": "pending"}).
			SetForUpdate()
		sql, args, err := query.RenderArgs(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "FOR UPDATE")
		assert.Len(t, args, 1)
	})
}

// TestBuilderCoverageReplaceMethod tests the Replace builder method specifically.
func TestBuilderCoverageReplaceMethod(t *testing.T) {
	ctx := context.Background()

	t.Run("replace with builder method", func(t *testing.T) {
		q := &psql.QueryBuilder{Query: "REPLACE"}
		q.Table("items")
		q.FieldsSet = append(q.FieldsSet, map[string]any{"id": 42, "quantity": 10})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "REPLACE")
		assert.Contains(t, sql, "SET")
		assert.Contains(t, sql, `"id"=42`)
		assert.Contains(t, sql, `"quantity"=10`)
	})
}

// TestBuilderCoverageDeleteWithJoin tests DELETE with more complex clauses.
func TestBuilderCoverageDeleteWithJoin(t *testing.T) {
	ctx := context.Background()

	t.Run("delete with multiple where", func(t *testing.T) {
		query := psql.B().Delete().From("logs").
			Where(psql.Lt(psql.F("created_at"), "2020-01-01")).
			Where(map[string]any{"archived": true})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `DELETE FROM "logs"`)
		assert.Contains(t, sql, `WHERE`)
		assert.Contains(t, sql, `"created_at"<'2020-01-01'`)
		assert.Contains(t, sql, `"archived"=TRUE`)
	})
}

// TestBuilderCoverageOrderByLimit tests combined ORDER BY + LIMIT + OFFSET.
func TestBuilderCoverageOrderByLimit(t *testing.T) {
	ctx := context.Background()

	t.Run("order by with limit and offset", func(t *testing.T) {
		query := psql.B().Select("id", "name").From("users").
			OrderBy(psql.S("name", "ASC")).
			Limit(20, 40)
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT "id","name" FROM "users" ORDER BY "name" ASC LIMIT 40 OFFSET 20`, sql)
	})
}

// TestBuilderCoverageComplexQuery tests a complex query combining many features.
func TestBuilderCoverageComplexQuery(t *testing.T) {
	ctx := context.Background()

	query := psql.B().
		Select("department", psql.Raw("COUNT(*) as cnt"), psql.Raw("AVG(salary) as avg_sal")).
		From("employees").
		InnerJoin("departments", psql.Equal(psql.F("employees.dept_id"), psql.F("departments.id"))).
		Where(map[string]any{"employees.active": true}).
		GroupByFields("department").
		Having(psql.Gt(psql.Raw("COUNT(*)"), 5)).
		OrderBy(psql.S("department", "ASC")).
		Limit(100)

	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `SELECT "department",COUNT(*) as cnt,AVG(salary) as avg_sal`)
	assert.Contains(t, sql, `INNER JOIN "departments"`)
	assert.Contains(t, sql, `WHERE ("employees"."active"=TRUE)`)
	assert.Contains(t, sql, `GROUP BY "department"`)
	assert.Contains(t, sql, `HAVING (COUNT(*)>5)`)
	assert.Contains(t, sql, `ORDER BY "department" ASC`)
	assert.Contains(t, sql, `LIMIT 100`)
}

// TestBuilderCoverageInsertOnConflictBothFlags tests edge cases with conflict flags.
func TestBuilderCoverageInsertOnConflictBothFlags(t *testing.T) {
	ctx := context.Background()

	t.Run("conflict nothing via builder chain", func(t *testing.T) {
		q := psql.B().Insert(map[string]any{"id": 1, "val": "a"}).
			OnConflict("id").
			DoNothing()
		q.Table("kv")
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		// Default engine: DoNothing sets ConflictNothing, but since ConflictUpdate is empty
		// and it's MySQL-like, it renders IGNORE
		assert.Contains(t, sql, "INSERT")
		assert.Contains(t, sql, "IGNORE")
	})
}

// TestBuilderCoverageForUpdatePrecedence tests that SkipLocked takes
// precedence over NoWait when both are set (based on the if/else if logic).
func TestBuilderCoverageForUpdatePrecedence(t *testing.T) {
	ctx := context.Background()

	q := psql.B().Select().From("table1")
	q.ForUpdate = true
	q.SkipLocked = true
	q.NoWait = true // Both set; SkipLocked should win

	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "FOR UPDATE")
	assert.Contains(t, sql, "SKIP LOCKED")
	assert.NotContains(t, sql, "NOWAIT")
}

// TestBuilderCoverageMultipleJoinsWithWhere tests chaining multiple joins with WHERE.
func TestBuilderCoverageMultipleJoinsWithWhere(t *testing.T) {
	ctx := context.Background()

	query := psql.B().
		Select(psql.F("u", "name"), psql.F("o", "total"), psql.F("p", "product_name")).
		From("users").
		InnerJoin("orders", psql.Equal(psql.F("users.id"), psql.F("orders.user_id"))).
		InnerJoin("products", psql.Equal(psql.F("orders.product_id"), psql.F("products.id"))).
		Where(map[string]any{"users.active": true}).
		OrderBy(psql.S("orders.total", "DESC")).
		Limit(50)

	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `INNER JOIN "orders"`)
	assert.Contains(t, sql, `INNER JOIN "products"`)
	assert.Contains(t, sql, `"users"."id"="orders"."user_id"`)
	assert.Contains(t, sql, `"orders"."product_id"="products"."id"`)
	assert.Contains(t, sql, "WHERE")
	assert.Contains(t, sql, "ORDER BY")
	assert.Contains(t, sql, "LIMIT 50")
}

// TestBuilderCoverageWhereAND tests WhereAND directly in Where.
func TestBuilderCoverageWhereAND(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select().From("users").
		Where(psql.WhereAND{
			map[string]any{"status": "active"},
			psql.Gt(psql.F("age"), 18),
		})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"status"='active'`)
	assert.Contains(t, sql, `"age">18`)
	assert.Contains(t, sql, "AND")
}
