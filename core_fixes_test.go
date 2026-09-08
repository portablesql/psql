package psql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Stub database/sql driver
//
// Every statement executed through the connection is recorded (with its
// arguments) so tests can assert on the SQL psql produced and on the order of
// transaction statements. Queries can be answered with configured rows.
// ---------------------------------------------------------------------------

const coreStubDriverName = "psql-core-stub"

// coreStubEngine is a private engine value with a stub dialect registered in
// init, so the tests do not interfere with the real engines' dialects.
const coreStubEngine = psql.Engine(99)

type stubStatement struct {
	Query string
	Args  []driver.Value
}

type stubRowsSpec struct {
	Cols   []string
	Data   [][]driver.Value
	ErrAt  int   // if > 0, Next fails with Err after ErrAt rows
	Err    error // error returned by Next when ErrAt is reached
	closed *atomic.Int32
}

type stubConfig struct {
	mu           sync.Mutex
	stmts        []stubStatement
	opens        int
	rows         func(query string, args []driver.Value) *stubRowsSpec
	execErr      func(query string) error
	lastInsertID int64
}

func (c *stubConfig) record(q string, args []driver.Value) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stmts = append(c.stmts, stubStatement{Query: q, Args: args})
}

func (c *stubConfig) queries() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	res := make([]string, len(c.stmts))
	for i, s := range c.stmts {
		res[i] = s.Query
	}
	return res
}

func (c *stubConfig) statements() []stubStatement {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]stubStatement(nil), c.stmts...)
}

func (c *stubConfig) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stmts = nil
}

func (c *stubConfig) openCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens
}

// find returns the first recorded query containing needle.
func (c *stubConfig) find(needle string) (stubStatement, bool) {
	for _, s := range c.statements() {
		if strings.Contains(s.Query, needle) {
			return s, true
		}
	}
	return stubStatement{}, false
}

var stubRegistry = struct {
	sync.Mutex
	m map[string]*stubConfig
}{m: make(map[string]*stubConfig)}

type stubDriver struct{}

func (stubDriver) Open(name string) (driver.Conn, error) {
	stubRegistry.Lock()
	cfg := stubRegistry.m[name]
	stubRegistry.Unlock()
	if cfg == nil {
		return nil, fmt.Errorf("unknown stub database %q", name)
	}
	cfg.mu.Lock()
	cfg.opens++
	cfg.mu.Unlock()
	return &stubConn{cfg: cfg}, nil
}

type stubConn struct{ cfg *stubConfig }

func (c *stubConn) Prepare(q string) (driver.Stmt, error) { return &stubStmt{c: c, q: q}, nil }
func (c *stubConn) Close() error                          { return nil }
func (c *stubConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *stubConn) BeginTx(_ context.Context, _ driver.TxOptions) (driver.Tx, error) {
	c.cfg.record("BEGIN", nil)
	return &stubTx{c: c}, nil
}

func namedToValues(args []driver.NamedValue) []driver.Value {
	res := make([]driver.Value, len(args))
	for i, a := range args {
		res[i] = a.Value
	}
	return res
}

func (c *stubConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	return c.exec(q, namedToValues(args))
}

func (c *stubConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.query(q, args)
}

func (c *stubConn) exec(q string, args []driver.Value) (driver.Result, error) {
	c.cfg.record(q, args)
	if c.cfg.execErr != nil {
		if err := c.cfg.execErr(q); err != nil {
			return nil, err
		}
	}
	return stubResult{lastID: c.cfg.lastInsertID}, nil
}

func (c *stubConn) query(q string, args []driver.NamedValue) (driver.Rows, error) {
	vals := namedToValues(args)
	c.cfg.record(q, vals)
	if c.cfg.rows != nil {
		if spec := c.cfg.rows(q, vals); spec != nil {
			return &stubRows{spec: spec}, nil
		}
	}
	return &stubRows{spec: &stubRowsSpec{}}, nil
}

type stubStmt struct {
	c *stubConn
	q string
}

func (s *stubStmt) Close() error  { return nil }
func (s *stubStmt) NumInput() int { return -1 }
func (s *stubStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.c.exec(s.q, args)
}
func (s *stubStmt) Query(args []driver.Value) (driver.Rows, error) {
	named := make([]driver.NamedValue, len(args))
	for i, a := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: a}
	}
	return s.c.query(s.q, named)
}

type stubTx struct{ c *stubConn }

func (t *stubTx) Commit() error   { t.c.cfg.record("COMMIT", nil); return nil }
func (t *stubTx) Rollback() error { t.c.cfg.record("ROLLBACK", nil); return nil }

type stubResult struct{ lastID int64 }

func (r stubResult) LastInsertId() (int64, error) { return r.lastID, nil }
func (r stubResult) RowsAffected() (int64, error) { return 1, nil }

type stubRows struct {
	spec *stubRowsSpec
	i    int
}

func (r *stubRows) Columns() []string { return r.spec.Cols }
func (r *stubRows) Close() error {
	if r.spec.closed != nil {
		r.spec.closed.Add(1)
	}
	return nil
}
func (r *stubRows) Next(dest []driver.Value) error {
	if r.spec.ErrAt > 0 && r.i >= r.spec.ErrAt {
		return r.spec.Err
	}
	if r.i >= len(r.spec.Data) {
		return io.EOF
	}
	copy(dest, r.spec.Data[r.i])
	r.i++
	return nil
}

// stubDialect is registered for coreStubEngine. Its schema checker calls the
// hook registered for the backend (see stubCheckHooks).
type stubDialect struct{}

