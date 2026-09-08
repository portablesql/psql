package psql_test

import (
	"context"
	"testing"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuilderDistinct(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select("name").From("users").SetDistinct()
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT DISTINCT "name" FROM "users"`, sql)
}

func TestBuilderGroupBy(t *testing.T) {
	ctx := context.Background()

	t.Run("simple group by", func(t *testing.T) {
		query := psql.B().Select("status", psql.Raw("COUNT(*)")).From("users").GroupByFields("status")
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT "status",COUNT(*) FROM "users" GROUP BY "status"`, sql)
	})

	t.Run("multiple group by fields", func(t *testing.T) {
		query := psql.B().Select("status", "role", psql.Raw("COUNT(*)")).From("users").GroupByFields("status", "role")
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT "status","role",COUNT(*) FROM "users" GROUP BY "status","role"`, sql)
	})
}

func TestBuilderHaving(t *testing.T) {
	ctx := context.Background()

	query := psql.B().
		Select("status", psql.Raw("COUNT(*) as cnt")).
		From("users").
		GroupByFields("status").
		Having(psql.Gt(psql.Raw("COUNT(*)"), 5))
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `GROUP BY "status"`)
	assert.Contains(t, sql, `HAVING (COUNT(*)>5)`)
}

func TestBuilderJoin(t *testing.T) {
	ctx := context.Background()

	t.Run("inner join", func(t *testing.T) {
		query := psql.B().
			Select(psql.F("users", "name"), psql.F("orders", "total")).
			From("users").
			InnerJoin("orders", psql.Equal(psql.F("users.id"), psql.F("orders.user_id")))
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `INNER JOIN "orders"`)
		assert.Contains(t, sql, `"users"."id"="orders"."user_id"`)
	})

	t.Run("left join", func(t *testing.T) {
		query := psql.B().
			Select().
			From("users").
			LeftJoin("profiles", psql.Equal(psql.F("users.id"), psql.F("profiles.user_id")))
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `LEFT JOIN "profiles"`)
		assert.Contains(t, sql, `"users"."id"="profiles"."user_id"`)
	})

	t.Run("right join", func(t *testing.T) {
		query := psql.B().
			Select().
			From("users").
			RightJoin("orders", psql.Equal(psql.F("users.id"), psql.F("orders.user_id")))
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `RIGHT JOIN "orders"`)
	})

	t.Run("multiple joins", func(t *testing.T) {
		query := psql.B().
			Select().
			From("users").
			InnerJoin("orders", psql.Equal(psql.F("users.id"), psql.F("orders.user_id"))).
			LeftJoin("items", psql.Equal(psql.F("orders.id"), psql.F("items.order_id")))
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `INNER JOIN "orders"`)
		assert.Contains(t, sql, `LEFT JOIN "items"`)
	})
}

func TestBuilderDelete(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Delete().From("users").Where(map[string]any{"id": 42})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `DELETE FROM "users" WHERE ("id"=42)`, sql)
}

func TestBuilderReplace(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Update("users").Set(map[string]any{"name": "John"}).Where(map[string]any{"id": 1})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `UPDATE "users"`)
	assert.Contains(t, sql, `SET`)
	assert.Contains(t, sql, `"name"='John'`)
	assert.Contains(t, sql, `WHERE ("id"=1)`)
}

func TestBuilderBetween(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select().From("products").
		Where(psql.Between(psql.F("price"), 10, 100))
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"price" BETWEEN 10 AND 100`)
}

func TestBuilderNot(t *testing.T) {
	ctx := context.Background()

	t.Run("NOT with IS NULL", func(t *testing.T) {
		query := psql.B().Select().From("users").
			Where(map[string]any{"deleted_at": &psql.Not{V: nil}})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users" WHERE ("deleted_at" IS NOT NULL)`, sql)
	})

	t.Run("NOT with equality", func(t *testing.T) {
		query := psql.B().Select().From("users").
			Where(map[string]any{"status": &psql.Not{V: "active"}})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users" WHERE ("status"!='active')`, sql)
	})

	t.Run("NOT with LIKE", func(t *testing.T) {
		query := psql.B().Select().From("users").
			Where(map[string]any{"name": &psql.Not{V: psql.Like{Field: psql.F("name"), Like: "test%"}}})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `NOT LIKE`)
	})
}

func TestBuilderIN(t *testing.T) {
	ctx := context.Background()

	t.Run("IN with []any", func(t *testing.T) {
		query := psql.B().Select().From("users").
			Where(map[string]any{"id": []any{1, 2, 3}})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users" WHERE ("id" IN(1,2,3))`, sql)
	})

	t.Run("IN with []string", func(t *testing.T) {
		query := psql.B().Select().From("users").
			Where(map[string]any{"status": []string{"active", "pending"}})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users" WHERE ("status" IN('active','pending'))`, sql)
	})

	t.Run("empty IN is FALSE", func(t *testing.T) {
		query := psql.B().Select().From("users").
			Where(map[string]any{"id": []any{}})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "FALSE")
	})
}

func TestBuilderMongoStyle(t *testing.T) {
	ctx := context.Background()

	t.Run("$gt", func(t *testing.T) {
		query := psql.B().Select().From("users").
			Where(map[string]any{"age": map[string]any{"$gt": 18}})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"age">18`)
	})

	t.Run("$gte and $lte combined", func(t *testing.T) {
		query := psql.B().Select().From("users").
			Where(map[string]any{"age": map[string]any{"$gte": 18, "$lte": 65}})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"age">=18`)
		assert.Contains(t, sql, `"age"<=65`)
	})
}

func TestBuilderForUpdate(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select().From("users").Where(map[string]any{"id": 1})
	query.ForUpdate = true
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "users" WHERE ("id"=1) FOR UPDATE`, sql)
}

func TestBuilderAlsoSelect(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select("id").From("users").AlsoSelect(psql.F("name"))
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT "id","name" FROM "users"`, sql)
}

func TestBuilderUpdateIgnore(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Update("users").Set(map[string]any{"name": "John"}).Where(map[string]any{"id": 1})
	query.UpdateIgnore = true
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `UPDATE IGNORE "users"`)
}

func TestBuilderEmptyWhere(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select().From("users").Where(map[string]any{})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "users" WHERE (TRUE)`, sql)
}

func TestBuilderChaining(t *testing.T) {
	ctx := context.Background()

	// Test that chaining Where calls adds AND conditions
	query := psql.B().Select().From("users").
		Where(map[string]any{"status": "active"}).
		Where(psql.Gt(psql.F("age"), 18))
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"status"='active'`)
	assert.Contains(t, sql, `AND`)
	assert.Contains(t, sql, `"age">18`)
}

func TestBuilderLimitClear(t *testing.T) {
	ctx := context.Background()

	// Test clearing limit
	query := psql.B().Select().From("users").Limit(10).Limit()
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "users"`, sql)
}
