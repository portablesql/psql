package psql

import (
	"context"
	"database/sql"
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

// Tx runs cb inside a SQL transaction. If cb returns nil the transaction is
// committed; otherwise it is rolled back and the error is returned.
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
	tx, err := BeginTx(ctx, nil)
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
