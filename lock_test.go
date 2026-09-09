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
	"testing"
	"time"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Named locks need a dialect implementing LockRenderer and a database to run
// the statements on. Neither exists in unit tests, so a private engine value
// gets a stub dialect and a stub database/sql driver recording the statements
// it receives (the registered dialects for the real engines are untouched).

const (
	lockStubEngine     = psql.Engine(98)
	lockStubDriverName = "psql-lock-stub"
)

var errLockStubTimeout = errors.New("stub: lock_timeout")

type lockStubDialect struct{}

func (lockStubDialect) Placeholder(_ int) string    { return "?" }
func (lockStubDialect) ExportArg(v any) any         { return psql.DefaultExportArg(v) }
func (lockStubDialect) LimitOffset(a, b int) string { return fmt.Sprintf("LIMIT %d OFFSET %d", b, a) }
func (lockStubDialect) SupportsFeature(_ psql.Variant, feature string) bool {
	return feature == psql.FeatureAdvisoryLocks
}
func (lockStubDialect) AcquireLockSQL(name string, timeout time.Duration) (string, []any, error) {
	if name == "bad" {
		return "", nil, errors.New("stub: bad lock name")
	}
	return "SELECT GET_LOCK(?, ?)", []any{name, int64(timeout / time.Second)}, nil
}
func (lockStubDialect) ReleaseLockSQL(name string) (string, []any, error) {
	if name == "auto" {
		return "", nil, nil // released at transaction end
	}
	return "SELECT RELEASE_LOCK(?)", []any{name}, nil
}
func (lockStubDialect) IsLockTimeout(err error) bool { return errors.Is(err, errLockStubTimeout) }

type lockStubConfig struct {
	mu    sync.Mutex
	stmts []string
	opens int
	// acquire returns the rows of an acquire statement: a nil slice means
	// "no result set"; err is returned as the query error
	acquire func(args []driver.Value) (rows [][]driver.Value, err error)
}

func (c *lockStubConfig) record(q string, args []driver.Value) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(args) > 0 {
		q = fmt.Sprintf("%s %v", q, args)
	}
	c.stmts = append(c.stmts, q)
}

func (c *lockStubConfig) statements() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.stmts...)
}

var lockStubRegistry = struct {
	sync.Mutex
	m map[string]*lockStubConfig
}{m: map[string]*lockStubConfig{}}

type lockStubDriver struct{}

func (lockStubDriver) Open(name string) (driver.Conn, error) {
	lockStubRegistry.Lock()
	cfg := lockStubRegistry.m[name]
	lockStubRegistry.Unlock()
	if cfg == nil {
		return nil, fmt.Errorf("unknown lock stub database %q", name)
	}
	cfg.mu.Lock()
	cfg.opens++
	cfg.mu.Unlock()
	return &lockStubConn{cfg: cfg}, nil
}

type lockStubConn struct{ cfg *lockStubConfig }

func (c *lockStubConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("lock stub: Prepare not supported")
}
func (c *lockStubConn) Close() error { return nil }
func (c *lockStubConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *lockStubConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.cfg.record("BEGIN", nil)
	return lockStubTx{c: c}, nil
}

func lockStubValues(args []driver.NamedValue) []driver.Value {
	res := make([]driver.Value, len(args))
	for i, a := range args {
		res[i] = a.Value
	}
	return res
}

func (c *lockStubConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.cfg.record(q, lockStubValues(args))
	return driver.RowsAffected(1), nil
}

func (c *lockStubConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	vals := lockStubValues(args)
	c.cfg.record(q, vals)
	if strings.HasPrefix(q, "SELECT GET_LOCK") && c.cfg.acquire != nil {
		rows, err := c.cfg.acquire(vals)
		if err != nil {
			return nil, err
		}
		if rows == nil {
			return &lockStubRows{}, nil
		}
		return &lockStubRows{cols: []string{"result"}, data: rows}, nil
	}
	return &lockStubRows{cols: []string{"result"}, data: [][]driver.Value{{int64(1)}}}, nil
}

type lockStubTx struct{ c *lockStubConn }

func (t lockStubTx) Commit() error   { t.c.cfg.record("COMMIT", nil); return nil }
func (t lockStubTx) Rollback() error { t.c.cfg.record("ROLLBACK", nil); return nil }

type lockStubRows struct {
	cols []string
	data [][]driver.Value
	i    int
}

func (r *lockStubRows) Columns() []string { return r.cols }
func (r *lockStubRows) Close() error      { return nil }
func (r *lockStubRows) Next(dest []driver.Value) error {
	if r.i >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.i])
	r.i++
	return nil
}

