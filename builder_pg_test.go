package psql_test

import (
	"context"
	"database/sql/driver"
	"strings"
	"sync"
	"testing"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for the advanced PostgreSQL / CockroachDB oriented builder features
// (RETURNING, upsert extensions, CTEs, DISTINCT ON, lock modes, AS OF SYSTEM
// TIME, EXPLAIN, multi-row inserts), rendered on every product. No dialect
// is registered for MySQL in the unit tests, so MySQL/MariaDB use the
// default "?" placeholder; PostgreSQL uses $n through testPgDialect.

var allVariants = []psql.Variant{
	psql.VariantPostgreSQL, psql.VariantCockroachDB, psql.VariantMySQL, psql.VariantMariaDB, psql.VariantSQLite,
}

// ctxForVariant returns a context bound to a backend of the given product.
func ctxForVariant(v psql.Variant, opts ...psql.BackendOption) context.Context {
	opts = append([]psql.BackendOption{psql.WithVariant(v)}, opts...)
	return psql.NewBackend(v.Engine(), nil, opts...).Plug(context.Background())
}

// testTable is a plain EscapeTableable for Replace.
type testTable string

func (t testTable) EscapeTable() string { return psql.QuoteName(string(t)) }

func isPg(v psql.Variant) bool {
	return v == psql.VariantPostgreSQL || v == psql.VariantCockroachDB
}

// ---------------------------------------------------------------------------
// 1. RETURNING
// ---------------------------------------------------------------------------

func TestPgReturningInsert(t *testing.T) {
	build := func() *psql.QueryBuilder {
		b := psql.B().Insert(map[string]any{"id": 1, "name": "a"}).Returning("id", "name")
		b.Table("t")
		return b
	}
	for _, v := range allVariants {
		ctx := ctxForVariant(v)
		sql, err := build().Render(ctx)
		switch v {
		case psql.VariantMySQL:
			require.Error(t, err, v)
			assert.ErrorIs(t, err, psql.ErrNotSupported, v)
			assert.Contains(t, err.Error(), "MySQL")
		case psql.VariantMariaDB:
			require.NoError(t, err, v)
			assert.Equal(t, `INSERT INTO "t" SET "id"=1,"name"='a' RETURNING "id","name"`, sql, v)
		default:
			require.NoError(t, err, v)
			assert.Equal(t, `INSERT INTO "t" ("id","name") VALUES (1,'a') RETURNING "id","name"`, sql, v)
		}
	}

	// parameterized: RETURNING expressions are never bound
	ctx := ctxForVariant(psql.VariantPostgreSQL)
	sql, args, err := build().RenderArgs(ctx)
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "t" ("id","name") VALUES ($1,$2) RETURNING "id","name"`, sql)
	assert.Equal(t, []any{1, "a"}, args)

	// star and expressions
	b := psql.B().Insert(map[string]any{"id": 1}).Returning("*")
	b.Table("t")
	sql, err = b.Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "t" ("id") VALUES (1) RETURNING *`, sql)

	b = psql.B().Insert(map[string]any{"id": 1}).Returning(psql.Raw("now() AS ts"), psql.F("t", "id"))
	b.Table("t")
	sql, err = b.Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "t" ("id") VALUES (1) RETURNING now() AS ts,"t"."id"`, sql)
}

func TestPgReturningUpdateDelete(t *testing.T) {
	for _, v := range allVariants {
		ctx := ctxForVariant(v)
		sql, err := psql.B().Update("t").Set(map[string]any{"a": 1}).Where(map[string]any{"id": 2}).Returning("id").Render(ctx)
		switch v {
		case psql.VariantMySQL, psql.VariantMariaDB:
			require.Error(t, err, v)
			assert.ErrorIs(t, err, psql.ErrNotSupported, v)
			assert.Contains(t, err.Error(), "UPDATE", v)
		default:
			require.NoError(t, err, v)
			assert.Equal(t, `UPDATE "t" SET "a"=1 WHERE ("id"=2) RETURNING "id"`, sql, v)
		}

		sql, err = psql.B().Delete().From("t").Where(map[string]any{"id": 2}).Returning("*").Render(ctx)
		if v == psql.VariantMySQL {
			require.Error(t, err, v)
			assert.ErrorIs(t, err, psql.ErrNotSupported, v)
		} else {
			require.NoError(t, err, v)
			assert.Equal(t, `DELETE FROM "t" WHERE ("id"=2) RETURNING *`, sql, v)
		}
	}

	// RenderArgs and RunQuery both surface the error
	ctx := ctxForVariant(psql.VariantMySQL)
	_, _, err := psql.B().Update("t").Set(map[string]any{"a": 1}).Returning("id").RenderArgs(ctx)
	assert.ErrorIs(t, err, psql.ErrNotSupported)
	_, err = psql.B().Update("t").Set(map[string]any{"a": 1}).Returning("id").RunQuery(ctx)
	assert.ErrorIs(t, err, psql.ErrNotSupported)
}

func TestPgReturningInsertSelectAndReplace(t *testing.T) {
	pg := ctxForVariant(psql.VariantPostgreSQL)
	sql, err := psql.B().InsertSelect("archive").Select("id").From("users").Where(map[string]any{"x": 1}).Returning("id").Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "archive" SELECT "id" FROM "users" WHERE ("x"=1) RETURNING "id"`, sql)

	maria := ctxForVariant(psql.VariantMariaDB)
	sql, err = psql.B().Replace(testTable("t")).Set(map[string]any{"id": 1}).Returning("id").Render(maria)
	require.NoError(t, err)
	assert.Equal(t, `REPLACE "t" SET "id"=1 RETURNING "id"`, sql)

	// SELECT cannot carry RETURNING
	_, err = psql.B().Select().From("t").Returning("id").Render(pg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SELECT")
}

