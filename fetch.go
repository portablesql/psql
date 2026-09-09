package psql

import (
	"context"
	"fmt"
	"iter"
	"os"
)

// FetchOptions controls the behavior of Fetch, Get, FetchOne, and related operations.
// Use helper constructors [Sort], [Limit], [LimitFrom], [WithPreload], [WithScope],
// [IncludeDeleted], and [FetchLock] to create options, or combine multiple options by
// passing them as variadic arguments.
type FetchOptions struct {
	Lock           bool            // SELECT ... FOR UPDATE (same as LockMode == LockUpdate)
	LockMode       LockMode        // row lock mode (FOR UPDATE, FOR SHARE, ...), see [QueryBuilder.SetLockMode]
	LockOf         []string        // restrict the lock to these tables (FOR UPDATE OF "t"), see [QueryBuilder.LockOf]
	SkipLocked     bool            // append SKIP LOCKED after the lock clause
	NoWait         bool            // append NOWAIT after the lock clause
	AsOfSystemTime string          // CockroachDB AS OF SYSTEM TIME expression, see [QueryBuilder.AsOfSystemTime]
	LimitCount     int             // number of results to return if >0
	LimitStart     int             // seek first record if >0
	Sort           []SortValueable // fields to sort by
	Preload        []string        // association fields to preload after fetching
	Scopes         []Scope         // reusable query modifiers
	WithDeleted    bool            // include soft-deleted records
	HardDelete     bool            // force hard delete even with soft delete
}

// Sort returns a [FetchOptions] that orders results by the given fields.
// Use [S] to create sort fields: psql.Sort(psql.S("Name", "ASC"))
func Sort(fields ...SortValueable) *FetchOptions {
	return &FetchOptions{Sort: fields}
}

// Limit returns a [FetchOptions] that limits the number of results returned.
func Limit(cnt int) *FetchOptions {
	return &FetchOptions{LimitCount: cnt}
}

// LimitFrom returns a [FetchOptions] with both an offset (start) and a limit (cnt).
// Equivalent to LIMIT cnt OFFSET start in PostgreSQL/SQLite, or LIMIT start, cnt in MySQL.
func LimitFrom(start, cnt int) *FetchOptions {
	return &FetchOptions{
		LimitCount: cnt,
		LimitStart: start,
	}
}

// FetchLock is a [FetchOptions] that adds SELECT ... FOR UPDATE to lock the selected rows.
var FetchLock = &FetchOptions{Lock: true}

// FetchLockSkipLocked is a [FetchOptions] that adds FOR UPDATE SKIP LOCKED.
var FetchLockSkipLocked = &FetchOptions{Lock: true, SkipLocked: true}

// FetchLockNoWait is a [FetchOptions] that adds FOR UPDATE NOWAIT.
var FetchLockNoWait = &FetchOptions{Lock: true, NoWait: true}

// FetchLockShare is a [FetchOptions] that takes a shared row lock: FOR SHARE
// on PostgreSQL, CockroachDB and MySQL 8, LOCK IN SHARE MODE on MariaDB and
// MySQL 5.7, omitted on SQLite.
var FetchLockShare = &FetchOptions{LockMode: LockShare}

// FetchLockNoKeyUpdate is a [FetchOptions] that adds FOR NO KEY UPDATE
// (PostgreSQL, CockroachDB; FOR UPDATE on MySQL and MariaDB).
var FetchLockNoKeyUpdate = &FetchOptions{LockMode: LockNoKeyUpdate}

// FetchLockKeyShare is a [FetchOptions] that adds FOR KEY SHARE (PostgreSQL,
// CockroachDB; the share lock on MySQL and MariaDB).
var FetchLockKeyShare = &FetchOptions{LockMode: LockKeyShare}

// WithLock returns a [FetchOptions] taking the given row lock, optionally
// restricted to the listed tables (FOR UPDATE OF "t"). See
// [QueryBuilder.SetLockMode] for the rendering on each engine. Combine it
// with [FetchLockSkipLocked] or [FetchLockNoWait] for the SKIP LOCKED / NOWAIT
// modifiers:
//
//	jobs, err := psql.Fetch[Job](ctx, where, psql.WithLock(psql.LockShare), psql.FetchLockSkipLocked)
func WithLock(mode LockMode, of ...string) *FetchOptions {
	return &FetchOptions{LockMode: mode, LockOf: of}
}

// AsOfSystemTime returns a [FetchOptions] that reads from a historical
// snapshot on CockroachDB (see [QueryBuilder.AsOfSystemTime]); expr is an
// interval such as "-10s" or a timestamp. Fetching fails with an error
// wrapping [ErrNotSupported] on other products.
func AsOfSystemTime(expr string) *FetchOptions {
	return &FetchOptions{AsOfSystemTime: expr}
}

