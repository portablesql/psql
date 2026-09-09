package psql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrLockTimeout is returned (wrapped) by [NamedLock] and [WithNamedLock]
// when the named lock could not be acquired within the timeout.
var ErrLockTimeout = errors.New("psql: lock acquisition timed out")

// LockTimeoutChecker is an optional interface for dialects whose lock
// acquisition statement reports a timeout as an error rather than as a false
// result (PostgreSQL's lock_timeout raises SQLSTATE 55P03). [NamedLock] maps
// such errors to [ErrLockTimeout].
type LockTimeoutChecker interface {
	IsLockTimeout(err error) bool
}

// NamedLock acquires the named lock and returns a function releasing it.
// Named locks are cooperative: they only exclude other NamedLock calls on the
// same name (on the same server) and are meant to serialize application-level
// operations such as migrations or scheduled jobs.
//
// timeout bounds the wait for a lock held by someone else: a zero timeout
// waits indefinitely, a negative timeout fails immediately when the lock is
// taken. An expired timeout returns an error wrapping [ErrLockTimeout].
// Engines without named locks (SQLite, CockroachDB) return [ErrNotSupported];
// check [Backend.Supports] with [FeatureAdvisoryLocks] up front.
//
// Locks belong to a database connection, so the lock is taken on the
// connection the release runs on. When ctx carries a transaction (see [Tx],
// [ContextTx]) the lock is taken inside it; on PostgreSQL such a lock is
// released when the transaction ends, whatever the release function does.
// Otherwise NamedLock pins a dedicated connection (with an open transaction
// so that transaction-scoped locks are held) until release is called. Release
// must be called exactly once, typically with defer; it returns the error of
// the release statement, if any.
//
// Per engine: MySQL/MariaDB use GET_LOCK/RELEASE_LOCK (session scope, the
// lock survives transactions); PostgreSQL uses pg_advisory_xact_lock on a
// hash of the name (transaction scope; pg_try_advisory_xact_lock for a
// negative timeout, lock_timeout for a positive one).
func NamedLock(ctx context.Context, name string, timeout time.Duration) (release func() error, err error) {
	be := GetBackend(ctx)
	lr, err := lockRenderer(be)
	if err != nil {
		return nil, err
	}

	// already on a connection (transaction or pinned connection): lock there
	switch ctx.Value(ctxDataObj).(type) {
	case *TxProxy, *sql.Tx, *sql.Conn:
		if err := acquireLock(ctx, lr, name, timeout); err != nil {
			return nil, err
		}
		return func() error { return releaseLock(ctx, lr, name) }, nil
	}

	db := be.DB()
	if d, ok := ctx.Value(ctxDataObj).(*sql.DB); ok {
		db = d
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	lctx := ContextTx(ctx, mustTxProxy(ctx, tx))
	if err := acquireLock(lctx, lr, name, timeout); err != nil {
		tx.Rollback()
		conn.Close()
		return nil, err
	}
	release = func() error {
		err := releaseLock(lctx, lr, name)
		if cerr := tx.Commit(); err == nil {
			err = cerr
		}
		if cerr := conn.Close(); err == nil {
			err = cerr
		}
		return err
	}
	return release, nil
}

// WithNamedLock runs fn while holding the named lock (see [NamedLock] for the
// lock semantics and timeout). When ctx already carries a transaction the
// lock is taken inside it and fn runs with the same context; otherwise
// WithNamedLock begins a transaction, passes the transactional context to fn
// and commits it when fn returns nil (or rolls it back when fn returns an
// error, which is returned). The lock is released in both cases before
// WithNamedLock returns.
func WithNamedLock(ctx context.Context, name string, timeout time.Duration, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(ctxDataObj).(*TxProxy); ok {
		release, err := NamedLock(ctx, name, timeout)
		if err != nil {
			return err
		}
		err = fn(ctx)
		if rerr := release(); err == nil {
			err = rerr
		}
		return err
	}

	// checked before opening a transaction so that unsupported engines fail
	// cheaply
	if _, err := lockRenderer(GetBackend(ctx)); err != nil {
		return err
	}

	tx, err := BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tctx := ContextTx(ctx, tx)
	release, err := NamedLock(tctx, name, timeout)
	if err != nil {
		return err
	}
	if err := fn(tctx); err != nil {
		release()
		return err
	}
	if err := release(); err != nil {
		return err
	}
	return tx.Commit()
}

// lockRenderer returns the backend's LockRenderer, or ErrNotSupported.
func lockRenderer(be *Backend) (LockRenderer, error) {
	lr, ok := be.Engine().dialect().(LockRenderer)
	if !ok || !be.Supports(FeatureAdvisoryLocks) {
		return nil, fmt.Errorf("%w: named locks on %s", ErrNotSupported, be.Variant())
	}
	return lr, nil
}

// mustTxProxy wraps a *sql.Tx started on a pinned connection.
func mustTxProxy(ctx context.Context, tx *sql.Tx) *TxProxy {
	proxy, _ := newTxCtrl(ctx, tx, nil)
	return proxy
}

// acquireLock runs the acquisition statement on the connection carried by ctx
// and interprets its result.
func acquireLock(ctx context.Context, lr LockRenderer, name string, timeout time.Duration) error {
	query, args, err := lr.AcquireLockSQL(name, timeout)
	if err != nil {
		return err
	}
	if query == "" {
		return fmt.Errorf("%w: named locks", ErrNotSupported)
	}
	rows, err := doQueryContext(ctx, query, args...)
	if err != nil {
		if tc, ok := lr.(LockTimeoutChecker); ok && tc.IsLockTimeout(err) {
			return fmt.Errorf("%w: lock %q not acquired within %s", ErrLockTimeout, name, timeout)
		}
		return &Error{Query: query, Err: err}
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return &Error{Query: query, Err: err}
		}
		return nil // no result set: acquired
	}
	var res any
	if err := rows.Scan(&res); err != nil {
		return &Error{Query: query, Err: err}
	}
	ok, known := lockResult(res)
	switch {
	case ok:
		return nil
	case known:
		return fmt.Errorf("%w: lock %q not acquired within %s", ErrLockTimeout, name, timeout)
	default:
		return &Error{Query: query, Err: fmt.Errorf("lock %q: acquisition failed (result %v)", name, res)}
	}
}

// releaseLock runs the release statement, if the dialect has one.
func releaseLock(ctx context.Context, lr LockRenderer, name string) error {
	query, args, err := lr.ReleaseLockSQL(name)
	if err != nil {
		return err
	}
	if query == "" {
		return nil
	}
	if _, err := ExecContext(ctx, query, args...); err != nil {
		return &Error{Query: query, Err: err}
	}
	return nil
}

// lockResult interprets the value returned by a lock acquisition statement:
// ok reports success; known is false when the value is neither a success nor
// a failure indicator (NULL, which MySQL returns on error).
func lockResult(v any) (ok, known bool) {
	switch r := v.(type) {
	case nil:
		return false, false
	case bool:
		return r, true
	case int64:
		return r == 1, r == 0 || r == 1
	case int:
		return r == 1, r == 0 || r == 1
	case uint64:
		return r == 1, r == 0 || r == 1
	case float64:
		return r == 1, r == 0 || r == 1
	case []byte:
		return lockResult(string(r))
	case string:
		switch strings.ToLower(strings.TrimSpace(r)) {
		case "1", "t", "true":
			return true, true
		case "0", "f", "false":
			return false, true
		}
		return false, false
	default:
		return false, false
	}
}