func (stubDialect) Placeholder(_ int) string    { return "?" }
func (stubDialect) ExportArg(v any) any         { return psql.DefaultExportArg(v) }
func (stubDialect) LimitOffset(a, b int) string { return fmt.Sprintf("LIMIT %d OFFSET %d", b, a) }
func (stubDialect) CheckStructure(ctx context.Context, be *psql.Backend, tv psql.TableView) error {
	if h, ok := stubCheckHooks.Load(be); ok {
		return h.(func(context.Context, psql.TableView) error)(ctx, tv)
	}
	return nil
}

var stubCheckHooks sync.Map // *psql.Backend → func(ctx, TableView) error

func init() {
	sql.Register(coreStubDriverName, stubDriver{})
	psql.RegisterDialect(coreStubEngine, stubDialect{})
}

// newStubBackend returns a backend on a fresh stub database.
func newStubBackend(t *testing.T, opts ...psql.BackendOption) (*stubConfig, *psql.Backend, context.Context) {
	t.Helper()
	name := t.Name()
	cfg := &stubConfig{}
	stubRegistry.Lock()
	stubRegistry.m[name] = cfg
	stubRegistry.Unlock()

	db, err := sql.Open(coreStubDriverName, name)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	be := psql.NewBackend(coreStubEngine, db, opts...)
	return cfg, be, be.Plug(context.Background())
}

// ---------------------------------------------------------------------------
// Test tables
// ---------------------------------------------------------------------------

type coreSoftItem struct {
	psql.Name `sql:"core_soft_item"`
	ID        int64  `sql:",key=PRIMARY"`
	Label     string `sql:",type=VARCHAR,size=64"`
	Score     *int64
	DeletedAt *time.Time
}

// stubRowsFor returns rows for SELECTs on core_soft_item.
func softItemRows(data [][]driver.Value) func(string, []driver.Value) *stubRowsSpec {
	return func(q string, _ []driver.Value) *stubRowsSpec {
		if strings.HasPrefix(q, "SELECT") {
			return &stubRowsSpec{Cols: []string{"ID", "Label", "Score", "DeletedAt"}, Data: data}
		}
		return nil
	}
}

// ---------------------------------------------------------------------------
// 1. registration and attribute cache are race free
// ---------------------------------------------------------------------------

type coreRaceItem struct {
	psql.Name `sql:"core_race_item"`
	ID        int64 `sql:",key=PRIMARY"`
	Value     string
	Stamp     time.Time
}

func TestCoreTableRegistrationConcurrent(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	be2 := psql.NewBackend(psql.EnginePostgreSQL, nil)

	const n = 64
	results := make([]*psql.TableMeta[coreRaceItem], n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tm := psql.Table[coreRaceItem]()
			results[i] = tm
			for _, f := range tm.AllFields() {
				_ = f.GetAttrs(be)
				_ = f.GetAttrs(be2)
				_ = f.SqlType(be)
			}
			_ = tm.FormattedName(be)
		}(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		assert.Same(t, results[0], results[i], "every goroutine must get the same TableMeta")
	}
}

// ---------------------------------------------------------------------------
// 2. change tracking with nullable columns
// ---------------------------------------------------------------------------

func TestCoreHasChangedNullableColumns(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)
	cfg.rows = softItemRows([][]driver.Value{
		{int64(1), "one", nil, nil},
		{int64(2), "two", int64(7), "2024-01-02 03:04:05"},
	})

	items, err := psql.Fetch[coreSoftItem](ctx, nil)
	require.NoError(t, err)
	require.Len(t, items, 2)

	// freshly loaded rows are not changed, whatever the NULL-ness of columns
	assert.False(t, psql.HasChanged(items[0]), "row with NULL columns must not be reported as changed")
	assert.False(t, psql.HasChanged(items[1]), "row with non-NULL nullable columns must not be reported as changed")
	assert.Nil(t, items[0].Score)
	assert.Nil(t, items[0].DeletedAt)
	require.NotNil(t, items[1].Score)
	assert.Equal(t, int64(7), *items[1].Score)
	require.NotNil(t, items[1].DeletedAt)

	// modifying the pointee is detected (state is a deep copy)
	*items[1].Score = 8
	assert.True(t, psql.HasChanged(items[1]))
	*items[1].Score = 7
	assert.False(t, psql.HasChanged(items[1]))

	// setting a NULL column is detected, clearing it again is not
	now := time.Now()
	items[0].DeletedAt = &now
	assert.True(t, psql.HasChanged(items[0]))
	items[0].DeletedAt = nil
	assert.False(t, psql.HasChanged(items[0]))

	// Update only writes the changed column
	cfg.reset()
	items[0].Label = "uno"
	require.NoError(t, psql.Update(ctx, items[0]))
	upd, ok := cfg.find("UPDATE")
	require.True(t, ok, "an UPDATE must have been issued")
	assert.Equal(t, `UPDATE "core_soft_item" SET "Label" = ? WHERE "ID" = ?`, upd.Query)
	assert.Equal(t, []driver.Value{"uno", int64(1)}, upd.Args)
	assert.False(t, psql.HasChanged(items[0]), "state must be refreshed after Update")

	// no change → no statement
	cfg.reset()
	require.NoError(t, psql.Update(ctx, items[0]))
	_, ok = cfg.find("UPDATE")
	assert.False(t, ok, "unchanged object must not issue an UPDATE")

	// Update stores a copy of pointer values: mutating the pointee afterwards
	// must be reported as a change
	v := int64(3)
	items[0].Score = &v
	require.NoError(t, psql.Update(ctx, items[0]))
	assert.False(t, psql.HasChanged(items[0]))
	v = 4
	assert.True(t, psql.HasChanged(items[0]), "state must not alias the object's pointer")
}

