package psql_test

import (
	"context"
	"testing"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// === EXISTS / NOT EXISTS tests ===

func TestExists(t *testing.T) {
	ctx := context.Background()

	sub := psql.B().Select(psql.Raw("1")).From("orders").
		Where(psql.Equal(psql.F("orders.user_id"), psql.F("users.id")))
	query := psql.B().Select().From("users").Where(psql.Exists(sub))
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "users" WHERE (EXISTS (SELECT 1 FROM "orders" WHERE ("orders"."user_id"="users"."id")))`, sql)
}

func TestNotExists(t *testing.T) {
	ctx := context.Background()

	sub := psql.B().Select(psql.Raw("1")).From("subscriptions").
		Where(psql.Equal(psql.F("subscriptions.channel_id"), psql.F("channels.id")))
	query := psql.B().Select().From("channels").Where(psql.NotExists(sub))
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "channels" WHERE (NOT EXISTS (SELECT 1 FROM "subscriptions" WHERE ("subscriptions"."channel_id"="channels"."id")))`, sql)
}

func TestExistsRenderArgs(t *testing.T) {
	ctx := context.Background()

	sub := psql.B().Select(psql.Raw("1")).From("orders").
		Where(map[string]any{"status": "active"})
	query := psql.B().Select().From("users").Where(psql.Exists(sub))
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "EXISTS (SELECT 1 FROM")
	assert.Len(t, args, 1)
	assert.Equal(t, "active", args[0])
}

func TestExistsWithAND(t *testing.T) {
	ctx := context.Background()

	sub := psql.B().Select(psql.Raw("1")).From("orders").
		Where(psql.Equal(psql.F("orders.user_id"), psql.F("users.id")))
	query := psql.B().Select().From("users").
		Where(map[string]any{"active": true}).
		Where(psql.Exists(sub))
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `("active"=TRUE)`)
	assert.Contains(t, sql, `EXISTS (SELECT 1`)
}

// === CASE WHEN tests ===

func TestCaseSearched(t *testing.T) {
	ctx := context.Background()

	caseExpr := psql.Case().
		When(psql.Gt(psql.F("age"), 18), "adult").
		When(psql.Gt(psql.F("age"), 12), "teen").
		Else("child")

	query := psql.B().Select("id", caseExpr).From("users")
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `CASE WHEN "age">18 THEN 'adult' WHEN "age">12 THEN 'teen' ELSE 'child' END`)
}

func TestCaseSimple(t *testing.T) {
	ctx := context.Background()

	caseExpr := psql.Case(psql.F("status")).
		When("active", 1).
		When("inactive", 0).
		Else(-1)

	query := psql.B().Select("id", caseExpr).From("users")
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `CASE "status" WHEN 'active' THEN 1 WHEN 'inactive' THEN 0 ELSE -1 END`)
}

func TestCaseNoElse(t *testing.T) {
	ctx := context.Background()

	caseExpr := psql.Case().
		When(psql.Equal(psql.F("type"), "admin"), "yes")

	query := psql.B().Select("id", caseExpr).From("users")
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `CASE WHEN "type"='admin' THEN 'yes' END`)
	assert.NotContains(t, sql, "ELSE")
}

func TestCaseInUpdate(t *testing.T) {
	ctx := context.Background()

	caseExpr := psql.Case().
		When(psql.Gt(psql.F("stock"), 0), psql.Raw(`"stock" - 1`)).
		Else(0)

	query := psql.B().Update("products").
		Set(map[string]any{"stock": &psql.SetRaw{SQL: caseExpr.EscapeValue()}}).
		Where(map[string]any{"id": 42})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "CASE WHEN")
	assert.Contains(t, sql, "END")
}

func TestCaseRenderArgs(t *testing.T) {
	ctx := context.Background()

	caseExpr := psql.Case().
		When(psql.Gt(psql.F("age"), 18), "adult").
		Else("child")

	query := psql.B().Select("id", caseExpr).From("users")
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "CASE WHEN")
	assert.Len(t, args, 3) // 18, "adult", "child"
}

// === COALESCE tests ===

func TestCoalesce(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select("id", psql.Coalesce(psql.F("nickname"), psql.F("name"))).From("users")
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `COALESCE("nickname","name")`)
}