// returningStmtsDialect wraps the shared stub dialect of coreStubEngine
// (Engine(99), see core_fixes_test.go) with ReturningStatements. It keeps
// every behaviour of stubDialect and only adds RETURNING support, so
// replacing the registration is harmless for the other tests of the package.
type returningStmtsDialect struct{ stubDialect }

func (returningStmtsDialect) SupportsReturningFor(string) bool { return true }

var returningStmtsDialectOnce sync.Once

func TestPgReturningDialectPrecedence(t *testing.T) {
	// registered once, after every init() ran (core_fixes_test.go registers
	// the plain stubDialect in its init)
	returningStmtsDialectOnce.Do(func() { psql.RegisterDialect(coreStubEngine, returningStmtsDialect{}) })

	// the variant default refuses RETURNING on MySQL; the dialect wins
	ctx := psql.NewBackend(coreStubEngine, nil, psql.WithVariant(psql.VariantMySQL)).Plug(context.Background())
	sql, err := psql.B().Update("t").Set(map[string]any{"a": 1}).Returning("id").Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `UPDATE "t" SET "a"=1 RETURNING "id"`, sql)
	sql, err = psql.B().Delete().From("t").Returning("id").Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `DELETE FROM "t" RETURNING "id"`, sql)

	// without the dialect override the same variant fails
	_, err = psql.B().Update("t").Set(map[string]any{"a": 1}).Returning("id").Render(ctxForVariant(psql.VariantMySQL))
	assert.ErrorIs(t, err, psql.ErrNotSupported)
}

// ---------------------------------------------------------------------------
// 2. Upsert extensions
// ---------------------------------------------------------------------------

func TestPgExcludedUpsert(t *testing.T) {
	build := func() *psql.QueryBuilder {
		b := psql.B().Insert(map[string]any{"id": 1, "hits": 1}).OnConflict("id").
			DoUpdate(map[string]any{"hits": psql.Excluded("hits")})
		b.Table("t")
		return b
	}
	for _, v := range allVariants {
		ctx := ctxForVariant(v)
		sql, err := build().Render(ctx)
		require.NoError(t, err, v)
		switch v {
		case psql.VariantMySQL, psql.VariantMariaDB:
			assert.Equal(t, `INSERT INTO "t" SET "hits"=1,"id"=1 ON DUPLICATE KEY UPDATE "hits"=VALUES("hits")`, sql, v)
		default:
			assert.Equal(t, `INSERT INTO "t" ("hits","id") VALUES (1,1) ON CONFLICT ("id") DO UPDATE SET "hits"=EXCLUDED."hits"`, sql, v)
		}
		_, args, err := build().RenderArgs(ctx)
		require.NoError(t, err, v)
		assert.Len(t, args, 2, v) // EXCLUDED is never bound
	}
	// context-less rendering uses the standard form
	assert.Equal(t, `EXCLUDED."hits"`, psql.Excluded("hits").EscapeValue())
	assert.Equal(t, `EXCLUDED."a""b"`, psql.Escape(psql.Excluded(`a"b`)))
}