func TestCoreHasChangedUntrackedObject(t *testing.T) {
	// no state (never loaded) → reported as changed, silently
	assert.True(t, psql.HasChanged(&coreSoftItem{ID: 1}))
}

// ---------------------------------------------------------------------------
// 3. Update SET order is deterministic (sorted)
// ---------------------------------------------------------------------------

type coreOrderItem struct {
	psql.Name `sql:"core_order_item"`
	ID        int64  `sql:",key=PRIMARY"`
	Zeta      string `sql:",type=VARCHAR,size=8"`
	Alpha     string `sql:",type=VARCHAR,size=8"`
	Mid       string `sql:",type=VARCHAR,size=8"`
}

func TestCoreUpdateColumnOrderSorted(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)

	// an object without row state writes every column (including the key),
	// always in the same sorted order
	const want = `UPDATE "core_order_item" SET "Alpha" = ?, "ID" = ?, "Mid" = ?, "Zeta" = ? WHERE "ID" = ?`
	for i := 0; i < 5; i++ {
		cfg.reset()
		require.NoError(t, psql.Update(ctx, &coreOrderItem{ID: 1, Zeta: "z", Alpha: "a", Mid: "m"}))
		upd, ok := cfg.find("UPDATE")
		require.True(t, ok)
		assert.Equal(t, want, upd.Query)
		assert.Equal(t, []driver.Value{"a", int64(1), "m", "z", int64(1)}, upd.Args)
	}
}

// ---------------------------------------------------------------------------
// 4. Get honors Sort; FetchMapped/FetchGrouped validate key and honor options
// ---------------------------------------------------------------------------

func TestCoreGetHonorsSort(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)
	cfg.rows = softItemRows([][]driver.Value{{int64(9), "last", nil, nil}})

	item, err := psql.Get[coreSoftItem](ctx, nil, psql.Sort(psql.S("ID", "DESC")))
	require.NoError(t, err)
	assert.Equal(t, int64(9), item.ID)

	sel, ok := cfg.find("SELECT")
	require.True(t, ok)
	assert.Contains(t, sel.Query, `ORDER BY "ID" DESC`)
	assert.Contains(t, sel.Query, "LIMIT 1")
}

func TestCoreFetchMappedKeyValidation(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)
	cfg.rows = softItemRows([][]driver.Value{
		{int64(1), "a", nil, nil},
		{int64(2), "b", int64(5), nil},
		{int64(3), "a", nil, nil},
	})

	// unknown key → error, not a map with a "<nil>" entry
	_, err := psql.FetchMapped[coreSoftItem](ctx, nil, "NoSuchColumn")
	require.Error(t, err)
	assert.ErrorIs(t, err, psql.ErrUnknownField)
	assert.Contains(t, err.Error(), "NoSuchColumn")
	_, err = psql.FetchGrouped[coreSoftItem](ctx, nil, "NoSuchColumn")
	assert.ErrorIs(t, err, psql.ErrUnknownField)

	// by column name
	m, err := psql.FetchMapped[coreSoftItem](ctx, nil, "ID")
	require.NoError(t, err)
	require.Len(t, m, 3)
	assert.Equal(t, "b", m["2"].Label)

	// by Go field name, pointer key dereferenced
	m, err = psql.FetchMapped[coreSoftItem](ctx, nil, "Score")
	require.NoError(t, err)
	assert.Equal(t, int64(2), m["5"].ID)
	assert.Contains(t, m, "<nil>")

	g, err := psql.FetchGrouped[coreSoftItem](ctx, nil, "Label")
	require.NoError(t, err)
	require.Len(t, g["a"], 2)
	assert.Equal(t, int64(1), g["a"][0].ID)
	assert.Equal(t, int64(3), g["a"][1].ID)
	require.Len(t, g["b"], 1)

	// options are applied like Fetch
	cfg.reset()
	_, err = psql.FetchMapped[coreSoftItem](ctx, nil, "ID", psql.FetchLockSkipLocked, psql.Sort(psql.S("Label", "ASC")), psql.LimitFrom(2, 5))
	require.NoError(t, err)
	sel, ok := cfg.find("SELECT")
	require.True(t, ok)
	assert.Contains(t, sel.Query, "FOR UPDATE SKIP LOCKED")
	assert.Contains(t, sel.Query, `ORDER BY "Label" ASC`)
	assert.Contains(t, sel.Query, "LIMIT 5 OFFSET 2")

	cfg.reset()
	_, err = psql.FetchGrouped[coreSoftItem](ctx, nil, "Label", psql.FetchLockNoWait)
	require.NoError(t, err)
	sel, ok = cfg.find("SELECT")
	require.True(t, ok)
	assert.Contains(t, sel.Query, "FOR UPDATE NOWAIT")
}

// ---------------------------------------------------------------------------
// 5. EscapeTx finds TxProxy
// ---------------------------------------------------------------------------

func TestCoreEscapeTx(t *testing.T) {
	cfg, be, ctx := newStubBackend(t)

	tx, err := psql.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	txCtx := psql.ContextTx(ctx, tx)

	outer, ok := psql.EscapeTx(txCtx)
	assert.True(t, ok, "EscapeTx must find the TxProxy attached by ContextTx")
	assert.Same(t, be, psql.GetBackend(outer), "escaped context keeps the backend")

	// a query on the escaped context does not run on the transaction's connection
	require.Equal(t, 1, cfg.openCount())
	require.NoError(t, psql.Q("SELECT 1").Exec(outer))
	assert.Equal(t, 2, cfg.openCount(), "escaped context must use a connection other than the transaction's")

	// nested: escaping from an inner savepoint context returns the outer tx context
	inner, err := psql.BeginTx(txCtx, nil)
	require.NoError(t, err)
	innerCtx := psql.ContextTx(txCtx, inner)
	esc, ok := psql.EscapeTx(innerCtx)
	assert.True(t, ok)
	assert.Equal(t, txCtx, esc)
	require.NoError(t, inner.Rollback())
}