func TestCoalesceWithValue(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select(psql.Coalesce(psql.F("display_name"), "Anonymous")).From("users")
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `COALESCE("display_name",'Anonymous')`)
}

func TestCoalesceWithSubquery(t *testing.T) {
	ctx := context.Background()

	sub := psql.B().Select(psql.Raw("COUNT(*)")).From("messages").
		Where(psql.Equal(psql.F("messages.thread_id"), psql.F("threads.id")))
	query := psql.B().Select("id", psql.Coalesce(sub, 0)).From("threads")
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `COALESCE((SELECT COUNT(*) FROM "messages"`)
	assert.Contains(t, sql, `,0)`)
}

func TestCoalesceRenderArgs(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select(psql.Coalesce(psql.F("name"), "default")).From("users")
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "COALESCE(")
	assert.Len(t, args, 1)
	assert.Equal(t, "default", args[0])
}

// === GREATEST / LEAST tests ===

func TestGreatest(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Update("channels").
		Set(map[string]any{"user_count": &psql.SetRaw{SQL: psql.Greatest(psql.Raw(`"user_count" - 1`), 0).EscapeValue()}}).
		Where(map[string]any{"id": 1})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `GREATEST("user_count" - 1,0)`)
}

func TestGreatestMultiple(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select(psql.Greatest(psql.F("a"), psql.F("b"), psql.F("c"))).From("t")
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `GREATEST("a","b","c")`)
}

func TestLeast(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select(psql.Least(psql.F("stock"), 100)).From("products")
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `LEAST("stock",100)`)
}

func TestGreatestRenderArgs(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select(psql.Greatest(psql.F("score"), 0)).From("players")
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "GREATEST(")
	assert.Len(t, args, 1) // 0 is parameterized
}

// === INSERT ... SELECT tests ===