func init() {
	sql.Register(lockStubDriverName, lockStubDriver{})
	psql.RegisterDialect(lockStubEngine, lockStubDialect{})
}

// newLockStubBackend returns a backend on a fresh stub database.
func newLockStubBackend(t *testing.T) (*lockStubConfig, context.Context) {
	t.Helper()
	cfg := &lockStubConfig{}
	lockStubRegistry.Lock()
	lockStubRegistry.m[t.Name()] = cfg
	lockStubRegistry.Unlock()

	db, err := sql.Open(lockStubDriverName, t.Name())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	be := psql.NewBackend(lockStubEngine, db)
	if !be.Supports(psql.FeatureAdvisoryLocks) {
		t.Fatalf("the lock stub dialect registered for psql.Engine(%d) was replaced by another test registration", lockStubEngine)
	}
	return cfg, be.Plug(context.Background())
}

func TestNamedLockNotSupported(t *testing.T) {
	// no LockRenderer on the registered test dialects, and a fake MySQL backend
	// has no dialect at all
	for _, e := range []psql.Engine{psql.EngineSQLite, psql.EnginePostgreSQL, psql.EngineMySQL} {
		ctx := ctxForEngine(e)
		_, err := psql.NamedLock(ctx, "x", 0)
		require.Error(t, err, e)
		assert.True(t, errors.Is(err, psql.ErrNotSupported), "%s: %v", e, err)

		called := false
		err = psql.WithNamedLock(ctx, "x", 0, func(context.Context) error { called = true; return nil })
		assert.True(t, errors.Is(err, psql.ErrNotSupported), "%s: %v", e, err)
		assert.False(t, called, e)
	}

	// the dialect has the renderer but the product does not support locks
	crdb := psql.NewBackend(psql.EnginePostgreSQL, nil, psql.WithVariant(psql.VariantCockroachDB)).Plug(context.Background())
	_, err := psql.NamedLock(crdb, "x", 0)
	assert.True(t, errors.Is(err, psql.ErrNotSupported), err)
}

func TestNamedLockPinsConnection(t *testing.T) {
	cfg, ctx := newLockStubBackend(t)

	release, err := psql.NamedLock(ctx, "job", 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, []string{"BEGIN", "SELECT GET_LOCK(?, ?) [job 5]"}, cfg.statements())

	// the lock holds a dedicated connection: other work goes elsewhere
	require.NoError(t, psql.Q("SELECT 1").Exec(ctx))
	require.NoError(t, release())
	assert.Equal(t, []string{
		"BEGIN", "SELECT GET_LOCK(?, ?) [job 5]",
		"SELECT 1",
		"SELECT RELEASE_LOCK(?) [job]", "COMMIT",
	}, cfg.statements())
	assert.Equal(t, 2, cfg.opens, "the lock holds its own connection")
}

func TestNamedLockInTransaction(t *testing.T) {
	cfg, ctx := newLockStubBackend(t)

	err := psql.Tx(ctx, func(ctx context.Context) error {
		release, err := psql.NamedLock(ctx, "auto", 0)
		if err != nil {
			return err
		}
		if err := psql.Q("UPDATE t SET a=1").Exec(ctx); err != nil {
			return err
		}
		return release()
	})
	require.NoError(t, err)
	// same connection, no release statement for transaction-scoped locks
	assert.Equal(t, []string{"BEGIN", "SELECT GET_LOCK(?, ?) [auto 0]", "UPDATE t SET a=1", "COMMIT"}, cfg.statements())
	assert.Equal(t, 1, cfg.opens)
}