// ---------------------------------------------------------------------------
// 6. Q().Exec runs on the context's transaction; rows.Err is checked; Iter
// ---------------------------------------------------------------------------

func TestCoreQExecUsesTransaction(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)

	err := psql.Tx(ctx, func(txCtx context.Context) error {
		return psql.Q("INSERT INTO t VALUES (?)", 1).Exec(txCtx)
	})
	require.NoError(t, err)
	assert.Equal(t, 1, cfg.openCount(), "Exec inside Tx must reuse the transaction's connection")
	assert.Equal(t, []string{"BEGIN", "INSERT INTO t VALUES (?)", "COMMIT"}, cfg.queries())
}

func TestCoreDeprecatedExecUsesDefaultBackend(t *testing.T) {
	cfg, be, _ := newStubBackend(t)
	old := psql.DefaultBackend
	psql.DefaultBackend = be
	defer func() { psql.DefaultBackend = old }()

	require.NoError(t, psql.Exec(psql.Q("DELETE FROM t")))
	assert.Equal(t, []string{"DELETE FROM t"}, cfg.queries())
}

func TestCoreRowsErrIsReported(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)
	boom := errors.New("connection lost")
	cfg.rows = func(q string, _ []driver.Value) *stubRowsSpec {
		if strings.HasPrefix(q, "SELECT") {
			return &stubRowsSpec{
				Cols:  []string{"ID", "Label", "Score", "DeletedAt"},
				Data:  [][]driver.Value{{int64(1), "a", nil, nil}, {int64(2), "b", nil, nil}},
				ErrAt: 1, Err: boom,
			}
		}
		return nil
	}

	_, err := psql.Fetch[coreSoftItem](ctx, nil)
	assert.ErrorIs(t, err, boom, "Fetch must report a mid-stream failure")

	_, err = psql.FetchMapped[coreSoftItem](ctx, nil, "ID")
	assert.ErrorIs(t, err, boom)

	_, err = psql.QT[coreSoftItem]("SELECT * FROM x").All(ctx)
	assert.ErrorIs(t, err, boom)

	n := 0
	err = psql.QT[coreSoftItem]("SELECT * FROM x").Each(ctx, func(*coreSoftItem) error { n++; return nil })
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 1, n)

	err = psql.Q("SELECT * FROM x").Each(ctx, func(*sql.Rows) error { return nil })
	assert.ErrorIs(t, err, boom)

	it, err := psql.IterErr[coreSoftItem](ctx, nil)
	require.NoError(t, err)
	var got []int64
	var iterErr error
	for v, err := range it {
		if err != nil {
			iterErr = err
			break
		}
		got = append(got, v.ID)
	}
	assert.Equal(t, []int64{1}, got)
	assert.ErrorIs(t, iterErr, boom)

	iter, err := psql.Iter[coreSoftItem](ctx, nil)
	require.NoError(t, err)
	assert.Panics(t, func() {
		for range iter {
		}
	}, "Iter panics on row errors, as documented")
}

func TestCoreIterClosesRows(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)
	var closed atomic.Int32
	cfg.rows = func(q string, _ []driver.Value) *stubRowsSpec {
		if strings.HasPrefix(q, "SELECT") {
			return &stubRowsSpec{
				Cols:   []string{"ID", "Label", "Score", "DeletedAt"},
				Data:   [][]driver.Value{{int64(1), "a", nil, nil}, {int64(2), "b", nil, nil}, {int64(3), "c", nil, nil}},
				closed: &closed,
			}
		}
		return nil
	}

	// full consumption
	iter, err := psql.Iter[coreSoftItem](ctx, nil)
	require.NoError(t, err)
	n := 0
	for range iter {
		n++
	}
	assert.Equal(t, 3, n)
	assert.Equal(t, int32(1), closed.Load())

	// early break
	iter, err = psql.Iter[coreSoftItem](ctx, nil)
	require.NoError(t, err)
	for range iter {
		break
	}
	assert.Equal(t, int32(2), closed.Load(), "rows must be closed when the loop breaks early")

	// scan error (bad integer) → panic, rows still closed
	cfg.rows = func(q string, _ []driver.Value) *stubRowsSpec {
		if strings.HasPrefix(q, "SELECT") {
			return &stubRowsSpec{
				Cols:   []string{"ID", "Label", "Score", "DeletedAt"},
				Data:   [][]driver.Value{{"not-a-number", "a", nil, nil}},
				closed: &closed,
			}
		}
		return nil
	}
	iter, err = psql.Iter[coreSoftItem](ctx, nil)
	require.NoError(t, err)
	assert.Panics(t, func() {
		for range iter {
		}
	})
	assert.Equal(t, int32(3), closed.Load(), "rows must be closed when scanning panics")

	it, err := psql.IterErr[coreSoftItem](ctx, nil)
	require.NoError(t, err)
	for _, err := range it {
		require.Error(t, err)
		assert.Contains(t, err.Error(), "on field ID")
	}
	assert.Equal(t, int32(4), closed.Load())
}

// ---------------------------------------------------------------------------
// 7. savepoints
// ---------------------------------------------------------------------------