func TestInsertSelect(t *testing.T) {
	ctx := context.Background()

	query := psql.B().InsertSelect("archive").
		Select("id", "name", "email").
		From("users").
		Where(map[string]any{"active": false})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "archive" SELECT "id","name","email" FROM "users" WHERE ("active"=FALSE)`, sql)
}

func TestInsertSelectRenderArgs(t *testing.T) {
	ctx := context.Background()

	query := psql.B().InsertSelect("archive").
		Select("id", "name").
		From("users").
		Where(map[string]any{"status": "deleted"})
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `INSERT INTO "archive" SELECT`)
	assert.Contains(t, sql, `FROM "users"`)
	assert.Len(t, args, 1)
	assert.Equal(t, "deleted", args[0])
}

func TestInsertSelectMissingSource(t *testing.T) {
	ctx := context.Background()

	// Only destination table, no source — should error
	query := psql.B().InsertSelect("archive")
	_, err := query.Render(ctx)
	assert.Error(t, err)
}

// === Subquery JOIN tests ===

func TestLeftJoinSubquery(t *testing.T) {
	ctx := context.Background()

	sub := psql.B().Select("user_id", psql.Raw("COUNT(*) AS vote_count")).
		From("votes").GroupByFields("user_id")

	query := psql.B().Select("u.*", psql.Raw(`"vc"."vote_count"`)).
		From("users AS u").
		LeftJoin(psql.SubTable(sub, "vc"),
			psql.Equal(psql.F("u.id"), psql.F("vc.user_id")))

	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `LEFT JOIN (SELECT "user_id",COUNT(*) AS vote_count FROM "votes" GROUP BY "user_id") AS "vc"`)
	assert.Contains(t, sql, `ON "u"."id"="vc"."user_id"`)
}

func TestInnerJoinSubquery(t *testing.T) {
	ctx := context.Background()

	sub := psql.B().Select("category_id", psql.Raw("MAX(price) AS max_price")).
		From("products").GroupByFields("category_id")

	query := psql.B().Select("c.name", psql.Raw(`"p"."max_price"`)).
		From("categories AS c").
		InnerJoin(psql.SubTable(sub, "p"),
			psql.Equal(psql.F("c.id"), psql.F("p.category_id")))

	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `INNER JOIN (SELECT`)
	assert.Contains(t, sql, `AS "p"`)
}

func TestJoinSubqueryRenderArgs(t *testing.T) {
	ctx := context.Background()

	sub := psql.B().Select("user_id", psql.Raw("COUNT(*) AS cnt")).
		From("orders").
		Where(map[string]any{"status": "completed"}).
		GroupByFields("user_id")

	query := psql.B().Select("u.name", psql.Raw(`"o"."cnt"`)).
		From("users AS u").
		LeftJoin(psql.SubTable(sub, "o"),
			psql.Equal(psql.F("u.id"), psql.F("o.user_id")))

	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "LEFT JOIN (SELECT")
	assert.Len(t, args, 1)
	assert.Equal(t, "completed", args[0])
}

func TestJoinStringStillWorks(t *testing.T) {
	ctx := context.Background()

	// Verify backward compatibility: string table names still work
	query := psql.B().Select().From("users").
		LeftJoin("orders", psql.Equal(psql.F("orders.user_id"), psql.F("users.id")))
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `LEFT JOIN "orders" ON`)
}

// === SubTable in FROM tests ===

func TestSubTableInFrom(t *testing.T) {
	ctx := context.Background()

	sub := psql.B().Select("user_id", psql.Raw("COUNT(*) AS cnt")).
		From("orders").GroupByFields("user_id")

	query := psql.B().Select("user_id", psql.Raw(`"cnt"`)).
		From(psql.SubTable(sub, "order_counts")).
		Where(map[string]any{"cnt": psql.Gt(nil, 5)})

	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `FROM (SELECT "user_id",COUNT(*) AS cnt FROM "orders" GROUP BY "user_id") AS "order_counts"`)
}

// === Combined feature tests ===

func TestExistsWithCoalesce(t *testing.T) {
	ctx := context.Background()

	// A realistic query combining EXISTS and COALESCE
	existsSub := psql.B().Select(psql.Raw("1")).From("orders").
		Where(psql.Equal(psql.F("orders.user_id"), psql.F("users.id")))

	query := psql.B().Select(
		"id",
		psql.Coalesce(psql.F("display_name"), psql.F("name")),
	).From("users").Where(psql.Exists(existsSub))

	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "EXISTS")
	assert.Contains(t, sql, "COALESCE")
}

// === []byte in SET and WHERE tests ===

func TestByteSliceInSet(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Update("files").
		Set(map[string]any{"data": []byte{0xff, 0x00, 0xbe, 0xef}}).
		Where(map[string]any{"id": 1})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"data"=x'ff00beef'`)
}

func TestByteSliceInWhere(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select().From("files").
		Where(map[string]any{"hash": []byte{0xde, 0xad}})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"hash"=x'dead'`)
}

func TestByteSliceNotInWhere(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Select().From("files").
		Where(map[string]any{"hash": &psql.Not{V: []byte{0xde, 0xad}}})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"hash"!=x'dead'`)
}

func TestByteSliceNilInSet(t *testing.T) {
	ctx := context.Background()

	// nil []byte is treated as NULL: SET clauses render an assignment, not a condition
	query := psql.B().Update("files").
		Set(map[string]any{"data": []byte(nil)}).
		Where(map[string]any{"id": 1})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"data"=NULL`)
}

func TestByteSliceEmptyInSet(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Update("files").
		Set(map[string]any{"data": []byte{}}).
		Where(map[string]any{"id": 1})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"data"=x''`)
}

func TestByteSliceRenderArgs(t *testing.T) {
	ctx := context.Background()

	query := psql.B().Update("files").
		Set(map[string]any{"data": []byte{0xff}}).
		Where(map[string]any{"id": 1})
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"data"=?`)
	assert.Len(t, args, 2) // []byte{0xff} and 1
}

func TestGreatestWithDecrement(t *testing.T) {
	ctx := context.Background()

	// user_count = GREATEST(user_count - 1, 0)
	query := psql.B().Update("channels").
		Set(map[string]any{
			"user_count": &psql.SetRaw{
				SQL: psql.Greatest(psql.Raw(`"user_count" - 1`), 0).EscapeValue(),
			},
		}).
		Where(map[string]any{"id": 42})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `GREATEST("user_count" - 1,0)`)
}
