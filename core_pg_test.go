package psql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Unit tests for the PostgreSQL / CockroachDB oriented features: autoinc
// columns, transaction retries and bulk inserts. They use the stub
// database/sql driver defined in core_fixes_test.go.

// corePgStubEngine is a second private engine whose dialect (pgStubDialect)
// implements the optional interfaces exercised here: RetryableChecker,
// ReturningRenderer, UpsertRenderer and BulkInserter, with PostgreSQL-style
// placeholders. coreStubEngine (99) keeps its minimal dialect; 98 is taken by lock_test.go.
const corePgStubEngine = psql.Engine(97)

// errStubRetryable is the only error pgStubDialect reports as retryable.
var errStubRetryable = errors.New("stub: serialization failure (40001)")

// pgStubBulk, when set, answers BulkInserter.BulkInsert; when nil the
// dialect declines with ErrNotSupported so the core falls back to INSERT.
var pgStubBulk atomic.Pointer[func(table string, cols []string, rows [][]any) error]

type pgStubDialect struct{}

func (pgStubDialect) Placeholder(n int) string    { return fmt.Sprintf("$%d", n) }
func (pgStubDialect) ExportArg(v any) any         { return psql.DefaultExportArg(v) }
func (pgStubDialect) LimitOffset(a, b int) string { return fmt.Sprintf("LIMIT %d OFFSET %d", b, a) }
func (pgStubDialect) SupportsReturning() bool     { return true }
func (pgStubDialect) IsRetryable(err error) bool  { return errors.Is(err, errStubRetryable) }
func (pgStubDialect) InsertIgnoreSQL(t, f, p string) string {
	return "INSERT INTO " + psql.QuoteName(t) + " (" + f + ") VALUES (" + p + ") ON CONFLICT DO NOTHING"
}
func (pgStubDialect) ReplaceSQL(t, f, p string, _ *psql.StructKey, _ []*psql.StructField) string {
	return "INSERT INTO " + psql.QuoteName(t) + " (" + f + ") VALUES (" + p + ") ON CONFLICT DO UPDATE"
}
func (pgStubDialect) BulkInsert(_ context.Context, _ *psql.Backend, table string, cols []string, rows [][]any) (int64, error) {
	if fn := pgStubBulk.Load(); fn != nil {
		return int64(len(rows)), (*fn)(table, cols, rows)
	}
	return 0, fmt.Errorf("stub COPY: %w", psql.ErrNotSupported)
}

func init() {
	psql.RegisterDialect(corePgStubEngine, pgStubDialect{})
}

// newPgStubBackend returns a backend on a fresh stub database using the
// pgStubDialect engine.
func newPgStubBackend(t *testing.T) (*stubConfig, *psql.Backend, context.Context) {
	t.Helper()
	name := t.Name() + "/pg"
	cfg := &stubConfig{}
	stubRegistry.Lock()
	stubRegistry.m[name] = cfg
	stubRegistry.Unlock()

	db, err := sql.Open(coreStubDriverName, name)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	be := psql.NewBackend(corePgStubEngine, db)
	return cfg, be, be.Plug(context.Background())
}

// ---------------------------------------------------------------------------
// 1. autoinc attribute
// ---------------------------------------------------------------------------

type coreAutoItem struct {
	psql.Name `sql:"core_auto_item"`
	ID        uint64 `sql:",key=PRIMARY,autoinc"`
	Label     string `sql:",type=VARCHAR,size=64"`
}

type coreAutoPtrItem struct {
	psql.Name `sql:"core_auto_ptr_item"`
	ID        *int64 `sql:",key=PRIMARY,autoinc=1"`
	Label     string `sql:",type=VARCHAR,size=64"`
}

type coreAutoOffItem struct {
	psql.Name `sql:"core_auto_off_item"`
	ID        int64 `sql:",key=PRIMARY,autoinc=0"`
}

type coreAutoBadItem struct {
	psql.Name `sql:"core_auto_bad_item"`
	ID        string `sql:",key=PRIMARY,autoinc"`
}

