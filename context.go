package psql

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"time"
)

type ctxData int

const (
	ctxDataObj ctxData = iota
	ctxValueObjFetch
)

type ctxValueObj struct {
	context.Context
	obj any
}

func (c *ctxValueObj) Value(v any) any {
	if v == ctxDataObj {
		return c.obj
	}
	if v == ctxValueObjFetch {
		return c
	}
	return c.Context.Value(v)
}

// ContextBackend attaches a [Backend] to the context. All psql operations using
// the returned context will use this backend.
func ContextBackend(ctx context.Context, be *Backend) context.Context {
	return &ctxValueObj{ctx, be}
}

// ContextDB attaches a *sql.DB to the context, causing queries to use it directly.
func ContextDB(ctx context.Context, db *sql.DB) context.Context {
	return &ctxValueObj{ctx, db}
}

// ContextConn attaches a *sql.Conn to the context, pinning queries to a single connection.
func ContextConn(ctx context.Context, conn *sql.Conn) context.Context {
	return &ctxValueObj{ctx, conn}
}

// ContextTx attaches a [TxProxy] transaction to the context. All queries using the
// returned context will execute within this transaction.
func ContextTx(ctx context.Context, tx *TxProxy) context.Context {
	return &ctxValueObj{ctx, tx}
}

// DefaultTxRetries is the number of times [Tx] (and [TxWithOptions] with a
// zero MaxRetries) re-runs a transaction that failed with a retryable error
// (see [IsRetryable]), in addition to the first attempt.
var DefaultTxRetries = 3

// TxOptions configures [TxWithOptions].
type TxOptions struct {
	// Isolation and ReadOnly are passed to database/sql when the transaction
	// is started (they map to sql.TxOptions). They are ignored for nested
	// transactions, which run as savepoints of the enclosing one.
	Isolation sql.IsolationLevel
	ReadOnly  bool
	// MaxRetries is the number of times the callback is re-run after a
	// retryable failure. 0 means [DefaultTxRetries]; a negative value
	// disables retries.
	MaxRetries int
	// Backoff returns how long to wait before retry attempt (1 for the first
	// retry). nil uses an exponential backoff with jitter starting around
	// 10ms, capped at one second before a random jitter of up to 50% is applied.
	Backoff func(attempt int) time.Duration
}

// sqlOptions converts o to the database/sql options given to BeginTx.
func (o *TxOptions) sqlOptions() *sql.TxOptions {
	if o == nil || (o.Isolation == sql.LevelDefault && !o.ReadOnly) {
		return nil
	}
	return &sql.TxOptions{Isolation: o.Isolation, ReadOnly: o.ReadOnly}
}

// maxRetries returns the effective retry count.
func (o *TxOptions) maxRetries() int {
	if o == nil || o.MaxRetries == 0 {
		return DefaultTxRetries
	}
	if o.MaxRetries < 0 {
		return 0
	}
	return o.MaxRetries
}

// backoff returns the delay before the given retry attempt.
func (o *TxOptions) backoff(attempt int) time.Duration {
	if o != nil && o.Backoff != nil {
		return o.Backoff(attempt)
	}
	return defaultTxBackoff(attempt)
}

// defaultTxBackoff doubles a 10ms base for each attempt, with ±50% jitter,
// and never waits more than a second: roughly 10ms, 20ms, 40ms...
func defaultTxBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := 10 * time.Millisecond
	for i := 1; i < attempt && base < time.Second; i++ {
		base *= 2
	}
	if base > time.Second {
		base = time.Second
	}
	// jitter in [0.5, 1.5) × base
	jitter := time.Duration(rand.Int64N(int64(base))) - base/2
	return base + jitter
}

// Tx runs cb inside a SQL transaction. If cb returns nil the transaction is
// committed; otherwise it is rolled back and the error is returned. It is
// [TxWithOptions] with nil options: default isolation, and up to
// [DefaultTxRetries] retries on retryable failures, so cb must be safe to
// re-run (see TxWithOptions).
//
// The context passed to cb carries the transaction, so all psql operations
// using it execute within that transaction. To run a query outside the
// transaction (e.g. to persist a log entry before rolling back), use the
// original context captured before calling Tx, or call [EscapeTx] on the
// transactional context:
//
//	outerCtx := ctx
//	err := psql.Tx(ctx, func(ctx context.Context) error {
//	    // ctx is transactional; outerCtx is not
//	    if err := psql.Insert(ctx, &order); err != nil {
//	        psql.Insert(outerCtx, &AuditLog{Event: "order_failed"})
//	        return err // rolls back, but the audit log persists
//	    }
//	    return nil
//	})
func Tx(ctx context.Context, cb func(ctx context.Context) error) error {
	return TxWithOptions(ctx, nil, cb)
}