func TestCoreSavepointCommitRelease(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)

	tx, err := psql.BeginTx(ctx, nil)
	require.NoError(t, err)
	txCtx := psql.ContextTx(ctx, tx)

	inner, err := psql.BeginTx(txCtx, nil)
	require.NoError(t, err)
	innerCtx := psql.ContextTx(txCtx, inner)

	deeper, err := psql.BeginTx(innerCtx, nil)
	require.NoError(t, err)
	require.NoError(t, psql.Q("INSERT INTO t VALUES (1)").Exec(psql.ContextTx(innerCtx, deeper)))
	require.NoError(t, deeper.Commit())
	require.NoError(t, inner.Commit())
	require.NoError(t, tx.Commit())

	assert.Equal(t, []string{
		"BEGIN",
		"SAVEPOINT L1",
		"SAVEPOINT L2",
		"INSERT INTO t VALUES (1)",
		"RELEASE SAVEPOINT L2",
		"RELEASE SAVEPOINT L1",
		"COMMIT",
	}, cfg.queries())
	assert.Equal(t, 1, cfg.openCount())
}

func TestCoreSavepointRollback(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)

	err := psql.Tx(ctx, func(txCtx context.Context) error {
		if err := psql.Q("INSERT INTO t VALUES (1)").Exec(txCtx); err != nil {
			return err
		}
		inner := psql.Tx(txCtx, func(innerCtx context.Context) error {
			if err := psql.Q("INSERT INTO t VALUES (2)").Exec(innerCtx); err != nil {
				return err
			}
			return errors.New("undo inner")
		})
		assert.EqualError(t, inner, "undo inner")
		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"BEGIN",
		"INSERT INTO t VALUES (1)",
		"SAVEPOINT L1",
		"INSERT INTO t VALUES (2)",
		"ROLLBACK TO SAVEPOINT L1",
		"COMMIT",
	}, cfg.queries())
}

func TestCoreTxDoubleCommitRollback(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)

	tx, err := psql.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	assert.ErrorIs(t, tx.Commit(), psql.ErrTxAlreadyProcessed)
	assert.ErrorIs(t, tx.Rollback(), psql.ErrTxAlreadyProcessed)

	tx2, err := psql.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, tx2.Rollback())
	assert.ErrorIs(t, tx2.Rollback(), psql.ErrTxAlreadyProcessed)
	assert.ErrorIs(t, tx2.Commit(), psql.ErrTxAlreadyProcessed)

	assert.Equal(t, []string{"BEGIN", "COMMIT", "BEGIN", "ROLLBACK"}, cfg.queries())

	// nested proxies: double finish is also rejected, and only one statement issued
	tx3, err := psql.BeginTx(ctx, nil)
	require.NoError(t, err)
	inner, err := tx3.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, inner.Rollback())
	assert.ErrorIs(t, inner.Commit(), psql.ErrTxAlreadyProcessed)
	assert.ErrorIs(t, inner.Rollback(), psql.ErrTxAlreadyProcessed)
	require.NoError(t, tx3.Commit())
	assert.Equal(t, []string{"BEGIN", "SAVEPOINT L1", "ROLLBACK TO SAVEPOINT L1", "COMMIT"}, cfg.queries()[4:])
}

func TestCoreTxNestedLeftOpen(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)

	tx, err := psql.BeginTx(ctx, nil)
	require.NoError(t, err)
	inner, err := tx.BeginTx(ctx, nil)
	require.NoError(t, err)

	// committing the outer transaction while the inner one is open is an error
	err = tx.Commit()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested transaction")
	assert.Contains(t, err.Error(), "still open")
	assert.NotContains(t, cfg.queries(), "COMMIT")

	// the failed Commit did not consume the outer proxy: once the inner one
	// is finished, the outer one can be committed
	require.NoError(t, inner.Rollback())
	require.NoError(t, tx.Commit())
	assert.Equal(t, []string{"BEGIN", "SAVEPOINT L1", "ROLLBACK TO SAVEPOINT L1", "COMMIT"}, cfg.queries())
	assert.ErrorIs(t, tx.Commit(), psql.ErrTxAlreadyProcessed)
}

// ---------------------------------------------------------------------------
// 8. schema check: retried until success, serialized, optional, explicit
// ---------------------------------------------------------------------------

type coreCheckItem struct {
	psql.Name `sql:"core_check_item"`
	ID        int64 `sql:",key=PRIMARY"`
}

type coreCheckItem2 struct {
	psql.Name `sql:"core_check_item2"`
	ID        int64 `sql:",key=PRIMARY"`
}

func TestCoreSchemaCheckRetriedUntilSuccess(t *testing.T) {
	_, be, ctx := newStubBackend(t)
	var calls atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	stubCheckHooks.Store(be, func(context.Context, psql.TableView) error {
		calls.Add(1)
		if fail.Load() {
			return errors.New("transient failure")
		}
		return nil
	})
	defer stubCheckHooks.Delete(be)

	_, _ = psql.Fetch[coreCheckItem](ctx, nil)
	assert.Equal(t, int32(1), calls.Load())
	_, _ = psql.Fetch[coreCheckItem](ctx, nil)
	assert.Equal(t, int32(2), calls.Load(), "a failed check must be retried by the next operation")

	fail.Store(false)
	_, _ = psql.Fetch[coreCheckItem](ctx, nil)
	assert.Equal(t, int32(3), calls.Load())
	_, _ = psql.Fetch[coreCheckItem](ctx, nil)
	_, _ = psql.Count[coreCheckItem](ctx, nil)
	assert.Equal(t, int32(3), calls.Load(), "a successful check must not run again")
}

