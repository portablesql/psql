# Scopes & Lazy Loading

## Scopes

A `psql.Scope` is a function from `*psql.QueryBuilder` to
`*psql.QueryBuilder`. Scopes are applied to the query built by `Fetch`,
`Get`, `FetchOne`, `Iter`, `Count` and friends (through
`psql.WithScope`) or to any builder (through `Apply`), after the `where`
and options have been added.

```go
var Active psql.Scope = func(q *psql.QueryBuilder) *psql.QueryBuilder {
    return q.Where(map[string]any{"Status": "active"})
}

func RecentN(n int) psql.Scope {
    return func(q *psql.QueryBuilder) *psql.QueryBuilder {
        return q.OrderBy(psql.S("CreatedAt", "DESC")).Limit(n)
    }
}

users, err := psql.Fetch[User](ctx, nil, psql.WithScope(Active, RecentN(10)))
admins, err := psql.Fetch[User](ctx, map[string]any{"Role": "admin"}, psql.WithScope(Active), psql.Sort(psql.S("Login", "ASC")))
n, err := psql.Count[User](ctx, nil, psql.WithScope(Active))

rows, err := psql.B().Select().From("users").Apply(Active, RecentN(10)).RunQuery(ctx)
```

Scopes see the raw builder: use column names, and remember that a scope
setting `Limit` overrides a `psql.Limit` option (scopes run last).

## Lazy Loading

`psql.Future[T]` defers a lookup by column value until it is needed, and
resolves every pending future for the same table and column with a single
`WHERE col IN (...)` query. It is meant for the "resolve a reference in each
of many objects" pattern (API responses, templates).

### Batches

Two ways to create futures:

```go
// Per-request batch carried by the context
ctx = psql.WithLazyBatch(ctx)
for _, post := range posts {
    post.Author = psql.LazyCtx[User](ctx, "ID", post.AuthorID)
}
// the first Resolve (or JSON marshal) loads every author in one query
author, err := posts[0].Author.Resolve(ctx)

// Global per-table registry (no context)
f1 := psql.Lazy[User]("ID", "1")
f2 := psql.Lazy[User]("ID", "2")
u1, err := f1.Resolve(ctx) // SELECT ... WHERE "ID" IN (?,?)
u2, err := f2.Resolve(ctx) // already resolved
```

- `psql.LazyCtx(ctx, col, val)` joins the batch carried by `ctx` (created
  with `psql.WithLazyBatch`; `psql.LazyBatchInContext(ctx)` tells whether
  there is one). Without a batch in the context, the future gets a private
  batch and resolves alone. The backend found in `ctx` is remembered so the
  future is never resolved against another backend.
- `psql.Lazy(col, val)` uses a per-table registry that only holds weak
  references: a future that is dropped before being resolved is garbage
  collected and never queried. `psql.LazyPending[T]()` reports how many
  such futures are still pending. The backend is only known at resolve time,
  so a leader batches the peers that are not yet bound, or bound to the same
  backend. In multi-tenant code prefer `LazyCtx`.
- Within a batch, `Lazy`/`LazyCtx` called again with the same column, value
  and backend returns the same `*Future` until it is resolved.
- The column may be a Go field name or a column name. Values are given as
  strings and converted to the column's Go type before querying, so `"01"`
  and `"1"` are the same key for an integer column. Only string, integer and
  `[]byte` columns are batched; other column types resolve one query each.
- Batches are chunked with `psql.PreloadChunkSize` keys per query.

### Resolving

```go
user, err := future.Resolve(ctx)
switch {
case errors.Is(err, os.ErrNotExist): // no such row
case errors.Is(err, psql.ErrNotReady): // no backend available
}
```

- `Resolve(nil)` uses the context given to `LazyCtx`, or `psql.DefaultBackend`
  for `Lazy` futures; with no backend reachable it returns `psql.ErrNotReady`
  without querying.
- The first goroutine to resolve a pending future becomes the batch leader;
  other goroutines resolving a future in that batch wait for the same
  result. A resolved future returns its cached result (or error) forever.
- Soft-deleted rows are not found.
- `Future[T]` implements `json.Marshaler` (resolving with a nil context) and
  `MarshalContextJSON` for `pjson.MarshalContext`, so a future in a response
  struct is expanded to the record when encoded:

```go
type Response struct {
    Author *psql.Future[User] `json:"author"`
}

data, err := json.Marshal(Response{Author: psql.LazyCtx[User](ctx, "ID", "42")})
```

## Change Detection

Objects whose struct embeds `psql.Name` or `psql.Key` record their column
values whenever they are scanned or written. `psql.HasChanged(obj)`
compares the current values with that record; objects without a state field,
or never loaded from nor saved to the database, always report `true`.
`Update` uses the same record to write only the changed columns. See
[Object Binding](object-binding.md#change-tracking-and-update).