// IncludeDeleted returns a [FetchOptions] that includes soft-deleted records in query results.
func IncludeDeleted() *FetchOptions {
	return &FetchOptions{WithDeleted: true}
}

func resolveFetchOpts(opts []*FetchOptions) *FetchOptions {
	res := &FetchOptions{}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if opt.Lock {
			res.Lock = true
		}
		if opt.LockMode != LockNone {
			res.LockMode = opt.LockMode
		}
		if len(opt.LockOf) > 0 {
			res.LockOf = append(res.LockOf, opt.LockOf...)
		}
		if opt.AsOfSystemTime != "" {
			res.AsOfSystemTime = opt.AsOfSystemTime
		}
		if opt.SkipLocked {
			res.SkipLocked = true
		}
		if opt.NoWait {
			res.NoWait = true
		}
		if opt.LimitCount > 0 {
			res.LimitCount = opt.LimitCount
		}
		if opt.LimitStart > 0 {
			res.LimitStart = opt.LimitStart
		}
		if len(opt.Sort) > 0 {
			res.Sort = append(res.Sort, opt.Sort...)
		}
		if len(opt.Preload) > 0 {
			res.Preload = append(res.Preload, opt.Preload...)
		}
		if len(opt.Scopes) > 0 {
			res.Scopes = append(res.Scopes, opt.Scopes...)
		}
		if opt.WithDeleted {
			res.WithDeleted = true
		}
		if opt.HardDelete {
			res.HardDelete = true
		}
	}
	return res
}

// FetchOne loads a single record into target. Unlike [Get], it does not allocate a new
// object; instead it scans into the provided pointer. Returns [os.ErrNotExist] if no
// record matches.
func FetchOne[T any](ctx context.Context, target *T, where any, opts ...*FetchOptions) error {
	return Table[T]().FetchOne(ctx, target, where, opts...)
}

// Get will instanciate a new object of type T and return a pointer to it after loading from database
func Get[T any](ctx context.Context, where any, opts ...*FetchOptions) (*T, error) {
	return Table[T]().Get(ctx, where, opts...)
}

// Fetch returns all records matching the where clause. Pass nil for where to fetch all
// records. Use [FetchOptions] to control sorting, limits, and preloading.
func Fetch[T any](ctx context.Context, where any, opts ...*FetchOptions) ([]*T, error) {
	return Table[T]().Fetch(ctx, where, opts...)
}

// Iter returns a Go 1.23 iterator function that yields records one at a time.
// This is more memory-efficient than [Fetch] for large result sets since rows
// are scanned lazily. Use with range:
//
//	iter, err := psql.Iter[User](ctx, nil)
//	for user := range iter { ... }
//
// The query is executed when Iter returns; the iterator MUST be consumed (or
// at least started and broken out of) to release the underlying rows. Errors
// while scanning rows cannot be returned through this signature and cause a
// panic; use [IterErr] to receive them as values instead.
func Iter[T any](ctx context.Context, where any, opts ...*FetchOptions) (func(func(v *T) bool), error) {
	return Table[T]().Iter(ctx, where, opts...)
}

// IterErr is like [Iter] but yields (record, error) pairs, so that errors
// while reading rows are delivered to the loop instead of panicking. After an
// error is yielded the iteration stops.
//
//	it, err := psql.IterErr[User](ctx, nil)
//	for user, err := range it {
//	    if err != nil { return err }
//	    ...
//	}
//
// As with [Iter], the iterator must be consumed to release the rows.
func IterErr[T any](ctx context.Context, where any, opts ...*FetchOptions) (iter.Seq2[*T, error], error) {
	return Table[T]().IterErr(ctx, where, opts...)
}

// selectQuery builds the SELECT used by the fetch operations: all columns,
// soft delete filter, sort, optional limit/offset, lock and scopes.
func (t *TableMeta[T]) selectQuery(bt *boundTable, where any, opt *FetchOptions, withLimit bool) *QueryBuilder {
	req := B().Select(Raw(bt.fldStr)).From(bt.name)
	if where != nil {
		req = req.Where(where)
	}
	t.applySoftDelete(bt, req, opt)

	if len(opt.Sort) > 0 {
		req = req.OrderBy(opt.Sort...)
	}

	if withLimit && opt.LimitCount > 0 {
		if opt.LimitStart > 0 {
			req = req.Limit(opt.LimitStart, opt.LimitCount)
		} else {
			req = req.Limit(opt.LimitCount)
		}
	}

	if opt.LockMode != LockNone {
		req.SetLockMode(opt.LockMode)
	} else if opt.Lock {
		req.ForUpdate = true
	}
	if opt.Lock || opt.LockMode != LockNone {
		req.SkipLocked = opt.SkipLocked
		req.NoWait = opt.NoWait
	}
	if len(opt.LockOf) > 0 {
		req.LockOf(opt.LockOf...)
	}
	if opt.AsOfSystemTime != "" {
		req.AsOfSystemTime(opt.AsOfSystemTime)
	}
	return req.Apply(opt.Scopes...)
}