func TestCoreSchemaCheckSerialized(t *testing.T) {
	_, be, ctx := newStubBackend(t)
	var calls, running, maxRunning atomic.Int32
	stubCheckHooks.Store(be, func(context.Context, psql.TableView) error {
		calls.Add(1)
		r := running.Add(1)
		for {
			m := maxRunning.Load()
			if r <= m || maxRunning.CompareAndSwap(m, r) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		running.Add(-1)
		return nil
	})
	defer stubCheckHooks.Delete(be)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = psql.Fetch[coreCheckItem2](ctx, nil)
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), calls.Load(), "concurrent first use must run the check once")
	assert.Equal(t, int32(1), maxRunning.Load())
}

func TestCoreSchemaCheckDisabledAndExplicit(t *testing.T) {
	_, be, ctx := newStubBackend(t, psql.WithSchemaCheck(false))
	var calls atomic.Int32
	var seen psql.TableView
	stubCheckHooks.Store(be, func(_ context.Context, tv psql.TableView) error {
		calls.Add(1)
		seen = tv
		return nil
	})
	defer stubCheckHooks.Delete(be)

	assert.False(t, be.SchemaCheckEnabled())
	_, _ = psql.Fetch[coreCheckItem](ctx, nil)
	require.NoError(t, psql.Insert(ctx, &coreCheckItem{ID: 1}))
	assert.Equal(t, int32(0), calls.Load(), "WithSchemaCheck(false) must never issue the check implicitly")

	require.NoError(t, be.CheckStructure(ctx, psql.Table[coreCheckItem]()))
	assert.Equal(t, int32(1), calls.Load())
	require.NotNil(t, seen)
	assert.Equal(t, "core_check_item", seen.FormattedName(be))
	assert.Equal(t, "core_check_item", seen.TableName())
	assert.NotNil(t, seen.FieldByColumn("ID"))

	// once explicitly checked, further explicit calls are no-ops too
	require.NoError(t, be.CheckStructure(ctx, psql.Table[coreCheckItem]()))
	assert.Equal(t, int32(1), calls.Load())

	// enabled backends default
	_, be2, _ := newStubBackend(t)
	assert.True(t, be2.SchemaCheckEnabled())
	var nilBe *psql.Backend
	assert.ErrorIs(t, nilBe.CheckStructure(ctx, psql.Table[coreCheckItem]()), psql.ErrNotReady)
}

type coreNoCheckItem struct {
	psql.Name `sql:"core_nocheck_item,check=0"`
	ID        int64 `sql:",key=PRIMARY"`
}

func TestCoreSchemaCheckAttrPassedThrough(t *testing.T) {
	_, be, ctx := newStubBackend(t)
	var attr string
	stubCheckHooks.Store(be, func(_ context.Context, tv psql.TableView) error {
		attr = tv.TableAttrs()["check"]
		return nil
	})
	defer stubCheckHooks.Delete(be)

	_, _ = psql.Fetch[coreNoCheckItem](ctx, nil)
	assert.Equal(t, "0", attr, "check=0 must reach the dialect's checker")
}

// ---------------------------------------------------------------------------
// 9. namers: one transformation, applied to columns too
// ---------------------------------------------------------------------------

type UserProfile struct {
	psql.Key    `sql:"PRIMARY,type=PRIMARY,fields='UserId'"`
	UserId      int64
	DisplayName string `sql:",type=VARCHAR,size=64"`
	AvatarUrl   string `sql:"avatar_url,type=VARCHAR,size=255"`
}

func TestCoreNamerTableName(t *testing.T) {
	tm := psql.Table[UserProfile]()
	assert.Equal(t, "UserProfile", tm.Name(), "declared name is the Go type name")
	assert.Equal(t, "UserProfile", tm.TableName())

	legacy := psql.NewBackend(coreStubEngine, nil)
	assert.Equal(t, "User_Profile", tm.FormattedName(legacy), "LegacyNamer keeps producing Camel_Snake names")

	def := psql.NewBackend(coreStubEngine, nil, psql.WithNamer(&psql.DefaultNamer{}))
	assert.Equal(t, "UserProfile", tm.FormattedName(def), "DefaultNamer keeps the Go name")

	cs := psql.NewBackend(coreStubEngine, nil, psql.WithNamer(&psql.CamelSnakeNamer{}))
	assert.Equal(t, "User_Profile", tm.FormattedName(cs))

	// nil backend behaves as LegacyNamer
	assert.Equal(t, "User_Profile", tm.FormattedName(nil))
}