// TxWithOptions runs cb inside a SQL transaction started with the given
// options (nil for defaults), committing when cb returns nil and rolling back
// otherwise.
//
// When the transaction is top-level (ctx does not already carry one) and cb
// or the commit fails with an error the backend's dialect reports as
// retryable (serialization failure, deadlock; see [IsRetryable]), the
// transaction is rolled back and, after a backoff, cb IS RUN AGAIN with a
// fresh transaction, up to opts.MaxRetries times. The callback must
// therefore be idempotent with respect to anything outside the database:
// do not send emails, publish messages, mutate in-memory state or capture
// results in outer variables without resetting them at the start of the
// callback; everything it does inside the transaction is discarded by the
// rollback, everything else is repeated. When retries are exhausted the
// last error is returned wrapped with [ErrTxRetriesExhausted]. If ctx is
// cancelled while waiting for a retry, the wait stops and the cancellation
// error is returned.
//
// A nested call (ctx already carries a transaction) runs cb once as a
// savepoint and returns its error unchanged: retrying part of a transaction
// is meaningless, and the error propagates so that the outer, top-level
// transaction can retry as a whole.
func TxWithOptions(ctx context.Context, opts *TxOptions, cb func(ctx context.Context) error) error {
	if _, nested := EscapeTx(ctx); nested {
		return runTxOnce(ctx, opts.sqlOptions(), cb)
	}

	retries := opts.maxRetries()
	var rc RetryableChecker
	if retries > 0 {
		rc, _ = GetBackend(ctx).Engine().dialect().(RetryableChecker)
	}

	for attempt := 0; ; attempt++ {
		err := runTxOnce(ctx, opts.sqlOptions(), cb)
		if err == nil || rc == nil || !rc.IsRetryable(err) {
			return err
		}
		if attempt >= retries {
			return fmt.Errorf("%w after %d attempt(s): %w", ErrTxRetriesExhausted, attempt+1, err)
		}
		debugLog(ctx, "retrying transaction (attempt %d of %d) after: %v", attempt+2, retries+1, err)
		select {
		case <-ctx.Done():
			return fmt.Errorf("transaction retry aborted: %w (last error: %w)", ctx.Err(), err)
		case <-time.After(opts.backoff(attempt + 1)):
		}
	}
}

// runTxOnce performs a single attempt: begin, cb, commit or rollback.
func runTxOnce(ctx context.Context, opts *sql.TxOptions, cb func(ctx context.Context) error) error {
	tx, err := BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	ctx = ContextTx(ctx, tx)
	err = cb(ctx)
	if err == nil {
		return tx.Commit()
	}
	return err
}

// BeginTx starts a new transaction. If the context already contains a transaction,
// a nested transaction is created using a SQL savepoint. Use [ContextTx] to attach
// the returned [TxProxy] to a context for use with psql operations.
func BeginTx(ctx context.Context, opts *sql.TxOptions) (*TxProxy, error) {
	obj := ctx.Value(ctxDataObj)
	if obj == nil {
		tx, err := GetBackend(ctx).DB().BeginTx(ctx, opts)
		return newTxCtrl(ctx, tx, err)
	}

	switch o := obj.(type) {
	case *sql.Conn:
		tx, err := o.BeginTx(ctx, opts)
		return newTxCtrl(ctx, tx, err)
	case *sql.DB:
		tx, err := o.BeginTx(ctx, opts)
		return newTxCtrl(ctx, tx, err)
	case *Backend:
		tx, err := o.db.BeginTx(ctx, opts)
		return newTxCtrl(ctx, tx, err)
	case *TxProxy:
		return o.BeginTx(ctx, opts)
	case interface {
		BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	}:
		tx, err := o.BeginTx(ctx, opts)
		return newTxCtrl(ctx, tx, err)
	default:
		tx, err := GetBackend(ctx).DB().BeginTx(ctx, opts)
		return newTxCtrl(ctx, tx, err)
	}
}

// EscapeTx returns the context that was active before the innermost transaction
// was attached. This is useful when you need to run a query outside the current
// transaction — for example, to store an audit log or error record that must
// persist even if the transaction is rolled back.
//
// If no transaction is found in the context chain, EscapeTx returns (ctx, false).
// The returned context still carries the [Backend] and any other values that were
// set above the transaction layer.
//
//	func logFailure(ctx context.Context, msg string) {
//	    outerCtx, ok := psql.EscapeTx(ctx)
//	    if !ok {
//	        outerCtx = ctx // no transaction, use as-is
//	    }
//	    psql.Insert(outerCtx, &ErrorLog{Message: msg})
//	}
func EscapeTx(ctx context.Context) (context.Context, bool) {
	for {
		obj := ctx.Value(ctxValueObjFetch)
		if obj == nil {
			// no parent object, just return the same ctx
			return ctx, false
		}
		objV := obj.(*ctxValueObj)

		switch objV.obj.(type) {
		case *TxProxy, *sql.Tx:
			// we reached the point we wanted
			return objV.Context, true
		}

		// we need to go deeper
		ctx = objV.Context
	}
}

