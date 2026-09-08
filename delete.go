package psql

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Delete will delete values from the table matching the where parameters.
// If the table has a soft delete field (DeletedAt), this performs an UPDATE
// setting the timestamp instead of a hard DELETE. Use [ForceDelete] to bypass
// soft delete.
func Delete[T any](ctx context.Context, where any, opts ...*FetchOptions) (sql.Result, error) {
	return Table[T]().Delete(ctx, where, opts...)
}

// Delete removes (or soft deletes) the records matching where. Pass nil to
// affect every record. [Limit] options are honored; query failures are
// returned as an [*Error]. See [Delete].
func (t *TableMeta[T]) Delete(ctx context.Context, where any, opts ...*FetchOptions) (sql.Result, error) {
	if t == nil {
		return nil, ErrNotReady
	}
	t.check(ctx)
	opt := resolveFetchOpts(opts)

	bt := t.bind(GetBackend(ctx))

	var req *QueryBuilder
	event := "psql:delete:run_fail"
	if bt.softDelete != nil && !opt.HardDelete {
		// Soft delete: UPDATE SET DeletedAt = NOW()
		event = "psql:soft_delete:run_fail"
		req = B().Update(bt.name).
			Set(map[string]any{bt.softDelete.Column: time.Now()})
		if where != nil {
			req = req.Where(where)
		}
		// Only soft-delete records that aren't already deleted
		req = req.Where(map[string]any{bt.softDelete.Column: nil})
	} else {
		// Hard delete
		req = B().Delete().From(bt.name)
		if where != nil {
			req = req.Where(where)
		}
	}

	if opt.LimitCount > 0 {
		if opt.LimitStart > 0 {
			req = req.Limit(opt.LimitStart, opt.LimitCount)
		} else {
			req = req.Limit(opt.LimitCount)
		}
	}

	res, err := execBuilder(ctx, req)
	if err != nil {
		logQueryError(ctx, event, t.table, "", err)
		return nil, err
	}
	return res, nil
}

// DeleteOne will operate the deletion in a separate transaction and ensure only 1 row was deleted or it will
// rollback the deletion and return an error. This is useful when working with important data and security is
// more important than performance.
func DeleteOne[T any](ctx context.Context, where any, opts ...*FetchOptions) error {
	return Table[T]().DeleteOne(ctx, where, opts...)
}

// DeleteOne deletes exactly one record matching where inside its own
// transaction, rolling back and returning [ErrDeleteBadAssert] if the number
// of affected rows is not 1. See [DeleteOne].
func (t *TableMeta[T]) DeleteOne(ctx context.Context, where any, opts ...*FetchOptions) error {
	if t == nil {
		return ErrNotReady
	}
	t.check(ctx)
	tx, err := BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := t.Delete(ContextTx(ctx, tx), where, opts...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: %d rows where exactly 1 expected", ErrDeleteBadAssert, n)
	}

	return tx.Commit()
}
