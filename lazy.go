package psql

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"weak"

	"github.com/KarpelesLab/pjson"
)

// Future represents a lazily-loaded database record. Created by [Lazy] or
// [LazyCtx], it defers the actual database query until [Future.Resolve] is
// called. When resolved, all pending futures of the same batch for the same
// table and column are fetched with a single "WHERE col IN (...)" query,
// significantly reducing database round trips.
//
// Batching is scoped: futures created with [LazyCtx] share the batch carried
// by their context (see [WithLazyBatch]); futures created with [Lazy] share a
// per-table registry that only holds weak references, so a future that is
// dropped before being resolved is garbage collected and never queried. A
// leader only resolves peers that recorded the same [Backend] as its own
// context (or none yet), so batches never cross tenants or transactions.
//
// Concurrent Resolve calls share the same result. Future also implements
// json.Marshaler and the pjson context-aware marshaler.
type Future[T any] struct {
	col   string
	val   string
	key   lazyKey
	table *TableMeta[T]
	scope *lazyScope
	ctx   context.Context // creation context for LazyCtx futures, may be nil

	obj  *T
	err  error
	done uint32 // atomic: 0=pending, 1=resolved
	wait chan struct{}

	// The following fields are guarded by scope.mu.
	claimed bool     // a leader owns this future and will resolve it
	be      *Backend // backend this future is bound to, nil until known
	norm    any      // canonical key of val as seen through the column's Go type
	arg     any      // val converted to the column's Go type (query argument)
	normOK  bool     // norm/arg are valid
}

// lazyKey identifies a future within a scope. Futures are deduplicated per
// table, column, value and backend.
type lazyKey struct {
	table any // *TableMeta[T]
	col   string
	val   string
	be    *Backend
}

// lazyScope is a set of pending futures that may be batch-resolved together.
// A scope is either a context batch (strong references) or a per-table
// registry (weak references).
type lazyScope struct {
	mu      sync.Mutex
	weak    bool
	m       map[lazyKey]any // *Future[T] (batch) or lazyWeak[T] (registry)
	inserts int             // inserts since the last prune (registry only)
}

func newLazyScope(weakRefs bool) *lazyScope {
	return &lazyScope{weak: weakRefs, m: make(map[lazyKey]any)}
}

type lazyBatchCtxKey struct{}

// WithLazyBatch returns a context carrying a new lazy batch. All futures
// created with [LazyCtx] from this context (or contexts derived from it) join
// the batch, and resolving any of them resolves every pending future of the
// batch for the same table and column in a single query. A typical use is one
// batch per request:
//
//	ctx = psql.WithLazyBatch(ctx)
//	for _, post := range posts {
//	    post.Author = psql.LazyCtx[User](ctx, "ID", post.AuthorID)
//	}
//	// first Resolve (or json marshal) loads every author at once
func WithLazyBatch(ctx context.Context) context.Context {
	return context.WithValue(ctx, lazyBatchCtxKey{}, newLazyScope(false))
}

// LazyBatchInContext reports whether ctx carries a batch created by [WithLazyBatch].
func LazyBatchInContext(ctx context.Context) bool {
	return lazyBatchFromCtx(ctx) != nil
}

func lazyBatchFromCtx(ctx context.Context) *lazyScope {
	if ctx == nil {
		return nil
	}
	if s, ok := ctx.Value(lazyBatchCtxKey{}).(*lazyScope); ok {
		return s
	}
	return nil
}

// lazyRegistries maps *TableMeta[T] to the per-table weak registry used by Lazy.
var lazyRegistries sync.Map

func lazyRegistry[T any](t *TableMeta[T]) *lazyScope {
	if v, ok := lazyRegistries.Load(t); ok {
		return v.(*lazyScope)
	}
	v, _ := lazyRegistries.LoadOrStore(t, newLazyScope(true))
	return v.(*lazyScope)
}