func TestCoreNamerColumns(t *testing.T) {
	// LegacyNamer: columns untouched, so existing schemas are unaffected
	cfg, _, ctx := newStubBackend(t)
	_, _ = psql.Fetch[UserProfile](ctx, nil)
	sel, ok := cfg.find("SELECT")
	require.True(t, ok)
	assert.Equal(t, `SELECT "UserId","DisplayName","avatar_url" FROM "User_Profile"`, sel.Query)

	// CamelSnakeNamer: Go field names transformed, explicit tag names kept
	cfg2, be2, ctx2 := newStubBackend(t, psql.WithNamer(&psql.CamelSnakeNamer{}))
	// the schema checker (run automatically on first use) sees the resolved view
	var cols []string
	stubCheckHooks.Store(be2, func(_ context.Context, tv psql.TableView) error {
		for _, f := range tv.AllFields() {
			cols = append(cols, f.Column)
		}
		assert.Equal(t, []string{"User_Id"}, tv.MainKey().Fields)
		assert.NotNil(t, tv.FieldByColumn("Display_Name"))
		assert.Equal(t, "User_Profile", tv.FormattedName(be2))
		assert.Equal(t, "UserProfile", tv.TableName())
		return nil
	})
	defer stubCheckHooks.Delete(be2)
	_, _ = psql.Fetch[UserProfile](ctx2, nil)
	assert.Equal(t, []string{"User_Id", "Display_Name", "avatar_url"}, cols)
	sel, ok = cfg2.find("SELECT")
	require.True(t, ok)
	assert.Equal(t, `SELECT "User_Id","Display_Name","avatar_url" FROM "User_Profile"`, sel.Query)

	// the same TableMeta serves both backends
	assert.Equal(t, "UserId", psql.Table[UserProfile]().AllFields()[0].Column)

	// insert/update/delete use the resolved names, including the key
	cfg2.reset()
	require.NoError(t, psql.Insert(ctx2, &UserProfile{UserId: 1, DisplayName: "x"}))
	ins, ok := cfg2.find("INSERT")
	require.True(t, ok)
	assert.Equal(t, `INSERT INTO "User_Profile" ("User_Id","Display_Name","avatar_url") VALUES (?,?,?)`, ins.Query)

	cfg2.reset()
	require.NoError(t, psql.Update(ctx2, &UserProfile{UserId: 1, DisplayName: "y"}))
	upd, ok := cfg2.find("UPDATE")
	require.True(t, ok)
	assert.Equal(t, `UPDATE "User_Profile" SET "Display_Name" = ?, "User_Id" = ?, "avatar_url" = ? WHERE "User_Id" = ?`, upd.Query)

	// changing the namer on a backend takes effect
	be2.SetNamer(&psql.DefaultNamer{})
	cfg2.reset()
	_, _ = psql.Fetch[UserProfile](ctx2, nil)
	sel, ok = cfg2.find("SELECT")
	require.True(t, ok)
	assert.Equal(t, `SELECT "UserId","DisplayName","avatar_url" FROM "UserProfile"`, sel.Query)
}

// ---------------------------------------------------------------------------
// 10. soft delete attribute and Restore(nil)
// ---------------------------------------------------------------------------

type coreSoftAttr struct {
	psql.Name `sql:"core_soft_attr"`
	ID        int64      `sql:",key=PRIMARY"`
	Removed   *time.Time `sql:",softdelete"`
}

func TestCoreSoftDeleteAttribute(t *testing.T) {
	tm := psql.Table[coreSoftAttr]()
	assert.True(t, tm.HasSoftDelete())
	f := tm.FieldByColumn("Removed")
	require.NotNil(t, f)
	assert.Equal(t, "*time.Time", f.Attrs["import"], "softdelete must not prevent type inference")
	assert.NotContains(t, f.Attrs, "softdelete")
	assert.True(t, f.Nullable)

	cfg, _, ctx := newStubBackend(t)
	_, err := psql.Delete[coreSoftAttr](ctx, map[string]any{"ID": 1})
	require.NoError(t, err)
	q, ok := cfg.find("UPDATE")
	require.True(t, ok)
	assert.Contains(t, q.Query, `SET "Removed"`)
	assert.Contains(t, q.Query, `"Removed" IS NULL`)

	// soft delete filter applied on fetch
	cfg.reset()
	_, _ = psql.Fetch[coreSoftAttr](ctx, nil)
	sel, ok := cfg.find("SELECT")
	require.True(t, ok)
	assert.Contains(t, sel.Query, `"Removed" IS NULL`)
}

func TestCoreRestoreNilWhere(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)

	_, err := psql.Restore[coreSoftItem](ctx, nil)
	require.NoError(t, err)
	q, ok := cfg.find("UPDATE")
	require.True(t, ok)
	assert.Equal(t, `UPDATE "core_soft_item" SET "DeletedAt"=NULL`, q.Query)
	assert.Empty(t, q.Args)

	cfg.reset()
	_, err = psql.Restore[coreSoftItem](ctx, map[string]any{"ID": 3})
	require.NoError(t, err)
	q, ok = cfg.find("UPDATE")
	require.True(t, ok)
	assert.Contains(t, q.Query, `WHERE ("ID"=?)`)
	assert.Equal(t, []driver.Value{int64(3)}, q.Args)

	_, err = psql.Restore[coreOrderItem](ctx, nil)
	assert.ErrorIs(t, err, psql.ErrNotReady, "Restore on a table without soft delete")
}

// ---------------------------------------------------------------------------
// 11. Insert: zero Set is not NULL; LastInsertId populates the key
// ---------------------------------------------------------------------------

type coreInsertItem struct {
	psql.Name `sql:"core_insert_item"`
	ID        int64    `sql:",key=PRIMARY"`
	Flags     psql.Set `sql:",type=SET,null=0,values='a,b'"`
	Blob      []byte
	Note      *string
	Tags      map[string]string `sql:"-"`
}

func TestCoreInsertNilHandling(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)

	require.NoError(t, psql.Insert(ctx, &coreInsertItem{ID: 1}))
	ins, ok := cfg.find("INSERT")
	require.True(t, ok)
	require.Len(t, ins.Args, 4)
	assert.Equal(t, int64(1), ins.Args[0])
	assert.Equal(t, "", ins.Args[1], "zero psql.Set is a driver.Valuer and must not become NULL")
	assert.Nil(t, ins.Args[2], "nil []byte becomes NULL")
	assert.Nil(t, ins.Args[3], "nil pointer becomes NULL")

	cfg.reset()
	note := "n"
	require.NoError(t, psql.Replace(ctx, &coreInsertItem{ID: 2, Flags: psql.Set{"a", "b"}, Blob: []byte{1}, Note: &note}))
	rep, ok := cfg.find("REPLACE")
	require.True(t, ok)
	assert.Equal(t, []driver.Value{int64(2), "a,b", []byte{1}, "n"}, rep.Args)

	cfg.reset()
	require.NoError(t, psql.InsertIgnore(ctx, &coreInsertItem{ID: 3}))
	ign, ok := cfg.find("INSERT IGNORE")
	require.True(t, ok)
	assert.Equal(t, "", ign.Args[1])

	cfg.reset()
	require.NoError(t, psql.Update(ctx, &coreInsertItem{ID: 3}))
	upd, ok := cfg.find("UPDATE")
	require.True(t, ok)
	assert.Equal(t, `UPDATE "core_insert_item" SET "Blob" = ?, "Flags" = ?, "ID" = ?, "Note" = ? WHERE "ID" = ?`, upd.Query)
	assert.Equal(t, []driver.Value{nil, "", int64(3), nil, int64(3)}, upd.Args)
}