func TestPgOnConflictConstraint(t *testing.T) {
	build := func() *psql.QueryBuilder {
		b := psql.B().Insert(map[string]any{"id": 1}).OnConflictConstraint("t_pkey").
			DoUpdate(map[string]any{"id": psql.Excluded("id")})
		b.Table("t")
		return b
	}
	for _, v := range allVariants {
		ctx := ctxForVariant(v)
		sql, err := build().Render(ctx)
		if isPg(v) {
			require.NoError(t, err, v)
			assert.Equal(t, `INSERT INTO "t" ("id") VALUES (1) ON CONFLICT ON CONSTRAINT "t_pkey" DO UPDATE SET "id"=EXCLUDED."id"`, sql, v)
		} else {
			require.Error(t, err, v)
			assert.ErrorIs(t, err, psql.ErrNotSupported, v)
			assert.Contains(t, err.Error(), "ON CONSTRAINT", v)
		}
	}

	pg := ctxForVariant(psql.VariantPostgreSQL)
	// DO NOTHING keeps the target
	b := psql.B().Insert(map[string]any{"id": 1}).OnConflictConstraint("t_pkey").DoNothing()
	b.Table("t")
	sql, err := b.Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "t" ("id") VALUES (1) ON CONFLICT ON CONSTRAINT "t_pkey" DO NOTHING`, sql)

	// columns and constraint cannot be combined
	b = psql.B().Insert(map[string]any{"id": 1}).OnConflict("id").OnConflictConstraint("t_pkey").DoNothing()
	b.Table("t")
	_, err = b.Render(pg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be combined")
}

func TestPgDoNothingWithTarget(t *testing.T) {
	b := psql.B().Insert(map[string]any{"id": 1}).OnConflict("id").DoNothing()
	b.Table("t")
	sql, err := b.Render(ctxForVariant(psql.VariantPostgreSQL))
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "t" ("id") VALUES (1) ON CONFLICT ("id") DO NOTHING`, sql)

	// without a target the clause is unchanged
	b = psql.B().Insert(map[string]any{"id": 1}).DoNothing()
	b.Table("t")
	sql, err = b.Render(ctxForVariant(psql.VariantCockroachDB))
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "t" ("id") VALUES (1) ON CONFLICT DO NOTHING`, sql)

	// SQLite keeps INSERT OR IGNORE, MySQL INSERT IGNORE
	b = psql.B().Insert(map[string]any{"id": 1}).OnConflict("id").DoNothing()
	b.Table("t")
	sql, err = b.Render(ctxForVariant(psql.VariantSQLite))
	require.NoError(t, err)
	assert.Equal(t, `INSERT OR IGNORE INTO "t" ("id") VALUES (1)`, sql)
	sql, err = b.Render(ctxForVariant(psql.VariantMariaDB))
	require.NoError(t, err)
	assert.Equal(t, `INSERT IGNORE INTO "t" SET "id"=1`, sql)
}

func TestPgDoUpdateWhere(t *testing.T) {
	build := func() *psql.QueryBuilder {
		b := psql.B().Insert(map[string]any{"id": 1, "hits": 1}).OnConflict("id").
			DoUpdate(map[string]any{"hits": psql.Excluded("hits")}).
			DoUpdateWhere(psql.Lt(psql.F("t", "hits"), 10))
		b.Table("t")
		return b
	}
	for _, v := range allVariants {
		ctx := ctxForVariant(v)
		sql, err := build().Render(ctx)
		switch v {
		case psql.VariantMySQL, psql.VariantMariaDB:
			require.Error(t, err, v)
			assert.ErrorIs(t, err, psql.ErrNotSupported, v)
			assert.Contains(t, err.Error(), "DO UPDATE ... WHERE", v)
		default:
			require.NoError(t, err, v)
			assert.Equal(t, `INSERT INTO "t" ("hits","id") VALUES (1,1) ON CONFLICT ("id") DO UPDATE SET "hits"=EXCLUDED."hits" WHERE ("t"."hits"<10)`, sql, v)
			sql, args, err := build().RenderArgs(ctx)
			require.NoError(t, err, v)
			assert.Equal(t, []any{1, 1, 10}, args, v)
			if isPg(v) {
				assert.Equal(t, `INSERT INTO "t" ("hits","id") VALUES ($1,$2) ON CONFLICT ("id") DO UPDATE SET "hits"=EXCLUDED."hits" WHERE ("t"."hits"<$3)`, sql, v)
			}
		}
	}

	// bare strings are rejected like Where
	b := psql.B().Insert(map[string]any{"id": 1}).OnConflict("id").DoUpdate(map[string]any{"id": 1}).DoUpdateWhere("x=1")
	b.Table("t")
	_, err := b.Render(ctxForVariant(psql.VariantPostgreSQL))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DoUpdateWhere")
}

// ---------------------------------------------------------------------------
// 3. CTEs
// ---------------------------------------------------------------------------

func TestPgWithCTE(t *testing.T) {
	active := psql.B().Select("id").From("users").Where(map[string]any{"active": true})
	build := func() *psql.QueryBuilder {
		return psql.B().With("active_users", active).Select("*").From("orders").
			Where(map[string]any{"user_id": &psql.SubIn{Sub: psql.B().Select("id").From("active_users")}})
	}
	for _, v := range allVariants {
		ctx := ctxForVariant(v)
		sql, err := build().Render(ctx)
		require.NoError(t, err, v)
		assert.Equal(t, `WITH "active_users" AS (SELECT "id" FROM "users" WHERE ("active"=TRUE)) SELECT * FROM "orders" WHERE ("user_id" IN (SELECT "id" FROM "active_users"))`, sql, v)
	}

	// joined CTE with a column list
	pg := ctxForVariant(psql.VariantPostgreSQL)
	counts := psql.B().Select("user_id", psql.Raw("COUNT(*)")).From("orders").GroupByFields("user_id")
	sql, err := psql.B().With("oc", counts, "uid", "cnt").
		Select("u.name", "oc.cnt").From("users u").
		Join("LEFT", "oc", psql.Equal(psql.F("oc.uid"), psql.F("u.id"))).Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `WITH "oc" ("uid","cnt") AS (SELECT "user_id",COUNT(*) FROM "orders" GROUP BY "user_id") SELECT "u"."name","oc"."cnt" FROM "users" AS "u" LEFT JOIN "oc" ON "oc"."uid"="u"."id"`, sql)
}

func TestPgWithCTEArgNumbering(t *testing.T) {
	first := psql.B().Select("id").From("a").Where(map[string]any{"x": 1})
	second := psql.B().Select("id").From("b").Where(map[string]any{"y": 2})
	build := func() *psql.QueryBuilder {
		return psql.B().With("ca", first).With("cb", second).
			Select("*").From("ca").Where(map[string]any{"z": 3}).
			Join("INNER", "cb", psql.Equal(psql.F("ca.id"), psql.F("cb.id")))
	}
	for _, v := range allVariants {
		ctx := ctxForVariant(v)
		sql, args, err := build().RenderArgs(ctx)
		require.NoError(t, err, v)
		assert.Equal(t, []any{1, 2, 3}, args, v)
		if isPg(v) {
			assert.Equal(t, `WITH "ca" AS (SELECT "id" FROM "a" WHERE ("x"=$1)),"cb" AS (SELECT "id" FROM "b" WHERE ("y"=$2)) SELECT * FROM "ca" INNER JOIN "cb" ON "ca"."id"="cb"."id" WHERE ("z"=$3)`, sql, v)
		} else {
			assert.Equal(t, `WITH "ca" AS (SELECT "id" FROM "a" WHERE ("x"=?)),"cb" AS (SELECT "id" FROM "b" WHERE ("y"=?)) SELECT * FROM "ca" INNER JOIN "cb" ON "ca"."id"="cb"."id" WHERE ("z"=?)`, sql, v)
		}
	}
}

func TestPgWithRecursive(t *testing.T) {
	raw := psql.Raw(`SELECT "id","parent" FROM "nodes" WHERE "id"=1 UNION ALL SELECT n."id",n."parent" FROM "nodes" n JOIN "tree" t ON n."parent"=t."id"`)
	for _, v := range allVariants {
		ctx := ctxForVariant(v)
		sql, err := psql.B().WithRecursive("tree", raw, "id", "parent").Select("*").From("tree").Render(ctx)
		require.NoError(t, err, v)
		assert.Equal(t, `WITH RECURSIVE "tree" ("id","parent") AS (`+raw.EscapeValue()+`) SELECT * FROM "tree"`, sql, v)
	}

	// RECURSIVE is emitted once even when mixed with plain CTEs
	pg := ctxForVariant(psql.VariantPostgreSQL)
	sql, err := psql.B().With("base", psql.B().Select("id").From("t")).WithRecursive("tree", raw).Select("*").From("tree").Render(pg)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(sql, `WITH RECURSIVE "base" AS (SELECT "id" FROM "t"),"tree" AS (`), sql)
	assert.Equal(t, 1, strings.Count(sql, "RECURSIVE"))
}

func TestPgWithCTEStatements(t *testing.T) {
	pg := ctxForVariant(psql.VariantPostgreSQL)
	sub := psql.B().Select("id").From("expired").Where(map[string]any{"state": "old"})

	// UPDATE and DELETE
	sql, args, err := psql.B().With("e", sub).Update("jobs").Set(map[string]any{"state": "gone"}).
		Where(map[string]any{"id": &psql.SubIn{Sub: psql.B().Select("id").From("e")}}).RenderArgs(pg)
	require.NoError(t, err)
	assert.Equal(t, `WITH "e" AS (SELECT "id" FROM "expired" WHERE ("state"=$1)) UPDATE "jobs" SET "state"=$2 WHERE ("id" IN (SELECT "id" FROM "e"))`, sql)
	assert.Equal(t, []any{"old", "gone"}, args)

	sql, err = psql.B().With("e", sub).Delete().From("jobs").Where(map[string]any{"id": &psql.SubIn{Sub: psql.B().Select("id").From("e")}}).Render(pg)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(sql, `WITH "e" AS (`), sql)
	assert.Contains(t, sql, `) DELETE FROM "jobs" WHERE`)

	// INSERT ... SELECT and INSERT keep the prefix even when the statement is rewritten
	sql, err = psql.B().With("e", sub).InsertSelect("archive").Select("id").From("e").Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `WITH "e" AS (SELECT "id" FROM "expired" WHERE ("state"='old')) INSERT INTO "archive" SELECT "id" FROM "e"`, sql)
	// MySQL / MariaDB only accept WITH ahead of the SELECT part
	for _, v := range []psql.Variant{psql.VariantMySQL, psql.VariantMariaDB} {
		sql, args, err := psql.B().With("e", sub).InsertSelect("archive").Select("id").From("e").Where(map[string]any{"k": 2}).RenderArgs(ctxForVariant(v))
		require.NoError(t, err, v)
		assert.Equal(t, `INSERT INTO "archive" WITH "e" AS (SELECT "id" FROM "expired" WHERE ("state"=?)) SELECT "id" FROM "e" WHERE ("k"=?)`, sql, v)
		assert.Equal(t, []any{"old", 2}, args, v)
	}

	sq := ctxForVariant(psql.VariantSQLite)
	b := psql.B().With("e", sub).Insert(map[string]any{"id": 1}).DoNothing()
	b.Table("t")
	sql, err = b.Render(sq)
	require.NoError(t, err)
	assert.Equal(t, `WITH "e" AS (SELECT "id" FROM "expired" WHERE ("state"='old')) INSERT OR IGNORE INTO "t" ("id") VALUES (1)`, sql)

	// a CTE query can itself be nested as a subquery
	inner := psql.B().With("x", psql.B().Select("id").From("t")).Select("id").From("x")
	sql, err = psql.B().Select().From("u").Where(map[string]any{"id": &psql.SubIn{Sub: inner}}).Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "u" WHERE ("id" IN (WITH "x" AS (SELECT "id" FROM "t") SELECT "id" FROM "x"))`, sql)
}