// lazyDeref returns the future stored in a scope entry, or nil if it was
// garbage collected.
func lazyDeref[T any](v any) *Future[T] {
	switch x := v.(type) {
	case *Future[T]:
		return x
	case lazyWeak[T]:
		return x.p.Value()
	}
	return nil
}

// Lazy returns a Future that will be resolved in the future. Multiple calls to
// Lazy with the same column and value return the same Future until it is
// resolved, so concurrent code sharing a table naturally deduplicates work.
//
// Futures created by Lazy join a per-table registry holding weak references:
// resolving any of them also resolves the other pending futures of the same
// column that are still referenced, with one "WHERE col IN (...)" query, while
// futures that were dropped are simply garbage collected. Because Lazy has no
// context, the backend is only known at resolve time; a future is claimed by
// the first leader to resolve it and peers already bound to a different
// backend are left alone. In multi-tenant code prefer [LazyCtx].
func Lazy[T any](col, val string) *Future[T] {
	t := Table[T]()
	return lazyIn(t, lazyRegistry(t), nil, col, val)
}

// LazyCtx is like [Lazy] but scopes the future to the batch carried by ctx
// (see [WithLazyBatch]). The backend found in ctx is recorded so that the
// future is never resolved against a different backend by another leader,
// and [Future.Resolve] with a nil context (as used by json.Marshal) uses ctx.
// If ctx carries no batch, the future gets a private batch and resolves alone.
func LazyCtx[T any](ctx context.Context, col, val string) *Future[T] {
	t := Table[T]()
	scope := lazyBatchFromCtx(ctx)
	if scope == nil {
		scope = newLazyScope(false)
	}
	return lazyIn(t, scope, ctx, col, val)
}

func lazyIn[T any](t *TableMeta[T], scope *lazyScope, ctx context.Context, col, val string) *Future[T] {
	var be *Backend
	if ctx != nil {
		be = GetBackend(ctx)
	}
	k := lazyKey{table: t, col: col, val: val, be: be}

	scope.mu.Lock()
	defer scope.mu.Unlock()

	if v, ok := scope.m[k]; ok {
		if f := lazyDeref[T](v); f != nil && atomic.LoadUint32(&f.done) == 0 && !f.claimed {
			return f
		}
		delete(scope.m, k)
	}

	f := &Future[T]{
		col:   col,
		val:   val,
		key:   k,
		table: t,
		scope: scope,
		ctx:   ctx,
		be:    be,
		wait:  make(chan struct{}),
	}
	if scope.weak {
		scope.m[k] = lazyWeak[T]{p: weak.Make(f)}
		scope.inserts++
		if scope.inserts > len(scope.m) {
			scope.pruneLocked()
		}
	} else {
		scope.m[k] = f
	}
	return f
}

// pruneLocked removes registry entries whose future was garbage collected.
// scope.mu must be held.
func (s *lazyScope) pruneLocked() {
	s.inserts = 0
	for k, v := range s.m {
		if p, ok := v.(interface{ alive() bool }); ok && !p.alive() {
			delete(s.m, k)
		}
	}
}

// lazyWeak wraps weak.Pointer so the non-generic scope can test liveness.
type lazyWeak[T any] struct{ p weak.Pointer[Future[T]] }

func (w lazyWeak[T]) alive() bool { return w.p.Value() != nil }

// LazyPending returns the number of unresolved futures for T created with
// [Lazy] that are still referenced. Entries whose future has been garbage
// collected are pruned by the call. It is mostly useful for diagnostics and
// tests.
func LazyPending[T any]() int {
	scope := lazyRegistry(Table[T]())
	scope.mu.Lock()
	defer scope.mu.Unlock()
	n := 0
	for k, v := range scope.m {
		f := lazyDeref[T](v)
		if f == nil || atomic.LoadUint32(&f.done) == 1 || f.claimed {
			delete(scope.m, k)
			continue
		}
		n++
	}
	scope.inserts = 0
	return n
}