func TestCoreInsertLastInsertId(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)
	cfg.lastInsertID = 42

	obj := &coreInsertItem{}
	require.NoError(t, psql.Insert(ctx, obj))
	assert.Equal(t, int64(42), obj.ID, "zero primary key must be populated from LastInsertId")

	explicit := &coreInsertItem{ID: 7}
	require.NoError(t, psql.Insert(ctx, explicit))
	assert.Equal(t, int64(7), explicit.ID, "explicit primary key must be kept")

	// composite / non-integer keys are left alone
	up := &UserProfile{}
	require.NoError(t, psql.Insert(ctx, up))
	assert.Equal(t, int64(42), up.UserId, "psql.Key single integer primary key is populated too")
	o := &coreOrderItem{}
	require.NoError(t, psql.Insert(ctx, o))
	assert.Equal(t, int64(42), o.ID)
	s := &coreSoftItem{}
	require.NoError(t, psql.Insert(ctx, s))
	assert.Equal(t, int64(42), s.ID)
	m := &coreMultiKey{}
	require.NoError(t, psql.Insert(ctx, m))
	assert.Equal(t, int64(0), m.A, "composite primary key is not populated")

	// nil pointer-to-integer key is populated as well
	p := &corePtrKey{}
	require.NoError(t, psql.Insert(ctx, p))
	require.NotNil(t, p.ID)
	assert.Equal(t, uint32(42), *p.ID)
	seven := uint32(7)
	p2 := &corePtrKey{ID: &seven}
	require.NoError(t, psql.Insert(ctx, p2))
	assert.Equal(t, uint32(7), *p2.ID)
}

type corePtrKey struct {
	psql.Name `sql:"core_ptr_key"`
	ID        *uint32 `sql:",key=PRIMARY"`
}

type coreMultiKey struct {
	psql.Name `sql:"core_multi_key"`
	A         int64 `sql:",key=PRIMARY"`
	B         int64 `sql:",key=PRIMARY"`
}

// ---------------------------------------------------------------------------
// 13. errors are wrapped consistently; DeleteOne on nil table
// ---------------------------------------------------------------------------

func TestCoreErrorsWrapped(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)
	boom := errors.New("boom")
	cfg.execErr = func(q string) error {
		if strings.HasPrefix(q, "UPDATE") || strings.HasPrefix(q, "DELETE") {
			return boom
		}
		return nil
	}

	var perr *psql.Error
	err := psql.Update(ctx, &coreOrderItem{ID: 1})
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	require.ErrorAs(t, err, &perr)
	assert.Contains(t, perr.Query, "UPDATE")

	_, err = psql.Delete[coreOrderItem](ctx, map[string]any{"ID": 1})
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	require.ErrorAs(t, err, &perr)
	assert.Contains(t, perr.Query, "DELETE")

	_, err = psql.Delete[coreSoftItem](ctx, map[string]any{"ID": 1})
	require.ErrorAs(t, err, &perr)
	assert.Contains(t, perr.Query, "UPDATE")

	_, err = psql.Restore[coreSoftItem](ctx, nil)
	require.ErrorAs(t, err, &perr)

	err = psql.DeleteOne[coreOrderItem](ctx, map[string]any{"ID": 1})
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, cfg.queries(), "ROLLBACK")
}

func TestCoreDeleteOneNilTable(t *testing.T) {
	var tm *psql.TableMeta[coreOrderItem]
	assert.NotPanics(t, func() {
		assert.ErrorIs(t, tm.DeleteOne(context.Background(), nil), psql.ErrNotReady)
	})
}

// ---------------------------------------------------------------------------
// 14. unsupported field types name the field
// ---------------------------------------------------------------------------

type coreBadField struct {
	psql.Name `sql:"core_bad_field"`
	ID        int64 `sql:",key=PRIMARY"`
	Weird     chan int
}

func TestCoreUnsupportedFieldPanicMessage(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "Table must panic on unsupported field types")
		msg := fmt.Sprint(r)
		assert.Contains(t, msg, "coreBadField.Weird")
		assert.Contains(t, msg, "chan int")
		assert.Contains(t, msg, "supported")
	}()
	psql.Table[coreBadField]()
}

// ---------------------------------------------------------------------------
// misc: options tolerate nil, FetchOne into reused target resets NULLs
// ---------------------------------------------------------------------------

func TestCoreFetchOneResetsNullColumns(t *testing.T) {
	cfg, _, ctx := newStubBackend(t)
	cfg.rows = softItemRows([][]driver.Value{{int64(1), "a", nil, nil}})

	score := int64(5)
	now := time.Now()
	target := &coreSoftItem{ID: 99, Label: "old", Score: &score, DeletedAt: &now}
	require.NoError(t, psql.FetchOne(ctx, target, nil, nil))
	assert.Equal(t, int64(1), target.ID)
	assert.Nil(t, target.Score)
	assert.Nil(t, target.DeletedAt)
	assert.False(t, psql.HasChanged(target))
}
