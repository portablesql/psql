package psql_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPgDialect provides PostgreSQL placeholder and LIMIT behavior for unit tests
// (the real dialect is registered by psql-pgsql via init()).
type testPgDialect struct{}

func (testPgDialect) Placeholder(n int) string { return "$" + strconv.Itoa(n) }
func (testPgDialect) ExportArg(v any) any      { return psql.DefaultExportArg(v) }
func (testPgDialect) LimitOffset(a, b int) string {
	return "LIMIT " + strconv.Itoa(a) + " OFFSET " + strconv.Itoa(b)
}

// testSqliteDialect provides SQLite placeholder and LIMIT behavior for unit tests.
type testSqliteDialect struct{}

func (testSqliteDialect) Placeholder(_ int) string { return "?" }
func (testSqliteDialect) ExportArg(v any) any      { return psql.DefaultExportArg(v) }
func (testSqliteDialect) LimitOffset(a, b int) string {
	return "LIMIT " + strconv.Itoa(a) + " OFFSET " + strconv.Itoa(b)
}

func init() {
	psql.RegisterDialect(psql.EnginePostgreSQL, testPgDialect{})
	psql.RegisterDialect(psql.EngineSQLite, testSqliteDialect{})
}

// ctxForEngine returns a context configured for the given engine.
func ctxForEngine(e psql.Engine) context.Context {
	return psql.NewBackend(e, nil).Plug(context.Background())
}

// === GREATEST / LEAST engine-specific rendering ===

func TestGreatestSQLite(t *testing.T) {
	ctx := ctxForEngine(psql.EngineSQLite)

	query := psql.B().Select(psql.Greatest(psql.F("a"), psql.F("b"))).From("t")
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `MAX("a","b")`)
	assert.NotContains(t, sql, "GREATEST")
}

func TestGreatestPostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	query := psql.B().Select(psql.Greatest(psql.F("a"), psql.F("b"))).From("t")
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `GREATEST("a","b")`)
}

func TestGreatestMySQL(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)

	query := psql.B().Select(psql.Greatest(psql.F("a"), 0)).From("t")
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `GREATEST("a",0)`)
}

func TestLeastSQLite(t *testing.T) {
	ctx := ctxForEngine(psql.EngineSQLite)

	query := psql.B().Select(psql.Least(psql.F("stock"), 100)).From("products")
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `MIN("stock",100)`)
	assert.NotContains(t, sql, "LEAST")
}

func TestLeastPostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	query := psql.B().Select(psql.Least(psql.F("stock"), 100)).From("products")
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `LEAST("stock",100)`)
}

// === GREATEST / LEAST with RenderArgs per engine ===

func TestGreatestRenderArgsSQLite(t *testing.T) {
	ctx := ctxForEngine(psql.EngineSQLite)

	query := psql.B().Select(psql.Greatest(psql.F("score"), 0)).From("players")
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "MAX(")
	assert.Len(t, args, 1)
}

func TestGreatestRenderArgsPostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	query := psql.B().Select(psql.Greatest(psql.F("score"), 0)).From("players")
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "GREATEST(")
	assert.Len(t, args, 1)
}

// === Any engine-specific rendering ===

func TestAnyPostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	query := psql.B().Select().From("users").
		Where(map[string]any{"id": &psql.Any{Values: []int{1, 2, 3}}})
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `= ANY($1)`)
	assert.Len(t, args, 1)
}

func TestAnyMySQL(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)

	query := psql.B().Select().From("users").
		Where(map[string]any{"id": &psql.Any{Values: []int{1, 2, 3}}})
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `IN(?,?,?)`)
	assert.Len(t, args, 3)
}

func TestAnySQLite(t *testing.T) {
	ctx := ctxForEngine(psql.EngineSQLite)

	query := psql.B().Select().From("users").
		Where(map[string]any{"id": &psql.Any{Values: []int{1, 2, 3}}})
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `IN(?,?,?)`)
	assert.Len(t, args, 3)
}

func TestAnyNotPostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	query := psql.B().Select().From("users").
		Where(map[string]any{"id": &psql.Not{V: &psql.Any{Values: []int{1, 2}}}})
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `!= ALL($1)`)
	assert.Len(t, args, 1)
}

// === Like case-insensitive engine-specific rendering ===

func TestCILikePostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	query := psql.B().Select().From("users").
		Where(map[string]any{"name": psql.Like{Like: "john%", CaseInsensitive: true}})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "ILIKE")
	assert.NotContains(t, sql, "COLLATE")
}

func TestCILikeSQLite(t *testing.T) {
	ctx := ctxForEngine(psql.EngineSQLite)

	query := psql.B().Select().From("users").
		Where(map[string]any{"name": psql.Like{Like: "john%", CaseInsensitive: true}})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `LOWER("name") LIKE LOWER('john%')`)
	assert.NotContains(t, sql, "COLLATE")
	assert.NotContains(t, sql, "ILIKE")
}

func TestCILikeMySQL(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)

	query := psql.B().Select().From("users").
		Where(map[string]any{"name": psql.Like{Like: "john%", CaseInsensitive: true}})
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "LIKE")
	assert.NotContains(t, sql, "ILIKE")
	assert.NotContains(t, sql, "COLLATE")
}

// === INSERT engine-specific rendering ===

func TestInsertPostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	q := psql.B().Insert(map[string]any{"id": 1, "name": "test"})
	q.Table("users")
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "INSERT INTO")
	assert.Contains(t, sql, "(") // column-value format
	assert.Contains(t, sql, "VALUES")
}

func TestInsertSQLite(t *testing.T) {
	ctx := ctxForEngine(psql.EngineSQLite)

	q := psql.B().Insert(map[string]any{"id": 1, "name": "test"})
	q.Table("users")
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "INSERT INTO")
	assert.Contains(t, sql, "VALUES")
}

func TestInsertMySQL(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)

	q := psql.B().Insert(map[string]any{"id": 1, "name": "test"})
	q.Table("users")
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "INSERT INTO")
	assert.Contains(t, sql, "SET") // MySQL SET syntax
}

// === Upsert engine-specific rendering ===

func TestUpsertPostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	q := psql.B().Insert(map[string]any{"id": 1, "name": "test"}).
		OnConflict("id").DoUpdate(map[string]any{"name": "test"})
	q.Table("users")
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "ON CONFLICT")
	assert.Contains(t, sql, "DO UPDATE SET")
}

func TestUpsertSQLite(t *testing.T) {
	ctx := ctxForEngine(psql.EngineSQLite)

	q := psql.B().Insert(map[string]any{"id": 1, "name": "test"}).
		OnConflict("id").DoUpdate(map[string]any{"name": "test"})
	q.Table("users")
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "ON CONFLICT")
	assert.Contains(t, sql, "DO UPDATE SET")
}

func TestUpsertMySQL(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)

	q := psql.B().Insert(map[string]any{"id": 1, "name": "test"}).
		OnConflict("id").DoUpdate(map[string]any{"name": "test"})
	q.Table("users")
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "ON DUPLICATE KEY UPDATE")
}

func TestDoNothingPostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	q := psql.B().Insert(map[string]any{"id": 1}).DoNothing()
	q.Table("users")
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "ON CONFLICT DO NOTHING")
}

func TestDoNothingSQLite(t *testing.T) {
	ctx := ctxForEngine(psql.EngineSQLite)

	q := psql.B().Insert(map[string]any{"id": 1}).DoNothing()
	q.Table("users")
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "INSERT OR IGNORE")
}

func TestDoNothingMySQL(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)

	q := psql.B().Insert(map[string]any{"id": 1}).DoNothing()
	q.Table("users")
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "INSERT IGNORE")
}

// === Placeholder style per engine ===

func TestRenderArgsPostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	query := psql.B().Select().From("users").
		Where(map[string]any{"name": "alice", "age": 30})
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "$1")
	assert.Contains(t, sql, "$2")
	assert.Len(t, args, 2)
}