// Resolve loads the record, running the query if needed. ctx may be nil, in
// which case the context given to [LazyCtx] is used, or [DefaultBackend] for
// futures created with [Lazy]; if no backend can be found, [ErrNotReady] is
// returned. If no record matches, the error is [os.ErrNotExist].
//
// The first Resolve for a pending future becomes the batch leader: it fetches
// its own record together with every other pending future of the same batch,
// table and column whose backend is the same (or not yet known). Other
// goroutines calling Resolve on a future being loaded wait for the result.
func (f *Future[T]) Resolve(ctx context.Context) (*T, error) {
	if atomic.LoadUint32(&f.done) == 1 {
		return f.obj, f.err
	}

	if ctx == nil {
		ctx = f.ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if GetBackend(ctx) == nil {
		return nil, ErrNotReady
	}

	// Try to be the resolver for this future
	f.resolve(ctx)

	// Wait until resolved (by us or by a batch leader)
	<-f.wait
	return f.obj, f.err
}

// lazyField returns the column's field and pointer-stripped Go type, or nil
// if the column is unknown.
func (f *Future[T]) lazyField() (*StructField, reflect.Type) {
	fld := findFieldByNameOrCol(f.table.fldcol, f.col)
	if fld == nil {
		return nil, nil
	}
	typ := f.table.typ.Field(fld.Index).Type
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	return fld, typ
}

// lazyBatchable reports whether values of the column's Go type can be
// reliably matched in Go against the rows returned by a batch query. Other
// column types (bool, float, time, custom scanners) are resolved one query
// per value so the database performs the comparison.
func lazyBatchable(typ reflect.Type) bool {
	if typ == nil {
		return false
	}
	switch typ.Kind() {
	case reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	case reflect.Slice:
		return typ.Elem().Kind() == reflect.Uint8
	}
	return false
}

// normalize converts val through the column's setter so that it is compared
// the way the database would compare it ("01" and "1" both become 1 for an
// integer column). It sets f.norm/f.arg/f.normOK; scope.mu must be held.
func (f *Future[T]) normalize(fld *StructField, typ reflect.Type) bool {
	if fld == nil || fld.setter == nil {
		f.normOK = false
		return false
	}
	v := reflect.New(typ).Elem()
	if err := fld.setter(v, sql.RawBytes(f.val)); err != nil {
		f.normOK = false
		return false
	}
	k, ok := canonicalKey(v)
	if !ok {
		f.normOK = false
		return false
	}
	f.norm = k
	f.arg = v.Interface()
	f.normOK = true
	return true
}

func (f *Future[T]) resolve(ctx context.Context) {
	be := GetBackend(ctx)
	scope := f.scope
	fld, typ := f.lazyField()
	batchable := lazyBatchable(typ)

	scope.mu.Lock()
	if atomic.LoadUint32(&f.done) == 1 || f.claimed {
		// resolved, or another leader claimed us as a peer
		scope.mu.Unlock()
		return
	}
	f.claimed = true
	f.be = be
	delete(scope.m, f.key)
	if batchable && !f.normalize(fld, typ) {
		batchable = false
	}

	// Collect pending peers for the same table and column that we may resolve.
	var peers []*Future[T]
	if batchable {
		for k, v := range scope.m {
			if k.table != any(f.table) || k.col != f.col {
				continue
			}
			p := lazyDeref[T](v)
			if p == nil || atomic.LoadUint32(&p.done) == 1 || p.claimed {
				delete(scope.m, k)
				continue
			}
			if p.be != nil && p.be != be {
				continue // bound to another backend
			}
			if !p.normalize(fld, typ) {
				continue // cannot be matched in Go, let it resolve alone
			}
			p.claimed = true
			p.be = be
			delete(scope.m, k)
			peers = append(peers, p)
		}
	}
	scope.mu.Unlock()

	if !batchable {
		f.finish(f.table.Get(ctx, map[string]any{f.col: f.val}))
		return
	}
	f.runBatch(ctx, fld, append([]*Future[T]{f}, peers...))
}

// finish stores the result and wakes waiters.
func (f *Future[T]) finish(obj *T, err error) {
	f.obj, f.err = obj, err
	atomic.StoreUint32(&f.done, 1)
	close(f.wait)
}

// runBatch fetches all rows for the given futures (all claimed, normalized,
// same column) and distributes the results.
func (f *Future[T]) runBatch(ctx context.Context, fld *StructField, all []*Future[T]) {
	// Deduplicate query arguments by canonical key.
	seen := make(map[any]struct{}, len(all))
	args := make([]any, 0, len(all))
	for _, p := range all {
		if _, dup := seen[p.norm]; dup {
			continue
		}
		seen[p.norm] = struct{}{}
		args = append(args, p.arg)
	}

	type row struct {
		obj  *T
		key  any
		used bool
	}
	var rows []*row
	for _, chunk := range chunkKeys(args, PreloadChunkSize) {
		results, err := f.table.Fetch(ctx, map[string]any{f.col: chunk})
		if err != nil {
			for _, p := range all {
				p.finish(nil, err)
			}
			return
		}
		for _, r := range results {
			key, ok := canonicalKey(reflect.ValueOf(r).Elem().Field(fld.Index))
			if !ok {
				continue
			}
			rows = append(rows, &row{obj: r, key: key})
		}
	}

	// Exact matching on canonical keys.
	byKey := make(map[any]*row, len(rows))
	for _, r := range rows {
		if _, dup := byKey[r.key]; !dup {
			byKey[r.key] = r
		}
	}
	var pending []*Future[T]
	for _, p := range all {
		if r, ok := byKey[p.norm]; ok {
			r.used = true
			p.finish(r.obj, nil)
			continue
		}
		pending = append(pending, p)
	}
	if len(pending) == 0 {
		return
	}

	// Rows the database returned that no request claimed indicate a looser
	// comparison on the database side (case-insensitive collation, trailing
	// space padding...). Try a case-insensitive match for string columns,
	// then let the database decide with one query per remaining value.
	var unused []*row
	for _, r := range rows {
		if !r.used {
			unused = append(unused, r)
		}
	}
	if len(unused) > 0 {
		var still []*Future[T]
		for _, p := range pending {
			ps, ok := p.norm.(string)
			matched := false
			if ok {
				for _, r := range unused {
					if rs, ok := r.key.(string); ok && !r.used && strings.EqualFold(ps, rs) {
						r.used = true
						matched = true
						p.finish(r.obj, nil)
						break
					}
				}
			}
			if !matched {
				still = append(still, p)
			}
		}
		pending = still
		unused = unused[:0]
		for _, r := range rows {
			if !r.used {
				unused = append(unused, r)
			}
		}
	}
	if len(unused) > 0 {
		for _, p := range pending {
			p.finish(f.table.Get(ctx, map[string]any{f.col: p.arg}))
		}
		return
	}
	for _, p := range pending {
		p.finish(nil, os.ErrNotExist)
	}
}

// MarshalJSON resolves the future (see [Future.Resolve] with a nil context)
// and marshals the record. It returns [ErrNotReady] when no backend is
// available, and [os.ErrNotExist] when the record does not exist.
func (f *Future[T]) MarshalJSON() ([]byte, error) {
	v, err := f.Resolve(nil)
	if err != nil {
		return nil, err
	}
	return pjson.Marshal(v)
}

// MarshalContextJSON resolves the future using ctx and marshals the record.
// It is used by pjson.MarshalContext so that a whole response can be encoded
// against the request's backend or transaction.
func (f *Future[T]) MarshalContextJSON(ctx context.Context) ([]byte, error) {
	v, err := f.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	return pjson.Marshal(v)
}
