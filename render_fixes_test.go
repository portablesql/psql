package psql_test

import (
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for the rendering fixes. Fake backends are used for each engine
// (see ctxForEngine in engine_render_test.go).

var allEngines = []psql.Engine{psql.EngineMySQL, psql.EnginePostgreSQL, psql.EngineSQLite}

// --- 1. LIMIT semantics ---

func TestFixLimitOffsetAllEngines(t *testing.T) {
	for _, e := range allEngines {
		ctx := ctxForEngine(e)
		// Limit(offset, count)
		sql, err := psql.B().Select().From("users").Limit(10, 20).Render(ctx)
		require.NoError(t, err, e)
		assert.Equal(t, `SELECT * FROM "users" LIMIT 20 OFFSET 10`, sql, e)

		sql, _, err = psql.B().Select().From("users").Limit(10, 20).RenderArgs(ctx)
		require.NoError(t, err, e)
		assert.Equal(t, `SELECT * FROM "users" LIMIT 20 OFFSET 10`, sql, e)

		sql, err = psql.B().Select().From("users").Limit(7).Render(ctx)
		require.NoError(t, err, e)
		assert.Equal(t, `SELECT * FROM "users" LIMIT 7`, sql, e)
	}
}

// --- 2. Stringer / Valuer escaping ---

type injectingStringer string

func (s injectingStringer) String() string { return string(s) }

type failingValuer struct{}

func (failingValuer) Value() (driver.Value, error) { return nil, errors.New("boom") }

func TestFixStringerIsEscaped(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)
	sql, err := psql.B().Select().From("t").
		Where(map[string]any{"s": injectingStringer("x' OR 1=1 --")}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("s"='x'' OR 1=1 --')`, sql)
	assert.Equal(t, `'x'' OR 1=1 --'`, psql.Escape(injectingStringer("x' OR 1=1 --")))
}

func TestFixTimePointerRendersAsTime(t *testing.T) {
	tm := time.Date(2024, 1, 15, 12, 30, 45, 0, time.UTC)
	assert.Equal(t, `'2024-01-15 12:30:45'`, psql.Escape(&tm))
	var nilTime *time.Time
	assert.Equal(t, `NULL`, psql.Escape(nilTime))
}

func TestFixValuerErrorPropagates(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)
	_, err := psql.B().Select().From("t").Where(map[string]any{"v": failingValuer{}}).Render(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

// --- 3. SET clause rendering ---

func TestFixSetNilAndSlices(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)
	sql, err := psql.B().Update("t").Set(map[string]any{
		"a":    nil,
		"b":    []byte{0xff},
		"tags": psql.Set{"x", "y"},
		"vec":  psql.Vector{1, 2},
		"n":    []byte(nil),
	}).Where(map[string]any{"id": 1}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `UPDATE "t" SET "a"=NULL,"b"=x'ff',"n"=NULL,"tags"='x,y',"vec"='[1,2]' WHERE ("id"=1)`, sql)

	sql, args, err := psql.B().Update("t").Set(map[string]any{"a": nil, "tags": psql.Set{"x", "y"}}).
		Where(map[string]any{"id": 1}).RenderArgs(ctx)
	require.NoError(t, err)
	assert.Equal(t, `UPDATE "t" SET "a"=?,"tags"=? WHERE ("id"=?)`, sql)
	require.Len(t, args, 3)
	assert.Nil(t, args[0])
	assert.Equal(t, psql.Set{"x", "y"}, args[1])
}

func TestFixSetExpressions(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)
	sql, err := psql.B().Update("t").Set(map[string]any{
		"views":   psql.Incr(1),
		"stock":   psql.Decr(2),
		"updated": &psql.SetRaw{SQL: "NOW()"},
		"other":   psql.F("source"),
		"sub":     psql.B().Select(psql.Raw("COUNT(*)")).From("u"),
	}).Where(map[string]any{"id": 1}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `UPDATE "t" SET "other"="source","stock"="stock"-(2),"sub"=(SELECT COUNT(*) FROM "u"),"updated"=NOW(),"views"="views"+(1) WHERE ("id"=1)`, sql)
}

func TestFixSetRejectsBareString(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)
	_, err := psql.B().Update("t").Set("a=1").Render(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bare string")

	_, err = psql.B().Update("t").Render(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no fields to set")

	// raw expressions are still accepted
	sql, err := psql.B().Update("t").Set(psql.Raw(`"a"="a"*2`)).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `UPDATE "t" SET "a"="a"*2`, sql)
}

func TestFixInsertSetMySQL(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)
	q := psql.B().Insert(map[string]any{"a": nil, "tags": psql.Set{"x"}, "n": psql.Incr(1)})
	q.Table("t")
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "t" SET "a"=NULL,"n"="n"+(1),"tags"='x'`, sql)
}