func TestRenderArgsMySQL(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)

	query := psql.B().Select().From("users").
		Where(map[string]any{"name": "alice", "age": 30})
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "?")
	assert.NotContains(t, sql, "$")
	assert.Len(t, args, 2)
}

// === LIMIT/OFFSET engine-specific rendering ===

func TestLimitOffsetPostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	// Limit(offset, count) → LIMIT count OFFSET offset
	query := psql.B().Select().From("users").Limit(10, 20)
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "LIMIT 20 OFFSET 10")
}

func TestLimitOffsetMySQL(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)

	// Limit(offset, count) → LIMIT count OFFSET offset (MySQL accepts this too)
	query := psql.B().Select().From("users").Limit(10, 20)
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "LIMIT 20 OFFSET 10")
}

// === FOR UPDATE not rendered on SQLite ===

func TestForUpdateSQLite(t *testing.T) {
	ctx := ctxForEngine(psql.EngineSQLite)

	query := psql.B().Select().From("jobs").SetForUpdate()
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.NotContains(t, sql, "FOR UPDATE")
}

func TestForUpdatePostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	query := psql.B().Select().From("jobs").SetForUpdate()
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "FOR UPDATE")
}

// === EXISTS / COALESCE / CASE across engines (verify they render the same) ===

func TestExistsPostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	sub := psql.B().Select(psql.Raw("1")).From("orders").
		Where(map[string]any{"status": "active"})
	query := psql.B().Select().From("users").Where(psql.Exists(sub))
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "EXISTS (SELECT 1 FROM")
	assert.Contains(t, sql, "$1") // subquery param uses PG placeholders
	assert.Len(t, args, 1)
}

func TestExistsSQLite(t *testing.T) {
	ctx := ctxForEngine(psql.EngineSQLite)

	sub := psql.B().Select(psql.Raw("1")).From("orders").
		Where(map[string]any{"status": "active"})
	query := psql.B().Select().From("users").Where(psql.Exists(sub))
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "EXISTS (SELECT 1 FROM")
	assert.Contains(t, sql, "?") // SQLite uses ? placeholders
	assert.Len(t, args, 1)
}

func TestCoalesceRenderArgsPostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	query := psql.B().Select(psql.Coalesce(psql.F("name"), "default")).From("users")
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "COALESCE(")
	assert.Contains(t, sql, "$1")
	assert.Len(t, args, 1)
}

func TestCaseRenderArgsPostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	caseExpr := psql.Case().
		When(psql.Gt(psql.F("age"), 18), "adult").
		Else("child")
	query := psql.B().Select("id", caseExpr).From("users")
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "CASE WHEN")
	assert.Contains(t, sql, "$1") // PG placeholders
	assert.Len(t, args, 3)
}

// === Subquery JOIN with RenderArgs per engine ===

func TestSubqueryJoinRenderArgsPostgreSQL(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

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
	assert.Contains(t, sql, `AS "o"`)
	assert.Contains(t, sql, "$1") // PG placeholder for "completed"
	assert.Len(t, args, 1)
}

func TestSubqueryJoinRenderArgsSQLite(t *testing.T) {
	ctx := ctxForEngine(psql.EngineSQLite)

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
	assert.Contains(t, sql, "?") // SQLite placeholder
	assert.Len(t, args, 1)
}

// === Not in parameterized mode ===

func TestNotRenderArgsMySQL(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)

	query := psql.B().Select().From("users").
		Where(map[string]any{"status": &psql.Not{V: "deleted"}})
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"status"!=?`)
	assert.Equal(t, []any{"deleted"}, args)
}

func TestNotRenderArgsPG(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)

	query := psql.B().Select().From("users").
		Where(map[string]any{"status": &psql.Not{V: "deleted"}})
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"status"!=$1`)
	assert.Equal(t, []any{"deleted"}, args)
}

func TestNotInFilterArrayRenderArgs(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)

	// Not used as a standalone expression in a filter array should expand, not become a bind param
	query := psql.B().Select().From("tasks").
		Where(map[string]any{"status": &psql.Not{V: nil}})
	sql, args, err := query.RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"status" IS NOT NULL`)
	assert.Len(t, args, 0)
}