func TestCorePgAutoIncAttribute(t *testing.T) {
	be := psql.NewBackend(coreStubEngine, nil)

	tm := psql.Table[coreAutoItem]()
	f := tm.AutoIncField()
	require.NotNil(t, f, "autoinc field must be registered")
	assert.Equal(t, "ID", f.Name)
	assert.True(t, f.IsAutoInc())
	assert.False(t, tm.FieldByColumn("Label").IsAutoInc())
	// the attribute must not prevent type inference, and must survive
	// resolution so drivers see it in FieldDef
	attrs := f.GetAttrs(be)
	assert.Equal(t, "BIGINT", attrs["type"], "type must still be inferred from uint64")
	assert.Equal(t, "1", attrs["autoinc"])
	assert.Equal(t, "0", attrs["null"], "primary key stays NOT NULL")
	assert.Equal(t, "1", f.Attrs["autoinc"], "declared attrs carry autoinc=1")
	assert.Equal(t, "uint64", f.Attrs["import"])

	// autoinc=1 on a pointer field
	pf := psql.Table[coreAutoPtrItem]().AutoIncField()
	require.NotNil(t, pf)
	assert.Equal(t, "BIGINT", pf.GetAttrs(be)["type"])

	// autoinc=0 means no autoinc, and the attribute is dropped
	off := psql.Table[coreAutoOffItem]()
	assert.Nil(t, off.AutoIncField())
	_, has := off.FieldByColumn("ID").Attrs["autoinc"]
	assert.False(t, has)

	// plain tables have none
	assert.Nil(t, psql.Table[coreSoftItem]().AutoIncField())

	// a non-integer field cannot be autoinc
	assert.PanicsWithValue(t, "psql: field coreAutoBadItem.ID: autoinc requires an integer field, got string", func() {
		psql.Table[coreAutoBadItem]()
	})
}

func TestCorePgAutoIncInsertOmitsColumn(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)
	cfg.lastInsertID = 42

	// zero key: column omitted, value populated from LastInsertId
	obj := &coreAutoItem{Label: "a"}
	require.NoError(t, psql.Insert(ctx, obj))
	ins, ok := cfg.find("INSERT")
	require.True(t, ok)
	assert.Equal(t, `INSERT INTO "core_auto_item" ("Label") VALUES (?)`, ins.Query)
	assert.Equal(t, []driver.Value{"a"}, ins.Args)
	assert.Equal(t, uint64(42), obj.ID)

	// explicit key: column included and kept
	cfg.reset()
	obj2 := &coreAutoItem{ID: 7, Label: "b"}
	require.NoError(t, psql.Insert(ctx, obj2))
	ins, ok = cfg.find("INSERT")
	require.True(t, ok)
	assert.Equal(t, `INSERT INTO "core_auto_item" ("ID","Label") VALUES (?,?)`, ins.Query)
	assert.Equal(t, []driver.Value{int64(7), "b"}, ins.Args)
	assert.Equal(t, uint64(7), obj2.ID)

	// one call mixing both kinds uses two statements, in target order
	cfg.reset()
	items := []*coreAutoItem{{Label: "x"}, {ID: 3, Label: "y"}, {Label: "z"}}
	require.NoError(t, psql.Insert(ctx, items...))
	assert.Equal(t, []string{
		`INSERT INTO "core_auto_item" ("Label") VALUES (?)`,
		`INSERT INTO "core_auto_item" ("ID","Label") VALUES (?,?)`,
		`INSERT INTO "core_auto_item" ("Label") VALUES (?)`,
	}, cfg.queries())
	assert.Equal(t, uint64(42), items[0].ID)
	assert.Equal(t, uint64(3), items[1].ID)
	assert.Equal(t, uint64(42), items[2].ID)

	// nil pointer key is omitted and populated too
	cfg.reset()
	p := &coreAutoPtrItem{Label: "p"}
	require.NoError(t, psql.Insert(ctx, p))
	ins, ok = cfg.find("INSERT")
	require.True(t, ok)
	assert.Equal(t, `INSERT INTO "core_auto_ptr_item" ("Label") VALUES (?)`, ins.Query)
	require.NotNil(t, p.ID)
	assert.Equal(t, int64(42), *p.ID)

	// InsertIgnore and Replace omit the column as well
	cfg.reset()
	require.NoError(t, psql.InsertIgnore(ctx, &coreAutoItem{Label: "i"}))
	ign, ok := cfg.find("INSERT IGNORE")
	require.True(t, ok)
	assert.Equal(t, `INSERT IGNORE INTO "core_auto_item" ("Label") VALUES (?)`, ign.Query)
	cfg.reset()
	require.NoError(t, psql.Replace(ctx, &coreAutoItem{Label: "r"}))
	rep, ok := cfg.find("REPLACE")
	require.True(t, ok)
	assert.Equal(t, `REPLACE INTO "core_auto_item" ("Label") VALUES (?)`, rep.Query)
	cfg.reset()
	require.NoError(t, psql.Replace(ctx, &coreAutoItem{ID: 9, Label: "r"}))
	rep, ok = cfg.find("REPLACE")
	require.True(t, ok)
	assert.Equal(t, `REPLACE INTO "core_auto_item" ("ID","Label") VALUES (?,?)`, rep.Query)
}