func TestFixInsertColsValsExpressions(t *testing.T) {
	for _, e := range []psql.Engine{psql.EnginePostgreSQL, psql.EngineSQLite} {
		ctx := ctxForEngine(e)
		q := psql.B().Insert(map[string]any{"a": nil, "b": &psql.SetRaw{SQL: "NOW()"}, "c": psql.Incr(1), "d": psql.Set{"x", "y"}})
		q.Table("t")
		sql, err := q.Render(ctx)
		require.NoError(t, err, e)
		assert.Equal(t, `INSERT INTO "t" ("a","b","c","d") VALUES (NULL,NOW(),"c"+(1),'x,y')`, sql, e)

		// non-map entries are an error instead of being silently dropped
		q = psql.B().Insert(psql.Raw("x"))
		q.Table("t")
		_, err = q.Render(ctx)
		require.Error(t, err, e)

		q = psql.B().Insert()
		q.Table("t")
		_, err = q.Render(ctx)
		require.Error(t, err, e)
	}
}

func TestFixOnConflictUpdateUsesAssignments(t *testing.T) {
	q := psql.B().Insert(map[string]any{"id": 1, "hits": 1}).OnConflict("id").
		DoUpdate(map[string]any{"hits": psql.Incr(1), "note": nil})
	q.Table("t")

	sql, err := q.Render(ctxForEngine(psql.EnginePostgreSQL))
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "t" ("hits","id") VALUES (1,1) ON CONFLICT ("id") DO UPDATE SET "hits"="hits"+(1),"note"=NULL`, sql)

	sql, err = q.Render(ctxForEngine(psql.EngineSQLite))
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "t" ("hits","id") VALUES (1,1) ON CONFLICT ("id") DO UPDATE SET "hits"="hits"+(1),"note"=NULL`, sql)

	sql, err = q.Render(ctxForEngine(psql.EngineMySQL))
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "t" SET "hits"=1,"id"=1 ON DUPLICATE KEY UPDATE "hits"="hits"+(1),"note"=NULL`, sql)
}

func TestFixSetExprInWhereIsError(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)
	_, err := psql.B().Select().From("t").Where(map[string]any{"a": psql.Incr(1)}).Render(ctx)
	require.Error(t, err)
}

// --- 4. Not handling ---

func TestFixNotEmptySliceIsTrue(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)
	sql, err := psql.B().Select().From("t").Where(map[string]any{"a": &psql.Not{V: []int{}}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (TRUE)`, sql)

	sql, err = psql.B().Select().From("t").Where(map[string]any{"a": &psql.Not{V: &psql.Any{Values: []int{}}}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (TRUE)`, sql)

	sql, err = psql.B().Select().From("t").Where(map[string]any{"a": &psql.Not{V: []string{}}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (TRUE)`, sql)

	// non-negated stays FALSE
	sql, err = psql.B().Select().From("t").Where(map[string]any{"a": []int{}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (FALSE)`, sql)
}

func TestFixNotOperatorMap(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)
	sql, err := psql.B().Select().From("t").Where(map[string]any{"a": &psql.Not{V: map[string]any{"$gt": 5}}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (NOT ("a">5))`, sql)

	sql, err = psql.B().Select().From("t").Where(map[string]any{"a": &psql.Not{V: map[string]any{"$lt": 9, "$gt": 5}}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (NOT ("a">5 AND "a"<9))`, sql)
}

func TestFixNotNestedGroups(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)
	sql, err := psql.B().Select().From("t").Where(map[string]any{"a": &psql.Not{V: psql.WhereOR{1, 2}}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (NOT ("a"=1 OR "a"=2))`, sql)

	sql, err = psql.B().Select().From("t").Where(map[string]any{"a": &psql.Not{V: psql.WhereAND{map[string]any{"$gt": 1}, map[string]any{"$lt": 5}}}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (NOT ("a">1 AND "a"<5))`, sql)

	// negated empty groups
	sql, err = psql.B().Select().From("t").Where(map[string]any{"a": &psql.Not{V: psql.WhereOR{}}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (TRUE)`, sql)
	sql, err = psql.B().Select().From("t").Where(map[string]any{"a": &psql.Not{V: psql.WhereAND{}}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (FALSE)`, sql)
}

func TestFixNotUnknownComparisonOp(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)
	_, err := psql.B().Select().From("t").
		Where(map[string]any{"a": &psql.Not{V: &psql.Comparison{Op: "LIKE", B: "x"}}}).Render(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported comparison operator")

	_, err = psql.B().Select().From("t").Where(&psql.Comparison{A: psql.F("a"), B: 1}).Render(ctx)
	require.Error(t, err)

	// known operators still negate
	sql, err := psql.B().Select().From("t").
		Where(map[string]any{"a": &psql.Not{V: &psql.Comparison{Op: "<>", B: "x"}}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("a" = 'x')`, sql)
}

// --- 5. Empty WHERE / groups ---

func TestFixEmptyWhereRendersTrue(t *testing.T) {
	for _, e := range allEngines {
		ctx := ctxForEngine(e)
		sql, err := psql.B().Select().From("t").Where(map[string]any{}).Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "t" WHERE (TRUE)`, sql, e)

		sql, err = psql.B().Select().From("t").Where(psql.WhereOR{}).Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "t" WHERE (FALSE)`, sql, e)

		sql, err = psql.B().Select().From("t").Where(psql.WhereAND{}).Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "t" WHERE (TRUE)`, sql, e)

		sql, err = psql.B().Select().From("t").Where(map[string]any{"a": psql.WhereOR{}, "b": psql.WhereAND{}}).Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "t" WHERE (FALSE AND TRUE)`, sql, e)
	}
	assert.Equal(t, "TRUE", psql.WhereAND{}.String())
	assert.Equal(t, "FALSE", psql.WhereOR{}.String())
}

// --- 6. Bare string conditions ---

func TestFixBareStringConditionIsError(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)
	_, err := psql.B().Select().From("t").Where("a=1").Render(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "psql.Raw")

	_, err = psql.B().Select().From("t").GroupByFields("a").Having("COUNT(*)>1").Render(ctx)
	require.Error(t, err)

	_, err = psql.B().Select().From("t").LeftJoin("u", "u.id=t.id").Render(ctx)
	require.Error(t, err)

	// direct WhereData manipulation is caught at render time
	q := psql.B().Select().From("t")
	q.WhereData = psql.WhereAND{"a=1"}
	_, err = q.Render(ctx)
	require.Error(t, err)

	q = psql.B().Select().From("t").Where(psql.WhereOR{"a=1"})
	_, err = q.Render(ctx)
	require.Error(t, err)

	// Raw and map forms keep working
	sql, err := psql.B().Select().From("t").Where(psql.Raw("a=1"), map[string]any{"b": 2}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (a=1) AND ("b"=2)`, sql)

	sql, _, err = psql.B().Select().From("t").LeftJoin("u", psql.Equal(psql.F("u.id"), psql.F("t.id"))).RenderArgs(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" LEFT JOIN "u" ON "u"."id"="t"."id"`, sql)
}

// --- 7. nil render context / subquery errors ---

func TestFixNilContextDoesNotPanic(t *testing.T) {
	sub := psql.B().Select("id").From("u").Where(map[string]any{"a": 1})
	assert.Equal(t, `(SELECT "id" FROM "u" WHERE ("a"=1))`, psql.Escape(sub))
	assert.Equal(t, `(SELECT "id" FROM "u" WHERE ("a"=1))`, sub.EscapeValue())
	assert.Equal(t, `EXISTS (SELECT "id" FROM "u" WHERE ("a"=1))`, psql.Exists(sub).EscapeValue())
	assert.Equal(t, `NOT EXISTS (SELECT "id" FROM "u" WHERE ("a"=1))`, psql.NotExists(sub).EscapeValue())
	assert.Equal(t, `COALESCE((SELECT "id" FROM "u" WHERE ("a"=1)),0)`, psql.Coalesce(sub, 0).EscapeValue())
	assert.Equal(t, `((SELECT "id" FROM "u" WHERE ("a"=1)))`, psql.WhereAND{sub}.String())
	assert.Equal(t, `(SELECT "id" FROM "u" WHERE ("a"=1)) AS "s"`, psql.SubTable(sub, "s").EscapeTable())
}

func TestFixSubqueryErrorPropagates(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)
	bad := psql.B().Select("id").From("u").Where("broken")

	_, err := psql.B().Select().From("t").Where(map[string]any{"id": &psql.SubIn{Sub: bad}}).Render(ctx)
	require.Error(t, err)

	_, err = psql.B().Select().From("t").Where(psql.Exists(bad)).Render(ctx)
	require.Error(t, err)

	_, err = psql.B().Select(psql.Coalesce(bad, 0)).From("t").Render(ctx)
	require.Error(t, err)

	_, _, err = psql.B().Select().From("t").LeftJoin(psql.SubTable(bad, "s"), psql.Equal(psql.F("s.id"), psql.F("t.id"))).RenderArgs(ctx)
	require.Error(t, err)
}

