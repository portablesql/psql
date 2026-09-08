package psql

import (
	"context"
	"database/sql"
	"reflect"
	"time"
)

var ptrTimeType = reflect.TypeFor[*time.Time]()

// applySoftDelete adds a WHERE condition to exclude soft-deleted records if the
// table has a soft delete field and WithDeleted is not set.
func (t *TableMeta[T]) applySoftDelete(bt *boundTable, req *QueryBuilder, opt *FetchOptions) {
	if bt.softDelete == nil || (opt != nil && opt.WithDeleted) {
		return
	}
	req.Where(map[string]any{bt.softDelete.Column: nil})
}

// ForceDelete performs a hard DELETE regardless of whether the table uses soft delete.
func ForceDelete[T any](ctx context.Context, where any, opts ...*FetchOptions) (sql.Result, error) {
	opts = append(opts, &FetchOptions{HardDelete: true})
	return Table[T]().Delete(ctx, where, opts...)
}

// Restore clears the soft delete timestamp on records matching the where clause,
// effectively un-deleting them. Pass nil for where to restore every record.
// Returns [ErrNotReady] if the table has no soft delete field.
func Restore[T any](ctx context.Context, where any) (sql.Result, error) {
	return Table[T]().Restore(ctx, where)
}

// Restore clears the soft delete timestamp of the records matching where (nil
// for all records). Query failures are returned as an [*Error]. See [Restore].
func (t *TableMeta[T]) Restore(ctx context.Context, where any) (sql.Result, error) {
	if t == nil {
		return nil, ErrNotReady
	}
	if t.softDelete == nil {
		return nil, ErrNotReady
	}
	t.check(ctx)

	bt := t.bind(GetBackend(ctx))
	req := B().Update(bt.name).
		Set(map[string]any{bt.softDelete.Column: Raw("NULL")})
	if where != nil {
		req = req.Where(where)
	}
	res, err := execBuilder(ctx, req)
	if err != nil {
		logQueryError(ctx, "psql:restore:run_fail", t.table, "", err)
		return nil, err
	}
	return res, nil
}