func TestCorePgAutoIncInsertReturning(t *testing.T) {
	cfg, _, ctx := newPgStubBackend(t)
	cfg.rows = func(q string, _ []driver.Value) *stubRowsSpec {
		if strings.Contains(q, "RETURNING") {
			return &stubRowsSpec{Cols: []string{"ID", "Label"}, Data: [][]driver.Value{{int64(1001), "a"}}}
		}
		return nil
	}

	obj := &coreAutoItem{Label: "a"}
	require.NoError(t, psql.Insert(ctx, obj))
	ins, ok := cfg.find("INSERT")
	require.True(t, ok)
	assert.Equal(t, `INSERT INTO "core_auto_item" ("Label") VALUES ($1) RETURNING "ID","Label"`, ins.Query)
	assert.Equal(t, uint64(1001), obj.ID, "key must come back through RETURNING")
}

func TestCorePgAutoIncUpdateSkipsColumn(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)

	require.NoError(t, psql.Update(ctx, &coreAutoItem{ID: 5, Label: "l"}))
	upd, ok := cfg.find("UPDATE")
	require.True(t, ok)
	assert.Equal(t, `UPDATE "core_auto_item" SET "Label" = ? WHERE "ID" = ?`, upd.Query)
	assert.Equal(t, []driver.Value{"l", int64(5)}, upd.Args)
}

// ---------------------------------------------------------------------------
// 2. transaction retries
// ---------------------------------------------------------------------------

func TestCorePgIsRetryable(t *testing.T) {
	assert.False(t, psql.IsRetryable(nil))
	assert.False(t, psql.IsRetryable(errors.New("nope")))
	assert.True(t, psql.IsRetryable(errStubRetryable))
	assert.True(t, psql.IsRetryable(fmt.Errorf("wrapped: %w", errStubRetryable)))
	assert.True(t, psql.IsRetryable(&psql.Error{Query: "x", Err: errStubRetryable}))
}

