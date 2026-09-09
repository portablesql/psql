# Associations

Associations declare relationships between row types with the `psql` struct
tag and are loaded in batches (one `WHERE col IN (...)` query per
association) to avoid N+1 queries. Association fields are not columns.

## Association Types

```go
type Author struct {
    psql.Name `sql:"authors"`
    ID        int64    `sql:",key=PRIMARY"`
    FullName  string   `sql:",type=VARCHAR,size=128"`
    Profile   *Profile `psql:"has_one:AuthorID"`                   // FK on Profile
    Books     []*Book  `psql:"has_many:AuthorID;order='Title ASC'"` // FK on Book
    Tags      []Tag    `psql:"many_to_many:author_tags,author_id,tag_id"`
}

type Profile struct {
    psql.Name `sql:"profiles"`
    ID        int64  `sql:",key=PRIMARY"`
    AuthorID  int64
    Bio       string `sql:",type=VARCHAR,size=512"`
}

type Book struct {
    psql.Name `sql:"books"`
    ID        int64   `sql:",key=PRIMARY"`
    AuthorID  *int64                            // nullable FK: nil means no author
    Title     string  `sql:",type=VARCHAR,size=256"`
    Author    *Author `psql:"belongs_to:AuthorID"` // FK on this struct
}

type Tag struct {
    psql.Name `sql:"tags"`
    ID        int64  `sql:",key=PRIMARY"`
    Label     string `sql:",type=VARCHAR,size=64"`
}
```

| Kind | Tag | Foreign key lives on | Field type |
|------|-----|----------------------|------------|
| `belongs_to` | `psql:"belongs_to:FK"` | this struct; matched against the target's primary key | `*T` or `T` |
| `has_one` | `psql:"has_one:FK"` | the target; matched against this struct's primary key | `*T` or `T` |
| `has_many` | `psql:"has_many:FK[;order='...']"` | the target; matched against this struct's primary key | `[]*T` or `[]T` |
| `many_to_many` | `psql:"many_to_many:JoinTable,FK,OtherFK[;order='...']"` | a join table: `FK` references this struct's primary key, `OtherFK` the target's | `[]*T` or `[]T` |

- `FK` is a Go field name or a column name on the side that holds it.
  Foreign key fields may be pointers; a nil foreign key leaves the
  association unset.
- The join table of a `many_to_many` is not created by psql; it must exist
  with at least the two columns. Its name is used as given (the namer does
  not apply).
- `order='Col [ASC|DESC][,Col2 ...]'` sorts `has_many` and `many_to_many`
  results (column or Go field names of the target). Without it, results are
  in database order. The attribute is ignored with a warning on the other kinds.
- Malformed tags are logged with a warning at `psql.Table[T]()` time and the
  field is ignored; a `has_many`/`many_to_many` field must be a slice.

Every side must have a single-column primary key (`key=PRIMARY` on a column,
or a `psql.Key` with `type=PRIMARY,fields='Col'`): `belongs_to` needs it on
the target, `has_one`/`has_many` on the parent, `many_to_many` on both.

## Preloading

```go
// after the fact
books, err := psql.Fetch[Book](ctx, nil)
err = psql.Preload(ctx, books, "Author")

// as a fetch option, with Fetch, Get, FetchOne, FetchMapped and FetchGrouped (not Iter)
books, err = psql.Fetch[Book](ctx, nil, psql.WithPreload("Author"))
author, err := psql.Get[Author](ctx, map[string]any{"ID": int64(1)}, psql.WithPreload("Books", "Profile", "Tags"))

// with options: include soft-deleted targets
err = psql.PreloadOpts(ctx, books, psql.IncludeDeleted(), "Author")
```

- `Preload` and `PreloadOpts` accept several association names; each is
  loaded with its own batch. Unknown names return an error.
- Preloading an empty or nil slice is a no-op.
- Both types must be registered (`psql.Table[T]()` is called automatically
  by any operation; call it explicitly for types only used as targets).
- Targets exclude soft-deleted rows unless the fetch used
  `psql.IncludeDeleted()`; `WithPreload` passes the parent fetch's options
  along, so `Fetch[Author](ctx, nil, psql.IncludeDeleted(),
  psql.WithPreload("Books"))` includes deleted books.
- Every row is fetched once and shared: parents referencing the same target
  (`belongs_to`, `many_to_many`) hold the same `*T`, so a mutation through one
  parent is visible through the others. With value-typed fields (`Author
  Author`, `Books []Book`) each parent receives its own copy.
- `AfterScan` hooks run on the loaded targets.

### Queries issued

For `belongs_to`, `has_one` and `has_many`: one `SELECT ... WHERE col IN
(...)` on the target table per chunk of `psql.PreloadChunkSize` keys
(default 1000). For `many_to_many`: one `SELECT FK, OtherFK FROM JoinTable
WHERE FK IN (...)` per chunk of parent keys, then one query per chunk of
target keys. Keys are deduplicated, and matched in Go regardless of their
Go type (an `int32` foreign key matches an `int64` primary key; `[]byte`
and string keys compare by content). When an ordered result spans several
chunks, it is re-sorted in Go using the `order` columns.

### Unset associations

When nothing matches, pointer fields stay nil and slices stay nil (not
empty). Value-typed single fields keep their zero value.