// --- 8. engine-aware bytes and times ---

func TestFixBytesPerEngine(t *testing.T) {
	data := []byte{0xff, 0x00}
	sql, err := psql.B().Select().From("t").Where(map[string]any{"d": data}).Render(ctxForEngine(psql.EnginePostgreSQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("d"='\xff00'::bytea)`, sql)

	sql, err = psql.B().Select().From("t").Where(map[string]any{"d": []byte{}}).Render(ctxForEngine(psql.EnginePostgreSQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("d"='\x'::bytea)`, sql)

	for _, e := range []psql.Engine{psql.EngineMySQL, psql.EngineSQLite} {
		sql, err = psql.B().Select().From("t").Where(map[string]any{"d": data}).Render(ctxForEngine(e))
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "t" WHERE ("d"=x'ff00')`, sql, e)
	}
}

func TestFixTimePerEngine(t *testing.T) {
	tm := time.Date(2024, 1, 15, 12, 30, 45, 123456000, time.FixedZone("JST", 9*3600))
	var zero time.Time

	sql, err := psql.B().Select().From("t").Where(map[string]any{"a": tm, "z": zero}).Render(ctxForEngine(psql.EnginePostgreSQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("a"='2024-01-15 03:30:45.123456' AND "z"='0001-01-01 00:00:00')`, sql)

	sql, err = psql.B().Select().From("t").Where(map[string]any{"a": tm, "z": zero}).Render(ctxForEngine(psql.EngineSQLite))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("a"='2024-01-15T03:30:45.123456000Z' AND "z"='0001-01-01T00:00:00.000000000Z')`, sql)

	sql, err = psql.B().Select().From("t").Where(map[string]any{"a": tm, "z": zero}).Render(ctxForEngine(psql.EngineMySQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("a"='2024-01-15 03:30:45.123456' AND "z"='0000-00-00 00:00:00.000000')`, sql)

	// pointer to time on SQLite goes through the same path
	sql, err = psql.B().Select().From("t").Where(map[string]any{"a": &tm}).Render(ctxForEngine(psql.EngineSQLite))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("a"='2024-01-15T03:30:45.123456000Z')`, sql)
}

// --- 9. engine-specific errors ---

func TestFixDeleteUpdateLimitPostgreSQL(t *testing.T) {
	pg := ctxForEngine(psql.EnginePostgreSQL)
	_, err := psql.B().Delete().From("t").Where(map[string]any{"a": 1}).Limit(1).Render(pg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LIMIT")

	_, err = psql.B().Update("t").Set(map[string]any{"a": 1}).Limit(1).Render(pg)
	require.Error(t, err)

	// SQLite (modernc build) has no DELETE ... LIMIT either
	_, err = psql.B().Delete().From("t").Where(map[string]any{"a": 1}).Limit(1).Render(ctxForEngine(psql.EngineSQLite))
	require.Error(t, err)

	// still fine on MySQL
	sql, err := psql.B().Delete().From("t").Where(map[string]any{"a": 1}).Limit(1).Render(ctxForEngine(psql.EngineMySQL))
	require.NoError(t, err)
	assert.Equal(t, `DELETE FROM "t" WHERE ("a"=1) LIMIT 1`, sql)
	// SELECT ... LIMIT on PG is fine
	_, err = psql.B().Select().From("t").Limit(1).Render(pg)
	require.NoError(t, err)
}

func TestFixDoUpdateWithoutOnConflict(t *testing.T) {
	for _, e := range []psql.Engine{psql.EnginePostgreSQL, psql.EngineSQLite} {
		q := psql.B().Insert(map[string]any{"id": 1}).DoUpdate(map[string]any{"id": 2})
		q.Table("t")
		_, err := q.Render(ctxForEngine(e))
		require.Error(t, err, e)
		assert.Contains(t, err.Error(), "OnConflict")
	}
	q := psql.B().Insert(map[string]any{"id": 1}).DoUpdate(map[string]any{"id": 2})
	q.Table("t")
	sql, err := q.Render(ctxForEngine(psql.EngineMySQL))
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "t" SET "id"=1 ON DUPLICATE KEY UPDATE "id"=2`, sql)
}