// GetBackend will attempt to find a backend in the provided context and return it, or it will
// return DefaultBackend if no backend was found.
func GetBackend(ctx context.Context) *Backend {
	for {
		if ctx == nil {
			return DefaultBackend
		}
		obj := ctx.Value(ctxValueObjFetch)
		if obj == nil {
			// no parent object
			return DefaultBackend
		}
		objV := obj.(*ctxValueObj)

		if be, ok := objV.obj.(*Backend); ok {
			return be
		}

		// we need to continue
		ctx = objV.Context
	}
}

// ExecContext executes a query (INSERT, UPDATE, DELETE, etc.) using whatever database
// object is attached to the context (transaction, connection, or backend).
func ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	obj := ctx.Value(ctxDataObj)
	if obj == nil {
		debugLog(ctx, "Exec on DB: %s %v", query, args)
		return GetBackend(ctx).DB().ExecContext(ctx, query, args...)
	}

	switch o := obj.(type) {
	case *sql.Tx:
		debugLog(ctx, "Exec on tx: %s %v", query, args)
		return o.ExecContext(ctx, query, args...)
	case *TxProxy:
		debugLog(ctx, "Exec on tx proxy: %s %v", query, args)
		return o.ExecContext(ctx, query, args...)
	case *sql.Conn:
		debugLog(ctx, "Exec on conn: %s %v", query, args)
		return o.ExecContext(ctx, query, args...)
	case *sql.DB:
		debugLog(ctx, "Exec on DB: %s %v", query, args)
		return o.ExecContext(ctx, query, args...)
	case *Backend:
		debugLog(ctx, "Exec on Backend: %s %v", query, args)
		return o.db.ExecContext(ctx, query, args...)
	case interface {
		ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	}:
		debugLog(ctx, "Exec on %T: %s %v", o, query, args)
		return o.ExecContext(ctx, query, args...)
	default:
		// unknown object, fallback to standard
		debugLog(ctx, "Exec on DB because %T is unknown: %s %v", o, query, args)
		return GetBackend(ctx).DB().ExecContext(ctx, query, args...)
	}
}

func doQueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	obj := ctx.Value(ctxDataObj)
	if obj == nil {
		debugLog(ctx, "Query on DB: %s %v", query, args)
		return GetBackend(ctx).DB().QueryContext(ctx, query, args...)
	}

	switch o := obj.(type) {
	case *sql.Tx:
		debugLog(ctx, "Query on tx: %s %v", query, args)
		return o.QueryContext(ctx, query, args...)
	case *TxProxy:
		debugLog(ctx, "Query on tx proxy: %s %v", query, args)
		return o.QueryContext(ctx, query, args...)
	case *sql.Conn:
		debugLog(ctx, "Query on conn: %s %v", query, args)
		return o.QueryContext(ctx, query, args...)
	case *sql.DB:
		debugLog(ctx, "Query on db: %s %v", query, args)
		return o.QueryContext(ctx, query, args...)
	case *Backend:
		debugLog(ctx, "Query on Backend: %s %v", query, args)
		return o.db.QueryContext(ctx, query, args...)
	case interface {
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	}:
		debugLog(ctx, "Query on %T: %s %v", o, query, args)
		return o.QueryContext(ctx, query, args...)
	default:
		debugLog(ctx, "Query db because %T is unknown: %s %v", o, query, args)
		// unknown object, fallback to standard
		return GetBackend(ctx).DB().QueryContext(ctx, query, args...)
	}
}

func doPrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	obj := ctx.Value(ctxDataObj)
	if obj == nil {
		debugLog(ctx, "Prepare on DB: %s", query)
		return GetBackend(ctx).DB().PrepareContext(ctx, query)
	}

	switch o := obj.(type) {
	case *sql.Tx:
		debugLog(ctx, "Prepare on tx: %s", query)
		return o.PrepareContext(ctx, query)
	case *TxProxy:
		debugLog(ctx, "Prepare on tx proxy: %s", query)
		return o.PrepareContext(ctx, query)
	case *sql.Conn:
		debugLog(ctx, "Prepare on conn: %s", query)
		return o.PrepareContext(ctx, query)
	case *sql.DB:
		debugLog(ctx, "Prepare on DB: %s", query)
		return o.PrepareContext(ctx, query)
	case *Backend:
		debugLog(ctx, "Prepare on Backend: %s", query)
		return o.db.PrepareContext(ctx, query)
	case interface {
		PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	}:
		debugLog(ctx, "Prepare on %T: %s", o, query)
		return o.PrepareContext(ctx, query)
	default:
		// unknown object, fallback to standard
		debugLog(ctx, "Prepare on DB because %T is unknown: %s", o, query)
		return GetBackend(ctx).DB().PrepareContext(ctx, query)
	}
}