// Get returns a new object loaded from the first record matching where, or
// [os.ErrNotExist] if there is none. Sort, lock, scope, soft delete and
// preload options are honored.
func (t *TableMeta[T]) Get(ctx context.Context, where any, opts ...*FetchOptions) (*T, error) {
	if t == nil {
		return nil, ErrNotReady
	}
	res := t.newobj()
	if err := t.FetchOne(ctx, res, where, opts...); err != nil {
		return nil, err
	}
	return res, nil
}

// FetchOne loads the first record matching where into target, or returns
// [os.ErrNotExist] if there is none. See [FetchOne].
func (t *TableMeta[T]) FetchOne(ctx context.Context, target *T, where any, opts ...*FetchOptions) error {
	if t == nil {
		return ErrNotReady
	}
	if target == nil {
		return fmt.Errorf("FetchOne requires a non-nil target")
	}
	t.check(ctx)
	opt := resolveFetchOpts(opts)
	bt := t.bind(GetBackend(ctx))

	req := t.selectQuery(bt, where, opt, false).Limit(1)

	// run query
	rows, err := req.RunQuery(ctx)
	if err != nil {
		logQueryError(ctx, "psql:fetch_one:run_fail", t.table, "", err)
		return err
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		// no result
		return os.ErrNotExist
	}

	err = t.ScanTo(ctx, rows, target)
	// Close rows before preloading to free the connection
	rows.Close()
	if err != nil {
		return err
	}

	if len(opt.Preload) > 0 {
		if err := PreloadOpts(ctx, []*T{target}, opt, opt.Preload...); err != nil {
			return err
		}
	}

	return nil
}

// Fetch returns every record matching where (nil for all records). See [Fetch].
func (t *TableMeta[T]) Fetch(ctx context.Context, where any, opts ...*FetchOptions) ([]*T, error) {
	if t == nil {
		return nil, ErrNotReady
	}
	t.check(ctx)
	opt := resolveFetchOpts(opts)
	bt := t.bind(GetBackend(ctx))

	req := t.selectQuery(bt, where, opt, true)

	// run query
	rows, err := req.RunQuery(ctx)
	if err != nil {
		logQueryError(ctx, "psql:fetch:run_fail", t.table, "", err)
		return nil, err
	}

	final, err := t.spawnAll(ctx, rows) // closes rows
	if err != nil {
		return nil, err
	}

	if len(opt.Preload) > 0 && len(final) > 0 {
		if err := PreloadOpts(ctx, final, opt, opt.Preload...); err != nil {
			return nil, err
		}
	}

	return final, nil
}

// Iter runs the query and returns an iterator over the matching records.
// See [Iter] for the consumption and error semantics.
func (t *TableMeta[T]) Iter(ctx context.Context, where any, opts ...*FetchOptions) (func(func(v *T) bool), error) {
	it, err := t.IterErr(ctx, where, opts...)
	if err != nil {
		return nil, err
	}
	return func(yield func(v *T) bool) {
		for v, err := range it {
			if err != nil {
				// iter process has no error reporting method other than panic
				panic(err)
			}
			if !yield(v) {
				return
			}
		}
	}, nil
}

// IterErr runs the query and returns an iterator yielding (record, error)
// pairs. See [IterErr].
func (t *TableMeta[T]) IterErr(ctx context.Context, where any, opts ...*FetchOptions) (iter.Seq2[*T, error], error) {
	if t == nil {
		return nil, ErrNotReady
	}
	t.check(ctx)
	opt := resolveFetchOpts(opts)
	bt := t.bind(GetBackend(ctx))

	req := t.selectQuery(bt, where, opt, true)

	// run query
	rows, err := req.RunQuery(ctx)
	if err != nil {
		logQueryError(ctx, "psql:iter:run_fail", t.table, "", err)
		return nil, err
	}

	return func(yield func(*T, error) bool) {
		defer rows.Close()

		plan, err := t.newScanPlan(ctx, rows)
		if err != nil {
			yield(nil, err)
			return
		}
		for rows.Next() {
			val := t.newobj()
			if err := plan.scan(ctx, rows, val, true); err != nil {
				yield(nil, err)
				return
			}
			if !yield(val, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(nil, err)
		}
	}, nil
}
