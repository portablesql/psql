package psql

import (
	"context"
	"os"
)

// Count returns the number of records matching the where clause. Pass nil to count all records.
// Optional [FetchOptions] can be passed to include soft-deleted records or apply scopes.
func Count[T any](ctx context.Context, where any, opts ...*FetchOptions) (int, error) {
	return Table[T]().Count(ctx, where, opts...)
}

// Count returns the number of records matching where (nil for all records).
// See [Count].
func (t *TableMeta[T]) Count(ctx context.Context, where any, opts ...*FetchOptions) (int, error) {
	if t == nil {
		return 0, ErrNotReady
	}
	t.check(ctx)
	opt := resolveFetchOpts(opts)

	bt := t.bind(GetBackend(ctx))
	req := B().Select(Raw("COUNT(1)")).From(bt.name)
	if where != nil {
		req = req.Where(where)
	}
	t.applySoftDelete(bt, req, opt)
	req = req.Apply(opt.Scopes...)

	// run query
	rows, err := req.RunQuery(ctx)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, err
		}
		// should not happen with COUNT(1)
		return 0, os.ErrNotExist
	}

	var res int
	err = rows.Scan(&res)

	return res, err
}