func TestNamedLockResults(t *testing.T) {
	cases := []struct {
		name    string
		rows    [][]driver.Value
		err     error
		timeout bool
		ok      bool
	}{
		{name: "int 1", rows: [][]driver.Value{{int64(1)}}, ok: true},
		{name: "bool true", rows: [][]driver.Value{{true}}, ok: true},
		{name: "string t", rows: [][]driver.Value{{"t"}}, ok: true},
		{name: "bytes 1", rows: [][]driver.Value{{[]byte("1")}}, ok: true},
		{name: "no result set", rows: nil, ok: true},
		{name: "empty result set", rows: [][]driver.Value{}, ok: true},
		{name: "int 0", rows: [][]driver.Value{{int64(0)}}, timeout: true},
		{name: "bool false", rows: [][]driver.Value{{false}}, timeout: true},
		{name: "string false", rows: [][]driver.Value{{"false"}}, timeout: true},
		{name: "null", rows: [][]driver.Value{{nil}}},
		{name: "garbage", rows: [][]driver.Value{{"maybe"}}},
		{name: "query error", err: errors.New("boom")},
		{name: "timeout error", err: errLockStubTimeout, timeout: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, ctx := newLockStubBackend(t)
			cfg.acquire = func([]driver.Value) ([][]driver.Value, error) { return c.rows, c.err }

			release, err := psql.NamedLock(ctx, "job", -1)
			if c.ok {
				require.NoError(t, err)
				require.NoError(t, release())
				return
			}
			require.Error(t, err)
			assert.Nil(t, release)
			assert.Equal(t, c.timeout, errors.Is(err, psql.ErrLockTimeout), err)
			if c.err != nil && !c.timeout {
				var qerr *psql.Error
				assert.True(t, errors.As(err, &qerr), err)
				assert.True(t, errors.Is(err, c.err), err)
			}
			// the pinned connection is rolled back and returned to the pool
			stmts := cfg.statements()
			require.NotEmpty(t, stmts)
			assert.Equal(t, "ROLLBACK", stmts[len(stmts)-1], stmts)
		})
	}
}

func TestNamedLockDialectError(t *testing.T) {
	_, ctx := newLockStubBackend(t)
	_, err := psql.NamedLock(ctx, "bad", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad lock name")
}

func TestWithNamedLock(t *testing.T) {
	cfg, ctx := newLockStubBackend(t)

	var seen bool
	err := psql.WithNamedLock(ctx, "job", time.Second, func(ctx context.Context) error {
		// fn runs in the transaction holding the lock
		_, ok := psql.EscapeTx(ctx)
		seen = ok
		return psql.Q("UPDATE t SET a=1").Exec(ctx)
	})
	require.NoError(t, err)
	assert.True(t, seen)
	assert.Equal(t, []string{
		"BEGIN", "SELECT GET_LOCK(?, ?) [job 1]", "UPDATE t SET a=1", "SELECT RELEASE_LOCK(?) [job]", "COMMIT",
	}, cfg.statements())
	assert.Equal(t, 1, cfg.opens)
}

func TestWithNamedLockRollsBack(t *testing.T) {
	cfg, ctx := newLockStubBackend(t)
	boom := errors.New("boom")

	err := psql.WithNamedLock(ctx, "job", 0, func(ctx context.Context) error {
		return boom
	})
	assert.True(t, errors.Is(err, boom), err)
	assert.Equal(t, []string{
		"BEGIN", "SELECT GET_LOCK(?, ?) [job 0]", "SELECT RELEASE_LOCK(?) [job]", "ROLLBACK",
	}, cfg.statements())

	// timeout: fn never runs, the transaction is rolled back
	cfg.stmts = nil
	cfg.acquire = func([]driver.Value) ([][]driver.Value, error) { return [][]driver.Value{{int64(0)}}, nil }
	called := false
	err = psql.WithNamedLock(ctx, "job", time.Second, func(context.Context) error { called = true; return nil })
	assert.True(t, errors.Is(err, psql.ErrLockTimeout), err)
	assert.False(t, called)
	assert.Equal(t, []string{"BEGIN", "SELECT GET_LOCK(?, ?) [job 1]", "ROLLBACK"}, cfg.statements())
}

func TestWithNamedLockInExistingTransaction(t *testing.T) {
	cfg, ctx := newLockStubBackend(t)

	err := psql.Tx(ctx, func(ctx context.Context) error {
		return psql.WithNamedLock(ctx, "job", 0, func(ctx context.Context) error {
			return psql.Q("UPDATE t SET a=1").Exec(ctx)
		})
	})
	require.NoError(t, err)
	// no nested transaction (savepoint): the lock joins the caller's transaction
	assert.Equal(t, []string{
		"BEGIN", "SELECT GET_LOCK(?, ?) [job 0]", "UPDATE t SET a=1", "SELECT RELEASE_LOCK(?) [job]", "COMMIT",
	}, cfg.statements())

	// an error from fn is returned and the lock still released
	cfg.stmts = nil
	boom := errors.New("boom")
	err = psql.Tx(ctx, func(ctx context.Context) error {
		return psql.WithNamedLock(ctx, "job", 0, func(context.Context) error { return boom })
	})
	assert.True(t, errors.Is(err, boom), err)
	assert.Equal(t, []string{"BEGIN", "SELECT GET_LOCK(?, ?) [job 0]", "SELECT RELEASE_LOCK(?) [job]", "ROLLBACK"}, cfg.statements())
}