// --- 10. DefaultExportArg ---

func TestFixDefaultExportArg(t *testing.T) {
	assert.Nil(t, psql.DefaultExportArg(psql.Vector(nil)))
	assert.Nil(t, psql.DefaultExportArg(psql.Set(nil)))
	assert.Nil(t, psql.DefaultExportArg([]int(nil)))

	tm := time.Date(2024, 1, 15, 12, 30, 45, 0, time.UTC)
	assert.Equal(t, tm, psql.DefaultExportArg(tm))
	assert.Equal(t, tm, psql.DefaultExportArg(&tm))

	// Valuers are passed through for the driver, even when they are Stringers
	v := psql.Vector{1, 2}
	assert.Equal(t, v, psql.DefaultExportArg(v))
	assert.Equal(t, psql.Set{"a"}, psql.DefaultExportArg(psql.Set{"a"}))

	// plain Stringers export their String()
	assert.Equal(t, "hello", psql.DefaultExportArg(injectingStringer("hello")))
}

// --- 11. Hex ---

func TestFixHexValueReceiver(t *testing.T) {
	h := psql.Hex{0xca, 0xfe}
	var valuer driver.Valuer = h // value, not pointer
	v, err := valuer.Value()
	require.NoError(t, err)
	assert.Equal(t, "cafe", v)

	ctx := ctxForEngine(psql.EngineMySQL)
	sql, err := psql.B().Select().From("t").Where(map[string]any{"h": h}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("h"='cafe')`, sql)

	var scanned psql.Hex
	require.NoError(t, scanned.Scan(nil))
	assert.NotNil(t, scanned)
	assert.Len(t, scanned, 0)
}

// --- 12. Set ---

func TestFixSetScanEmpty(t *testing.T) {
	var s psql.Set
	require.NoError(t, s.Scan(""))
	assert.NotNil(t, s)
	assert.Len(t, s, 0)

	require.NoError(t, s.Scan("a,b"))
	assert.Equal(t, psql.Set{"a", "b"}, s)

	require.NoError(t, s.Scan(nil))
	assert.NotNil(t, s)
	assert.Len(t, s, 0)
}

func TestFixSetInWhereIsSingleValue(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)
	sql, err := psql.B().Select().From("t").Where(map[string]any{"tags": psql.Set{"a", "b"}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("tags"='a,b')`, sql)

	sql, err = psql.B().Select().From("t").Where(map[string]any{"tags": &psql.Not{V: psql.Set{"a", "b"}}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("tags"!='a,b')`, sql)

	sql, args, err := psql.B().Select().From("t").Where(map[string]any{"tags": psql.Set{"a", "b"}, "v": psql.Vector{1}}).RenderArgs(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("tags"=? AND "v"=?)`, sql)
	assert.Len(t, args, 2)

	// V() is also a single value
	sql, err = psql.B().Select().From("t").Where(map[string]any{"a": psql.V("x")}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("a"='x')`, sql)
}

// --- 13. nil comparisons ---

func TestFixNilComparisonsRenderIsNull(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)
	sql, err := psql.B().Select().From("t").Where(map[string]any{"a": &psql.Any{Values: nil}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("a" IS NULL)`, sql)

	sql, err = psql.B().Select().From("t").Where(map[string]any{"a": &psql.Not{V: &psql.Any{Values: nil}}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("a" IS NOT NULL)`, sql)

	sql, err = psql.B().Select().From("t").Where(psql.Equal(psql.F("a"), nil)).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("a" IS NULL)`, sql)

	sql, err = psql.B().Select().From("t").Where(&psql.Comparison{A: psql.F("a"), B: nil, Op: "!="}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("a" IS NOT NULL)`, sql)

	sql, err = psql.B().Select().From("t").Where(map[string]any{"a": &psql.Not{V: psql.Equal(nil, nil)}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("a" IS NOT NULL)`, sql)

	assert.Equal(t, `"a" IS NULL`, psql.Equal(psql.F("a"), nil).EscapeValue())
}

// --- 14. Increment parentheses, sorted operator map ---

func TestFixIncrementParentheses(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)
	sql, err := psql.B().Update("t").Set(map[string]any{"a": psql.Incr(psql.Raw("1+1")), "b": psql.Decr(-1)}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `UPDATE "t" SET "a"="a"+(1+1),"b"="b"-(-1)`, sql)

	sql, args, err := psql.B().Update("t").Set(map[string]any{"a": psql.Incr(3)}).RenderArgs(ctx)
	require.NoError(t, err)
	assert.Equal(t, `UPDATE "t" SET "a"="a"+(?)`, sql)
	assert.Equal(t, []any{3}, args)
}

func TestFixOperatorMapSortedAndValidated(t *testing.T) {
	ctx := ctxForEngine(psql.EngineMySQL)
	sql, err := psql.B().Select().From("t").Where(map[string]any{"a": map[string]any{"$lte": 9, "$gt": 1, "$lt": 8, "$gte": 2}}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (("a">1 AND "a">=2 AND "a"<8 AND "a"<=9))`, sql)

	_, err = psql.B().Select().From("t").Where(map[string]any{"a": map[string]any{"$ne": 1}}).Render(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "$ne")
}

// --- 15. ORDER BY with parameterized expressions ---

func TestFixOrderByExpressionsParameterized(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)
	sql, args, err := psql.B().Select().From("t").
		OrderBy(psql.Equal(psql.F("a"), "x").(psql.SortValueable), psql.Between(psql.F("b"), 1, 2).(psql.SortValueable)).
		RenderArgs(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" ORDER BY "a"=$1,"b" BETWEEN $2 AND $3`, sql)
	assert.Equal(t, []any{"x", 1, 2}, args)
}

// --- 16. portable functions ---

func TestFixFindInSetPortable(t *testing.T) {
	q := psql.B().Select().From("t").Where(map[string]any{"tags": &psql.FindInSet{Value: "go"}})
	qn := psql.B().Select().From("t").Where(map[string]any{"tags": &psql.Not{V: &psql.FindInSet{Value: "go"}}})

	sql, err := q.Render(ctxForEngine(psql.EngineMySQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (FIND_IN_SET('go',"tags"))`, sql)
	sql, err = qn.Render(ctxForEngine(psql.EngineMySQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (NOT FIND_IN_SET('go',"tags"))`, sql)

	sql, err = q.Render(ctxForEngine(psql.EnginePostgreSQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ('go' = ANY(string_to_array("tags", ',')))`, sql)
	sql, err = qn.Render(ctxForEngine(psql.EnginePostgreSQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (NOT ('go' = ANY(string_to_array("tags", ','))))`, sql)

	sql, err = q.Render(ctxForEngine(psql.EngineSQLite))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ((',' || "tags" || ',') LIKE '%,' || 'go' || ',%')`, sql)
	sql, err = qn.Render(ctxForEngine(psql.EngineSQLite))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ((',' || "tags" || ',') NOT LIKE '%,' || 'go' || ',%')`, sql)

	// top-level form is engine-aware and parameterized
	top := psql.B().Select().From("t").Where(&psql.FindInSet{Field: psql.F("tags"), Value: "go"})
	sql, args, err := top.RenderArgs(ctxForEngine(psql.EnginePostgreSQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ($1 = ANY(string_to_array("tags", ',')))`, sql)
	assert.Equal(t, []any{"go"}, args)
	sql, args, err = top.RenderArgs(ctxForEngine(psql.EngineMySQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (FIND_IN_SET(?,"tags"))`, sql)
	assert.Equal(t, []any{"go"}, args)
}

func TestFixVectorDistanceUnsupportedEngine(t *testing.T) {
	vec := psql.Vector{1, 2}
	for _, e := range []psql.Engine{psql.EngineMySQL, psql.EngineSQLite} {
		_, err := psql.B().Select().From("t").OrderBy(psql.VecL2Distance(psql.F("e"), vec)).Render(ctxForEngine(e))
		require.Error(t, err, e)
		assert.Contains(t, err.Error(), "vector distance is not supported")

		_, _, err = psql.B().Select().From("t").Where(psql.Lt(psql.VecCosineDistance(psql.F("e"), vec), 0.5)).RenderArgs(ctxForEngine(e))
		require.Error(t, err, e)
	}
}

func TestFixGreatestSingleArgument(t *testing.T) {
	for _, e := range allEngines {
		sql, err := psql.B().Select(psql.Greatest(psql.F("a")), psql.Least(5)).From("t").Render(ctxForEngine(e))
		require.NoError(t, err, e)
		assert.Equal(t, `SELECT "a",5 FROM "t"`, sql, e)

		_, err = psql.B().Select(psql.Greatest()).From("t").Render(ctxForEngine(e))
		require.Error(t, err, e)
	}
}

func TestFixCILikeSQLite(t *testing.T) {
	ctx := ctxForEngine(psql.EngineSQLite)
	sql, err := psql.B().Select().From("t").Where(psql.CILike(psql.F("name"), "jo%")).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (LOWER("name") LIKE LOWER('jo%') ESCAPE '\')`, sql)

	sql, args, err := psql.B().Select().From("t").Where(map[string]any{"name": &psql.Not{V: psql.CILike(nil, "jo%")}}).RenderArgs(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (LOWER("name") NOT LIKE LOWER(?) ESCAPE '\')`, sql)
	assert.Equal(t, []any{"jo%"}, args)

	// PostgreSQL still uses ILIKE, MySQL plain LIKE
	sql, err = psql.B().Select().From("t").Where(psql.CILike(psql.F("name"), "jo%")).Render(ctxForEngine(psql.EnginePostgreSQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("name" ILIKE 'jo%' ESCAPE '\')`, sql)
	sql, err = psql.B().Select().From("t").Where(psql.CILike(psql.F("name"), "jo%")).Render(ctxForEngine(psql.EngineMySQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("name" LIKE 'jo%' ESCAPE '\')`, sql)
}

// --- 17. table aliases and table.* ---

func TestFixTableAliases(t *testing.T) {
	ctx := ctxForEngine(psql.EnginePostgreSQL)
	sql, err := psql.B().Select("u.id", "o.*").From("users AS u").
		LeftJoin("orders o", psql.Equal(psql.F("o.user_id"), psql.F("u.id"))).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT "u"."id","o".* FROM "users" AS "u" LEFT JOIN "orders" AS "o" ON "o"."user_id"="u"."id"`, sql)

	sql, err = psql.B().Select().From("users u").Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "users" AS "u"`, sql)

	sql, err = psql.B().Select().From("users as u").Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "users" AS "u"`, sql)

	sql, err = psql.B().Update("users u").Set(map[string]any{"a": 1}).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `UPDATE "users" AS "u" SET "a"=1`, sql)

	// plain names are unchanged
	sql, err = psql.B().Select("t.*").From("users").Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT "t".* FROM "users"`, sql)

	assert.Equal(t, `"t".*`, psql.F("t", "*").EscapeValue())
	assert.Equal(t, `"t".*`, psql.F("t.*").EscapeValue())
	assert.Equal(t, `*`, psql.F("*").EscapeValue())
}
