package psql

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
)

// SQLQuery represents a raw SQL query with arguments. Create one with [Q].
// Use [SQLQuery.Each] to iterate rows or [SQLQuery.Exec] to execute without results.
type SQLQuery struct {
	Query string
	Args  []any
}

// SQLQueryT is a typed raw SQL query that automatically scans results into type T.
// Create one with [QT]. Use [SQLQueryT.Each], [SQLQueryT.Single], or [SQLQueryT.All]
// to execute and get typed results.
type SQLQueryT[T any] struct {
	Query string
	Args  []any
}

// Q creates a raw [SQLQuery] for executing arbitrary SQL:
func Q(q string, args ...any) *SQLQuery {
	return &SQLQuery{q, args}
}

// QT creates a typed [SQLQueryT] that scans results into type T:
func QT[T any](q string, args ...any) *SQLQueryT[T] {
	return &SQLQueryT[T]{q, args}
}

// Exec simply runs a query against the DefaultBackend
//
// Deprecated: use [SQLQuery.Exec] instead
func Exec(q *SQLQuery) error {
	return q.Exec(context.Background())
}

// Query performs a query and use a callback to advance results, meaning there is no need to
// call sql.Rows.Close()
//
// err = psql.Query(psql.Q("SELECT ..."), func(row *sql.Rows) error { ... })
//
// Deprecated: use .Each() instead
func Query(q *SQLQuery, cb func(*sql.Rows) error) error {
	return QueryContext(context.Background(), q, cb)
}

// QueryContext performs a query and use a callback to advance results, meaning there is no need to
// call sql.Rows.Close()
//
// Deprecated: use .Each() instead
func QueryContext(ctx context.Context, q *SQLQuery, cb func(*sql.Rows) error) error {
	return q.Each(ctx, cb)
}

// Each will execute the query and call cb for each row, so you do not need to call
// .Next() or .Close() on the object. Returning [ErrBreakLoop] from cb stops the
// iteration without error.
//
// Example use: err := psql.Q("SELECT ...").Each(ctx, func(row *sql.Rows) error { ... })
func (q *SQLQuery) Each(ctx context.Context, cb func(*sql.Rows) error) error {
	r, err := doQueryContext(ctx, q.Query, q.Args...)
	if err != nil {
		return err
	}
	defer r.Close()

	for r.Next() {
		err := cb(r)
		if err != nil {
			if errors.Is(err, ErrBreakLoop) {
				return nil
			}
			return err
		}
	}
	return r.Err()
}

// Exec executes the query using whatever database object is attached to ctx
// (transaction, connection or backend, see [ExecContext]) and returns any
// error that could have happened.
func (q *SQLQuery) Exec(ctx context.Context) error {
	_, err := ExecContext(ctx, q.Query, q.Args...)
	return err
}

// Each will execute the query and call cb for each row. Returning
// [ErrBreakLoop] from cb stops the iteration without error.
func (q *SQLQueryT[T]) Each(ctx context.Context, cb func(*T) error) error {
	t := Table[T]()
	t.check(ctx)

	r, err := doQueryContext(ctx, q.Query, q.Args...)
	if err != nil {
		return err
	}
	defer r.Close()

	plan, err := t.newScanPlan(ctx, r)
	if err != nil {
		return err
	}

	for r.Next() {
		obj := t.newobj()
		if err := plan.scan(ctx, r, obj, true); err != nil {
			return err
		}
		err = cb(obj)
		if err != nil {
			if errors.Is(err, ErrBreakLoop) {
				return nil
			}
			return err
		}
	}
	return r.Err()
}

// Single will execute the query and fetch a single result, or return
// [fs.ErrNotExist] if the query produced no row.
func (q *SQLQueryT[T]) Single(ctx context.Context) (*T, error) {
	t := Table[T]()
	t.check(ctx)

	r, err := doQueryContext(ctx, q.Query, q.Args...)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if !r.Next() {
		if err := r.Err(); err != nil {
			return nil, err
		}
		return nil, fs.ErrNotExist
	}
	return t.spawn(ctx, r)
}

// All will execute the query and return all the results
func (q *SQLQueryT[T]) All(ctx context.Context) ([]*T, error) {
	t := Table[T]()
	t.check(ctx)

	r, err := doQueryContext(ctx, q.Query, q.Args...)
	if err != nil {
		return nil, err
	}

	return t.spawnAll(ctx, r)
}

// execBuilder renders and executes a builder query through [ExecContext],
// wrapping execution failures in an [*Error] carrying the rendered query.
func execBuilder(ctx context.Context, req *QueryBuilder) (sql.Result, error) {
	query, args, err := req.RenderArgs(ctx)
	if err != nil {
		return nil, err
	}
	res, err := ExecContext(ctx, query, args...)
	if err != nil {
		return nil, &Error{Query: query, Err: err}
	}
	return res, nil
}