func TestPgWithCTEErrors(t *testing.T) {
	pg := ctxForVariant(psql.VariantPostgreSQL)
	// errors of the CTE query propagate
	bad := psql.B().Select("id").From("t").Where("bare string")
	_, err := psql.B().With("c", bad).Select("*").From("c").Render(pg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bare string")

	// unsupported sub type
	_, err = psql.B().With("c", 42).Select("*").From("c").Render(pg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported type int")
}

// ---------------------------------------------------------------------------
// 4. DISTINCT ON
// ---------------------------------------------------------------------------

func TestPgDistinctOn(t *testing.T) {
	build := func() *psql.QueryBuilder {
		return psql.B().Select("*").DistinctOn("user_id").From("events").
			OrderBy(psql.S("user_id", "ASC"), psql.S("created", "DESC"))
	}
	for _, v := range allVariants {
		ctx := ctxForVariant(v)
		sql, err := build().Render(ctx)
		if isPg(v) {
			require.NoError(t, err, v)
			assert.Equal(t, `SELECT DISTINCT ON ("user_id") * FROM "events" ORDER BY "user_id" ASC,"created" DESC`, sql, v)
		} else {
			require.Error(t, err, v)
			assert.ErrorIs(t, err, psql.ErrNotSupported, v)
			assert.Contains(t, err.Error(), "DISTINCT ON", v)
			assert.Contains(t, err.Error(), v.String(), v)
			_, _, err = build().RenderArgs(ctx)
			assert.ErrorIs(t, err, psql.ErrNotSupported, v)
		}
	}

	pg := ctxForVariant(psql.VariantPostgreSQL)
	// several expressions, expressions and fields; DISTINCT ON replaces DISTINCT
	sql, err := psql.B().Select("id").SetDistinct().DistinctOn("a", psql.F("t", "b"), psql.Raw("lower(c)")).From("t").Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT DISTINCT ON ("a","t"."b",lower(c)) "id" FROM "t"`, sql)

	// plain DISTINCT is unchanged
	sql, err = psql.B().Select("id").SetDistinct().From("t").Render(ctxForVariant(psql.VariantMySQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT DISTINCT "id" FROM "t"`, sql)
}

// ---------------------------------------------------------------------------
// 5. Row locking
// ---------------------------------------------------------------------------

func TestPgLockModes(t *testing.T) {
	type want struct {
		v    psql.Variant
		opts []psql.BackendOption
		exp  map[psql.LockMode]string // "" means omitted
	}
	pgExp := map[psql.LockMode]string{
		psql.LockUpdate:      " FOR UPDATE",
		psql.LockShare:       " FOR SHARE",
		psql.LockNoKeyUpdate: " FOR NO KEY UPDATE",
		psql.LockKeyShare:    " FOR KEY SHARE",
	}
	mysql8Exp := map[psql.LockMode]string{
		psql.LockUpdate:      " FOR UPDATE",
		psql.LockShare:       " FOR SHARE",
		psql.LockNoKeyUpdate: " FOR UPDATE",
		psql.LockKeyShare:    " FOR SHARE",
	}
	legacyExp := map[psql.LockMode]string{
		psql.LockUpdate:      " FOR UPDATE",
		psql.LockShare:       " LOCK IN SHARE MODE",
		psql.LockNoKeyUpdate: " FOR UPDATE",
		psql.LockKeyShare:    " LOCK IN SHARE MODE",
	}
	cases := []want{
		{v: psql.VariantPostgreSQL, exp: pgExp},
		{v: psql.VariantCockroachDB, exp: pgExp},
		{v: psql.VariantMySQL, exp: mysql8Exp},
		{v: psql.VariantMySQL, opts: []psql.BackendOption{psql.WithServerVersion("8.0.32-log")}, exp: mysql8Exp},
		{v: psql.VariantMySQL, opts: []psql.BackendOption{psql.WithServerVersion("5.7.42")}, exp: legacyExp},
		{v: psql.VariantMariaDB, opts: []psql.BackendOption{psql.WithServerVersion("10.11.4-MariaDB")}, exp: legacyExp},
		{v: psql.VariantSQLite, exp: map[psql.LockMode]string{}},
	}
	for _, c := range cases {
		ctx := ctxForVariant(c.v, c.opts...)
		for _, mode := range []psql.LockMode{psql.LockNone, psql.LockUpdate, psql.LockShare, psql.LockNoKeyUpdate, psql.LockKeyShare} {
			sql, err := psql.B().Select().From("t").SetLockMode(mode).Render(ctx)
			require.NoError(t, err, "%s %v", c.v, mode)
			assert.Equal(t, `SELECT * FROM "t"`+c.exp[mode], sql, "%s %v", c.v, mode)

			// SKIP LOCKED / NOWAIT follow the clause
			if mode == psql.LockNone {
				continue
			}
			sql, err = psql.B().Select().From("t").SetLockMode(mode).SetSkipLocked().Render(ctx)
			require.NoError(t, err)
			if c.exp[mode] == "" {
				assert.Equal(t, `SELECT * FROM "t"`, sql, c.v)
			} else {
				assert.Equal(t, `SELECT * FROM "t"`+c.exp[mode]+" SKIP LOCKED", sql, "%s %v", c.v, mode)
			}
			sql, err = psql.B().Select().From("t").SetLockMode(mode).SetNoWait().Render(ctx)
			require.NoError(t, err)
			if c.exp[mode] != "" {
				assert.Equal(t, `SELECT * FROM "t"`+c.exp[mode]+" NOWAIT", sql, "%s %v", c.v, mode)
			}
		}
	}
}

func TestPgLockOf(t *testing.T) {
	build := func() *psql.QueryBuilder {
		return psql.B().Select().From("t").Join("LEFT", "u", psql.Equal(psql.F("t.uid"), psql.F("u.id"))).
			SetLockMode(psql.LockShare).LockOf("t").SetSkipLocked()
	}
	for _, v := range allVariants {
		ctx := ctxForVariant(v)
		sql, err := build().Render(ctx)
		switch v {
		case psql.VariantMariaDB:
			require.Error(t, err, v)
			assert.ErrorIs(t, err, psql.ErrNotSupported, v)
			assert.Contains(t, err.Error(), "LOCK IN SHARE MODE OF", v)
		case psql.VariantSQLite:
			require.NoError(t, err, v)
			assert.Equal(t, `SELECT * FROM "t" LEFT JOIN "u" ON "t"."uid"="u"."id"`, sql, v)
		default:
			require.NoError(t, err, v)
			assert.Equal(t, `SELECT * FROM "t" LEFT JOIN "u" ON "t"."uid"="u"."id" FOR SHARE OF "t" SKIP LOCKED`, sql, v)
		}
	}

	// FOR UPDATE OF on MariaDB is not available either; MySQL 5.7 neither
	_, err := psql.B().Select().From("t").LockOf("t").Render(ctxForVariant(psql.VariantMariaDB))
	assert.ErrorIs(t, err, psql.ErrNotSupported)
	_, err = psql.B().Select().From("t").LockOf("t").Render(ctxForVariant(psql.VariantMySQL, psql.WithServerVersion("5.7.1")))
	assert.ErrorIs(t, err, psql.ErrNotSupported)

	// LockOf alone implies FOR UPDATE; several tables are listed
	sql, err := psql.B().Select().From("t").LockOf("t", "u").Render(ctxForVariant(psql.VariantPostgreSQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" FOR UPDATE OF "t","u"`, sql)
}

func TestPgLockCompat(t *testing.T) {
	pg := ctxForVariant(psql.VariantPostgreSQL)
	// legacy API unchanged
	sql, err := psql.B().Select().From("t").SetForUpdate().Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" FOR UPDATE`, sql)
	sql, err = psql.B().Select().From("t").SetSkipLocked().Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" FOR UPDATE SKIP LOCKED`, sql)
	sql, err = psql.B().Select().From("t").SetNoWait().Render(ctxForVariant(psql.VariantMySQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" FOR UPDATE NOWAIT`, sql)

	// ForUpdate flag and lock mode agree
	q := psql.B().Select().From("t").SetLockMode(psql.LockUpdate)
	assert.True(t, q.ForUpdate)
	q = psql.B().Select().From("t").SetLockMode(psql.LockShare)
	assert.False(t, q.ForUpdate)
	// SetSkipLocked after a share lock keeps the share lock
	sql, err = q.SetSkipLocked().Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" FOR SHARE SKIP LOCKED`, sql)
	// switching back to no lock drops the clause
	sql, err = psql.B().Select().From("t").SetForUpdate().SetLockMode(psql.LockNone).Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t"`, sql)

	assert.Equal(t, "FOR NO KEY UPDATE", psql.LockNoKeyUpdate.String())
	assert.Equal(t, "", psql.LockNone.String())
}

// ---------------------------------------------------------------------------
// 6. AS OF SYSTEM TIME
// ---------------------------------------------------------------------------

func TestPgAsOfSystemTime(t *testing.T) {
	build := func() *psql.QueryBuilder {
		return psql.B().Select("id").From("t").AsOfSystemTime("-10s").Where(map[string]any{"a": 1}).Limit(5)
	}
	for _, v := range allVariants {
		ctx := ctxForVariant(v)
		sql, err := build().Render(ctx)
		if v == psql.VariantCockroachDB {
			require.NoError(t, err, v)
			assert.Equal(t, `SELECT "id" FROM "t" AS OF SYSTEM TIME '-10s' WHERE ("a"=1) LIMIT 5`, sql, v)
			sql, args, err := build().RenderArgs(ctx)
			require.NoError(t, err)
			assert.Equal(t, `SELECT "id" FROM "t" AS OF SYSTEM TIME '-10s' WHERE ("a"=$1) LIMIT 5`, sql)
			assert.Equal(t, []any{1}, args)
		} else {
			require.Error(t, err, v)
			assert.ErrorIs(t, err, psql.ErrNotSupported, v)
			assert.Contains(t, err.Error(), "AS OF SYSTEM TIME", v)
		}
	}

	crdb := ctxForVariant(psql.VariantCockroachDB)
	// after the joins, escaped as a string literal
	sql, err := psql.B().Select().From("t").Join("INNER", "u", psql.Equal(psql.F("t.uid"), psql.F("u.id"))).
		AsOfSystemTime("2024-01-15 12:00:00'").Render(crdb)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" INNER JOIN "u" ON "t"."uid"="u"."id" AS OF SYSTEM TIME '2024-01-15 12:00:00'''`, sql)
}

// ---------------------------------------------------------------------------
// 7. EXPLAIN
// ---------------------------------------------------------------------------

func TestPgExplainMySQLAnalyzeVersion(t *testing.T) {
	ctx := ctxForVariant(psql.VariantMySQL, psql.WithServerVersion("8.0.17"))
	_, err := psql.B().Select().From("t").Explain(ctx, true)
	require.Error(t, err)
	assert.ErrorIs(t, err, psql.ErrNotSupported)
	assert.Contains(t, err.Error(), "8.0.17")

	// rendering errors are returned first
	_, err = psql.B().Select().From("t").DistinctOn("a").Explain(ctxForVariant(psql.VariantMySQL), false)
	assert.ErrorIs(t, err, psql.ErrNotSupported)
}

func TestPgExplainStatements(t *testing.T) {
	cases := []struct {
		v       psql.Variant
		version string
		analyze bool
		prefix  string
	}{
		{psql.VariantPostgreSQL, "", false, "EXPLAIN "},
		{psql.VariantPostgreSQL, "", true, "EXPLAIN ANALYZE "},
		{psql.VariantCockroachDB, "CockroachDB CCL v24.1.0", true, "EXPLAIN ANALYZE "},
		{psql.VariantMySQL, "", false, "EXPLAIN "},
		{psql.VariantMySQL, "8.0.18", true, "EXPLAIN ANALYZE "},
		{psql.VariantMySQL, "", true, "EXPLAIN ANALYZE "}, // unknown version assumed recent
		{psql.VariantMariaDB, "10.6.12-MariaDB", false, "EXPLAIN "},
		{psql.VariantMariaDB, "10.6.12-MariaDB", true, "ANALYZE "},
		{psql.VariantSQLite, "", false, "EXPLAIN QUERY PLAN "},
		{psql.VariantSQLite, "", true, "EXPLAIN QUERY PLAN "},
	}
	for _, c := range cases {
		cfg, _, ctx := newStubBackend(t, psql.WithVariant(c.v), psql.WithServerVersion(c.version))
		cfg.rows = func(q string, _ []driver.Value) *stubRowsSpec {
			if strings.HasPrefix(q, "EXPLAIN") || strings.HasPrefix(q, "ANALYZE") {
				return &stubRowsSpec{Cols: []string{"id", "detail"}, Data: [][]driver.Value{
					{int64(1), "SCAN t"},
					{int64(2), []byte("USE INDEX")},
					{int64(3), nil},
				}}
			}
			return nil
		}
		plan, err := psql.B().Select("id").From("t").Where(map[string]any{"a": 1}).Explain(ctx, c.analyze)
		require.NoError(t, err, "%s analyze=%v", c.v, c.analyze)
		assert.Equal(t, "1\tSCAN t\n2\tUSE INDEX\n3\tNULL", plan, c.v)
		st, ok := cfg.find("FROM")
		require.True(t, ok)
		assert.Equal(t, c.prefix+`SELECT "id" FROM "t" WHERE ("a"=?)`, st.Query, "%s analyze=%v", c.v, c.analyze)
		assert.Equal(t, []driver.Value{int64(1)}, st.Args)
	}
}

// ---------------------------------------------------------------------------
// 8. Multi-row inserts
// ---------------------------------------------------------------------------

func TestPgInsertRows(t *testing.T) {
	build := func() *psql.QueryBuilder {
		b := psql.B().InsertRows([]string{"id", "name"}, []any{1, "a"}, []any{2, "b"})
		b.Table("t")
		return b
	}
	for _, v := range allVariants {
		ctx := ctxForVariant(v)
		sql, err := build().Render(ctx)
		require.NoError(t, err, v)
		assert.Equal(t, `INSERT INTO "t" ("id","name") VALUES (1,'a'),(2,'b')`, sql, v)

		sql, args, err := build().RenderArgs(ctx)
		require.NoError(t, err, v)
		assert.Equal(t, []any{1, "a", 2, "b"}, args, v)
		if isPg(v) {
			assert.Equal(t, `INSERT INTO "t" ("id","name") VALUES ($1,$2),($3,$4)`, sql, v)
		} else {
			assert.Equal(t, `INSERT INTO "t" ("id","name") VALUES (?,?),(?,?)`, sql, v)
		}
	}

	// Values with maps; sorted columns; expressions expanded
	b := psql.B().Values(map[string]any{"name": "a", "id": 1}, map[string]any{"name": psql.Raw("upper('b')"), "id": 2})
	b.Table("t")
	sql, err := b.Render(ctxForVariant(psql.VariantMySQL))
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "t" ("id","name") VALUES (1,'a'),(2,upper('b'))`, sql)
	assert.Equal(t, "INSERT", b.Query)

	// InsertRows can be appended to
	b = psql.B().InsertRows([]string{"id"}, []any{1}).InsertRows([]string{"id"}, []any{2})
	b.Table("t")
	sql, err = b.Render(ctxForVariant(psql.VariantSQLite))
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "t" ("id") VALUES (1),(2)`, sql)
}

func TestPgInsertRowsClauses(t *testing.T) {
	build := func() *psql.QueryBuilder {
		b := psql.B().InsertRows([]string{"id", "hits"}, []any{1, 1}, []any{2, 1}).
			OnConflict("id").DoUpdate(map[string]any{"hits": psql.Excluded("hits")}).Returning("id")
		b.Table("t")
		return b
	}
	for _, v := range allVariants {
		ctx := ctxForVariant(v)
		sql, err := build().Render(ctx)
		switch v {
		case psql.VariantMySQL:
			assert.ErrorIs(t, err, psql.ErrNotSupported, v) // RETURNING
		case psql.VariantMariaDB:
			require.NoError(t, err, v)
			assert.Equal(t, `INSERT INTO "t" ("id","hits") VALUES (1,1),(2,1) ON DUPLICATE KEY UPDATE "hits"=VALUES("hits") RETURNING "id"`, sql, v)
		default:
			require.NoError(t, err, v)
			assert.Equal(t, `INSERT INTO "t" ("id","hits") VALUES (1,1),(2,1) ON CONFLICT ("id") DO UPDATE SET "hits"=EXCLUDED."hits" RETURNING "id"`, sql, v)
		}
	}

	// IGNORE variants
	b := psql.B().InsertRows([]string{"id"}, []any{1}, []any{2}).DoNothing()
	b.Table("t")
	sql, err := b.Render(ctxForVariant(psql.VariantMySQL))
	require.NoError(t, err)
	assert.Equal(t, `INSERT IGNORE INTO "t" ("id") VALUES (1),(2)`, sql)
	sql, err = b.Render(ctxForVariant(psql.VariantSQLite))
	require.NoError(t, err)
	assert.Equal(t, `INSERT OR IGNORE INTO "t" ("id") VALUES (1),(2)`, sql)
	sql, err = b.Render(ctxForVariant(psql.VariantPostgreSQL))
	require.NoError(t, err)
	assert.Equal(t, `INSERT INTO "t" ("id") VALUES (1),(2) ON CONFLICT DO NOTHING`, sql)

	// REPLACE with rows uses the column-list form
	sql, err = psql.B().Replace(testTable("t")).InsertRows([]string{"id"}, []any{1}, []any{2}).Render(ctxForVariant(psql.VariantSQLite))
	require.NoError(t, err)
	assert.Equal(t, `REPLACE INTO "t" ("id") VALUES (1),(2)`, sql)
}

func TestPgInsertRowsErrors(t *testing.T) {
	ctx := ctxForVariant(psql.VariantPostgreSQL)

	b := psql.B().InsertRows([]string{"id", "name"}, []any{1})
	b.Table("t")
	_, err := b.Render(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 values for 2 columns")

	b = psql.B().InsertRows([]string{"id"}, []any{1}).InsertRows([]string{"name"}, []any{"x"})
	b.Table("t")
	_, err = b.Render(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "differ")

	b = psql.B().Values(map[string]any{"id": 1}, map[string]any{"name": "x"})
	b.Table("t")
	_, err = b.Render(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing column")

	b = psql.B().Values(map[string]any{"id": 1}, map[string]any{"id": 2, "name": "x"})
	b.Table("t")
	_, err = b.Render(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected 1")

	// mixing with Insert values
	b = psql.B().Insert(map[string]any{"id": 1}).InsertRows([]string{"id"}, []any{2})
	b.Table("t")
	_, err = b.Render(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be combined")

	// no rows
	b = psql.B().Values()
	b.Query = "INSERT"
	b.Table("t")
	_, err = b.Render(ctx)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Fetch options
// ---------------------------------------------------------------------------

func TestPgFetchOptionsLockAndAsOf(t *testing.T) {
	cfg, _, ctx := newStubBackend(t, psql.WithVariant(psql.VariantCockroachDB))
	cfg.rows = softItemRows(nil)

	_, err := psql.Fetch[coreSoftItem](ctx, nil, psql.WithLock(psql.LockShare, "core_soft_item"), psql.FetchLockSkipLocked)
	require.NoError(t, err)
	sel, ok := cfg.find("SELECT")
	require.True(t, ok)
	assert.True(t, strings.HasSuffix(sel.Query, ` FOR SHARE OF "core_soft_item" SKIP LOCKED`), sel.Query)

	cfg.reset()
	_, err = psql.Fetch[coreSoftItem](ctx, nil, psql.FetchLockKeyShare, psql.FetchLockNoWait)
	require.NoError(t, err)
	sel, ok = cfg.find("SELECT")
	require.True(t, ok)
	assert.True(t, strings.HasSuffix(sel.Query, " FOR KEY SHARE NOWAIT"), sel.Query)

	cfg.reset()
	_, err = psql.Fetch[coreSoftItem](ctx, nil, psql.FetchLockNoKeyUpdate)
	require.NoError(t, err)
	sel, ok = cfg.find("SELECT")
	require.True(t, ok)
	assert.True(t, strings.HasSuffix(sel.Query, " FOR NO KEY UPDATE"), sel.Query)

	// legacy FetchLock still means FOR UPDATE
	cfg.reset()
	_, err = psql.Get[coreSoftItem](ctx, map[string]any{"ID": 1}, psql.FetchLock)
	assert.Error(t, err) // no rows → ErrNotExist
	sel, ok = cfg.find("SELECT")
	require.True(t, ok)
	assert.True(t, strings.HasSuffix(sel.Query, " LIMIT 1 FOR UPDATE"), sel.Query)

	// AS OF SYSTEM TIME goes after the FROM clause
	cfg.reset()
	_, err = psql.Fetch[coreSoftItem](ctx, map[string]any{"Label": "x"}, psql.AsOfSystemTime("-5s"), psql.Limit(3))
	require.NoError(t, err)
	sel, ok = cfg.find("SELECT")
	require.True(t, ok)
	assert.Contains(t, sel.Query, `FROM "core_soft_item" AS OF SYSTEM TIME '-5s' WHERE`)
	assert.True(t, strings.HasSuffix(sel.Query, " LIMIT 3"), sel.Query)

	cfg.reset()
	it, err := psql.IterErr[coreSoftItem](ctx, nil, psql.AsOfSystemTime("-5s"), psql.FetchLockShare)
	require.NoError(t, err)
	for range it {
	}
	sel, ok = cfg.find("SELECT")
	require.True(t, ok)
	assert.Contains(t, sel.Query, `AS OF SYSTEM TIME '-5s'`)
	assert.True(t, strings.HasSuffix(sel.Query, " FOR SHARE"), sel.Query)
}

func TestPgFetchOptionsAsOfUnsupported(t *testing.T) {
	cfg, _, ctx := newStubBackend(t, psql.WithVariant(psql.VariantPostgreSQL))
	cfg.rows = softItemRows(nil)
	_, err := psql.Fetch[coreSoftItem](ctx, nil, psql.AsOfSystemTime("-5s"))
	require.Error(t, err)
	assert.ErrorIs(t, err, psql.ErrNotSupported)
	_, ok := cfg.find("SELECT")
	assert.False(t, ok, "no query must be sent")
}

// ---------------------------------------------------------------------------
// Rendering without a backend is permissive
// ---------------------------------------------------------------------------

func TestPgUnknownEngineIsPermissive(t *testing.T) {
	ctx := psql.NewBackend(psql.EngineUnknown, nil).Plug(context.Background())
	sql, err := psql.B().Select().DistinctOn("a").From("t").SetLockMode(psql.LockShare).Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `SELECT DISTINCT ON ("a") * FROM "t" FOR SHARE`, sql)

	sql, err = psql.B().Update("t").Set(map[string]any{"a": psql.Excluded("a")}).Returning("id").Render(ctx)
	require.NoError(t, err)
	assert.Equal(t, `UPDATE "t" SET "a"=VALUES("a") RETURNING "id"`, sql)
}