func TestCorePgTxRetriesThenCommits(t *testing.T) {
	cfg, _, ctx := newPgStubBackend(t)

	var calls int32
	var waits []int
	opts := &psql.TxOptions{Backoff: func(attempt int) time.Duration {
		waits = append(waits, attempt)
		return 0
	}}
	err := psql.TxWithOptions(ctx, opts, func(txCtx context.Context) error {
		calls++
		if err := psql.Q("INSERT INTO t VALUES (?)", int(calls)).Exec(txCtx); err != nil {
			return err
		}
		if calls < 3 {
			return &psql.Error{Query: "INSERT", Err: errStubRetryable}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls, "callback must be re-run until it succeeds")
	assert.Equal(t, []int{1, 2}, waits, "backoff is asked for each retry")
	assert.Equal(t, []string{
		"BEGIN", "INSERT INTO t VALUES (?)", "ROLLBACK",
		"BEGIN", "INSERT INTO t VALUES (?)", "ROLLBACK",
		"BEGIN", "INSERT INTO t VALUES (?)", "COMMIT",
	}, cfg.queries())
}

func TestCorePgTxRetriesExhausted(t *testing.T) {
	cfg, _, ctx := newPgStubBackend(t)

	calls := 0
	opts := &psql.TxOptions{MaxRetries: 2, Backoff: func(int) time.Duration { return 0 }}
	err := psql.TxWithOptions(ctx, opts, func(context.Context) error {
		calls++
		return errStubRetryable
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, psql.ErrTxRetriesExhausted)
	assert.ErrorIs(t, err, errStubRetryable, "the last error stays reachable")
	assert.Equal(t, 3, calls, "MaxRetries=2 means three attempts")
	assert.Equal(t, []string{"BEGIN", "ROLLBACK", "BEGIN", "ROLLBACK", "BEGIN", "ROLLBACK"}, cfg.queries())

	// a negative MaxRetries disables retrying; the error is returned as is
	cfg.reset()
	calls = 0
	err = psql.TxWithOptions(ctx, &psql.TxOptions{MaxRetries: -1}, func(context.Context) error {
		calls++
		return errStubRetryable
	})
	assert.Equal(t, errStubRetryable, err)
	assert.NotErrorIs(t, err, psql.ErrTxRetriesExhausted)
	assert.Equal(t, 1, calls)
}

func TestCorePgTxNonRetryableRunsOnce(t *testing.T) {
	cfg, _, ctx := newPgStubBackend(t)

	boom := errors.New("boom")
	calls := 0
	err := psql.Tx(ctx, func(context.Context) error {
		calls++
		return boom
	})
	assert.Equal(t, boom, err)
	assert.Equal(t, 1, calls)
	assert.Equal(t, []string{"BEGIN", "ROLLBACK"}, cfg.queries())

	// an engine without RetryableChecker never retries
	cfg2, _, ctx2 := newStubBackend(t)
	calls = 0
	err = psql.Tx(ctx2, func(context.Context) error {
		calls++
		return errStubRetryable
	})
	assert.Equal(t, errStubRetryable, err)
	assert.Equal(t, 1, calls)
	assert.Equal(t, []string{"BEGIN", "ROLLBACK"}, cfg2.queries())
}

func TestCorePgTxNestedNotRetried(t *testing.T) {
	cfg, _, ctx := newPgStubBackend(t)

	inner, outer := 0, 0
	zero := &psql.TxOptions{Backoff: func(int) time.Duration { return 0 }}
	err := psql.TxWithOptions(ctx, zero, func(txCtx context.Context) error {
		outer++
		err := psql.TxWithOptions(txCtx, zero, func(context.Context) error {
			inner++
			if outer < 2 {
				return errStubRetryable
			}
			return nil
		})
		return err // the nested error propagates so the outer tx retries
	})
	require.NoError(t, err)
	assert.Equal(t, 2, outer, "outer transaction retried once")
	assert.Equal(t, 2, inner, "inner transaction ran once per outer attempt")
	assert.Equal(t, []string{
		"BEGIN", "SAVEPOINT L1", "ROLLBACK TO SAVEPOINT L1", "ROLLBACK",
		"BEGIN", "SAVEPOINT L1", "RELEASE SAVEPOINT L1", "COMMIT",
	}, cfg.queries())
}

func TestCorePgTxRetryHonoursContext(t *testing.T) {
	_, _, ctx := newPgStubBackend(t)
	ctx, cancel := context.WithCancel(ctx)

	calls := 0
	opts := &psql.TxOptions{Backoff: func(int) time.Duration { return time.Hour }}
	err := psql.TxWithOptions(ctx, opts, func(context.Context) error {
		calls++
		cancel() // cancelled while the retry wait is pending
		return errStubRetryable
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, err, errStubRetryable)
	assert.Equal(t, 1, calls, "no retry after cancellation")
}

// txOptsDriver is a minimal driver recording the driver.TxOptions it is
// given, to check the TxOptions → sql.TxOptions mapping.
type txOptsDriver struct {
	last atomic.Pointer[driver.TxOptions]
}

func (d *txOptsDriver) Open(string) (driver.Conn, error) { return &txOptsConn{d: d}, nil }

type txOptsConn struct{ d *txOptsDriver }

func (c *txOptsConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *txOptsConn) Close() error                        { return nil }
func (c *txOptsConn) Begin() (driver.Tx, error)           { return txOptsTx{}, nil }
func (c *txOptsConn) BeginTx(_ context.Context, o driver.TxOptions) (driver.Tx, error) {
	c.d.last.Store(&o)
	return txOptsTx{}, nil
}

type txOptsTx struct{}

func (txOptsTx) Commit() error   { return nil }
func (txOptsTx) Rollback() error { return nil }

func TestCorePgTxOptionsIsolation(t *testing.T) {
	drv := &txOptsDriver{}
	sql.Register("psql-core-txopts", drv)
	db, err := sql.Open("psql-core-txopts", "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx := psql.NewBackend(corePgStubEngine, db).Plug(context.Background())

	err = psql.TxWithOptions(ctx, &psql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true}, func(context.Context) error {
		return nil
	})
	require.NoError(t, err)
	got := drv.last.Load()
	require.NotNil(t, got)
	assert.Equal(t, driver.IsolationLevel(sql.LevelSerializable), got.Isolation)
	assert.True(t, got.ReadOnly)

	// nil options: defaults
	require.NoError(t, psql.Tx(ctx, func(context.Context) error { return nil }))
	got = drv.last.Load()
	require.NotNil(t, got)
	assert.Equal(t, driver.IsolationLevel(sql.LevelDefault), got.Isolation)
	assert.False(t, got.ReadOnly)
}

// ---------------------------------------------------------------------------
// 3. BulkInsert
// ---------------------------------------------------------------------------

type coreBulkItem struct {
	psql.Name `sql:"core_bulk_item"`
	ID        int64  `sql:",key=PRIMARY"`
	Label     string `sql:",type=VARCHAR,size=64"`
	log       *[]string
}

func (b *coreBulkItem) BeforeSave(context.Context) error   { b.note("before_save"); return nil }
func (b *coreBulkItem) BeforeInsert(context.Context) error { b.note("before_insert"); return nil }
func (b *coreBulkItem) AfterInsert(context.Context) error  { b.note("after_insert"); return nil }
func (b *coreBulkItem) AfterSave(context.Context) error    { b.note("after_save"); return nil }
func (b *coreBulkItem) note(s string) {
	if b.log != nil {
		*b.log = append(*b.log, fmt.Sprintf("%s:%s", s, b.Label))
	}
}

func TestCorePgBulkInsertShape(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)

	require.NoError(t, psql.BulkInsert[coreBulkItem](ctx, nil), "empty input is a no-op")
	assert.Empty(t, cfg.queries())

	var log []string
	rows := []*coreBulkItem{
		{ID: 1, Label: "a", log: &log}, {ID: 2, Label: "b", log: &log}, {ID: 3, Label: "c", log: &log},
	}
	require.NoError(t, psql.BulkInsert(ctx, rows, psql.BulkBatchSize(2)))
	stmts := cfg.statements()
	require.Len(t, stmts, 2, "3 rows with batch size 2 need two statements")
	assert.Equal(t, `INSERT INTO "core_bulk_item" ("ID","Label") VALUES (?,?),(?,?)`, stmts[0].Query)
	assert.Equal(t, []driver.Value{int64(1), "a", int64(2), "b"}, stmts[0].Args)
	assert.Equal(t, `INSERT INTO "core_bulk_item" ("ID","Label") VALUES (?,?)`, stmts[1].Query)
	assert.Equal(t, []driver.Value{int64(3), "c"}, stmts[1].Args)

	// Before* hooks run for every row up front, After* per batch afterwards
	assert.Equal(t, []string{
		"before_save:a", "before_insert:a", "before_save:b", "before_insert:b", "before_save:c", "before_insert:c",
		"after_insert:a", "after_save:a", "after_insert:b", "after_save:b",
		"after_insert:c", "after_save:c",
	}, log)

	// BulkNoHooks
	cfg.reset()
	log = nil
	require.NoError(t, psql.BulkInsert(ctx, rows, psql.BulkNoHooks()))
	assert.Empty(t, log)
	assert.Len(t, cfg.queries(), 1)

	// BulkIgnore without an UpsertRenderer: generic INSERT IGNORE
	cfg.reset()
	require.NoError(t, psql.BulkInsert(ctx, rows, psql.BulkIgnore(), psql.BulkNoHooks()))
	assert.Equal(t, `INSERT IGNORE INTO "core_bulk_item" ("ID","Label") VALUES (?,?),(?,?),(?,?)`, cfg.queries()[0])

	// a failing hook stops before anything is written
	cfg.reset()
	bad := &coreBulkFail{}
	err := psql.BulkInsert(ctx, []*coreBulkFail{bad})
	assert.EqualError(t, err, "no way")
	assert.Empty(t, cfg.queries())
}

type coreBulkFail struct {
	psql.Name `sql:"core_bulk_fail"`
	ID        int64 `sql:",key=PRIMARY"`
}

func (*coreBulkFail) BeforeInsert(context.Context) error { return errors.New("no way") }

func TestCorePgBulkInsertParamLimit(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)

	// the stub engine allows 32766 parameters: 2 columns → 16383 rows per
	// statement even with a huge batch size
	n := 20000
	rows := make([]*coreBulkItem, n)
	for i := range rows {
		rows[i] = &coreBulkItem{ID: int64(i + 1), Label: "x"}
	}
	require.NoError(t, psql.BulkInsert(ctx, rows, psql.BulkBatchSize(100000), psql.BulkNoHooks()))
	stmts := cfg.statements()
	require.Len(t, stmts, 2)
	assert.Len(t, stmts[0].Args, 16383*2)
	assert.Len(t, stmts[1].Args, (n-16383)*2)
	assert.Equal(t, 16383, strings.Count(stmts[0].Query, "(?,?)"))
}

func TestCorePgBulkInsertAutoIncShapes(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)
	cfg.lastInsertID = 100

	rows := []*coreAutoItem{{Label: "a"}, {Label: "b"}, {ID: 50, Label: "c"}, {Label: "d"}}
	require.NoError(t, psql.BulkInsert(ctx, rows))
	assert.Equal(t, []string{
		`INSERT INTO "core_auto_item" ("Label") VALUES (?),(?)`,
		`INSERT INTO "core_auto_item" ("ID","Label") VALUES (?,?)`,
		`INSERT INTO "core_auto_item" ("Label") VALUES (?)`,
	}, cfg.queries(), "batches never mix rows with and without the autoinc column")
	// the stub engine is not MySQL: ids are not guessed from LastInsertId
	assert.Equal(t, uint64(0), rows[0].ID)
	assert.Equal(t, uint64(50), rows[2].ID)
}

func TestCorePgBulkInsertReturning(t *testing.T) {
	cfg, _, ctx := newPgStubBackend(t)
	cfg.rows = func(q string, args []driver.Value) *stubRowsSpec {
		if !strings.Contains(q, "RETURNING") {
			return nil
		}
		// echo the labels back with generated ids
		var data [][]driver.Value
		for i, a := range args {
			data = append(data, []driver.Value{int64(1000 + i), a})
		}
		return &stubRowsSpec{Cols: []string{"ID", "Label"}, Data: data}
	}

	rows := []*coreAutoItem{{Label: "a"}, {Label: "b"}, {Label: "c"}}
	require.NoError(t, psql.BulkInsert(ctx, rows, psql.BulkBatchSize(2)))
	stmts := cfg.statements()
	require.Len(t, stmts, 2)
	assert.Equal(t, `INSERT INTO "core_auto_item" ("Label") VALUES ($1),($2) RETURNING "ID","Label"`, stmts[0].Query)
	assert.Equal(t, `INSERT INTO "core_auto_item" ("Label") VALUES ($1) RETURNING "ID","Label"`, stmts[1].Query)
	assert.Equal(t, uint64(1000), rows[0].ID)
	assert.Equal(t, uint64(1001), rows[1].ID)
	assert.Equal(t, uint64(1000), rows[2].ID, "second batch starts a new id sequence in the stub")
	assert.False(t, psql.HasChanged(rows[0]), "objects refreshed from RETURNING carry a row state")

	// BulkIgnore: dialect's ON CONFLICT DO NOTHING, no RETURNING (skipped rows
	// could not be matched to objects)
	cfg.reset()
	require.NoError(t, psql.BulkInsert(ctx, rows, psql.BulkIgnore()))
	assert.Equal(t, `INSERT INTO "core_auto_item" ("ID","Label") VALUES ($1,$2),($3,$4),($5,$6) ON CONFLICT DO NOTHING`, cfg.queries()[0])
}

func TestCorePgBulkInsertNative(t *testing.T) {
	cfg, _, ctx := newPgStubBackend(t)

	var gotTable string
	var gotCols []string
	var gotRows [][]any
	fn := func(table string, cols []string, rows [][]any) error {
		gotTable, gotCols, gotRows = table, cols, rows
		return nil
	}
	pgStubBulk.Store(&fn)
	t.Cleanup(func() { pgStubBulk.Store(nil) })

	var log []string
	rows := []*coreBulkItem{{ID: 1, Label: "a", log: &log}, {ID: 2, Label: "b", log: &log}}
	require.NoError(t, psql.BulkInsert(ctx, rows))
	assert.Empty(t, cfg.queries(), "the BulkInserter must have been used instead of INSERT")
	assert.Equal(t, "core_bulk_item", gotTable)
	assert.Equal(t, []string{"ID", "Label"}, gotCols)
	assert.Equal(t, [][]any{{int64(1), "a"}, {int64(2), "b"}}, gotRows)
	assert.Equal(t, []string{
		"before_save:a", "before_insert:a", "before_save:b", "before_insert:b",
		"after_insert:a", "after_save:a", "after_insert:b", "after_save:b",
	}, log)

	// rows needing a generated key bypass the BulkInserter
	gotRows = nil
	require.NoError(t, psql.BulkInsert(ctx, []*coreAutoItem{{Label: "k"}}))
	assert.Nil(t, gotRows)
	assert.Contains(t, cfg.queries()[0], "RETURNING")

	// a legacy integer primary key left at zero also needs the key
	cfg.reset()
	require.NoError(t, psql.BulkInsert(ctx, []*coreBulkItem{{Label: "z"}}, psql.BulkNoHooks()))
	assert.Nil(t, gotRows)
	assert.Len(t, cfg.queries(), 1)

	// BulkIgnore bypasses it too
	cfg.reset()
	require.NoError(t, psql.BulkInsert(ctx, rows, psql.BulkIgnore(), psql.BulkNoHooks()))
	assert.Nil(t, gotRows)
	assert.Contains(t, cfg.queries()[0], "ON CONFLICT DO NOTHING")

	// a BulkInserter error is wrapped as *psql.Error
	boom := errors.New("copy failed")
	fn2 := func(string, []string, [][]any) error { return boom }
	pgStubBulk.Store(&fn2)
	err := psql.BulkInsert(ctx, rows, psql.BulkNoHooks())
	var perr *psql.Error
	require.ErrorAs(t, err, &perr)
	assert.ErrorIs(t, err, boom)

	// ErrNotSupported from the BulkInserter falls back to INSERT
	pgStubBulk.Store(nil)
	cfg.reset()
	require.NoError(t, psql.BulkInsert(ctx, rows, psql.BulkNoHooks()))
	assert.Contains(t, cfg.queries()[0], `INSERT INTO "core_bulk_item"`)
}
