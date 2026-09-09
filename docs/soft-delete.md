# Soft Delete

A table has soft delete when its struct declares a nullable timestamp
column for it. `Delete` then sets that column instead of removing the row,
and reads exclude rows where it is set.

## Declaring the Column

Exactly two declarations enable soft delete:

```go
type Post struct {
    psql.Name `sql:"posts"`
    ID        uint64 `sql:",key=PRIMARY"`
    Title     string `sql:",type=VARCHAR,size=256"`
    DeletedAt *time.Time            // 1. a *time.Time field named DeletedAt
}

type Comment struct {
    psql.Name `sql:"comments"`
    ID        uint64     `sql:",key=PRIMARY"`
    Removed   *time.Time `sql:",softdelete"` // 2. any *time.Time field tagged softdelete
}
```

The field must be a `*time.Time` (NULL means "not deleted"). Other
`*time.Time` fields have no effect. `psql.Table[T]().HasSoftDelete()`
reports whether a table has the column.

## Behavior

```go
// Delete: UPDATE "posts" SET "DeletedAt"=? WHERE ("ID"=?) AND ("DeletedAt" IS NULL)
_, err := psql.Delete[Post](ctx, map[string]any{"ID": uint64(1)})

// reads exclude deleted rows: ... WHERE ("DeletedAt" IS NULL)
posts, err := psql.Fetch[Post](ctx, nil)
n, err := psql.Count[Post](ctx, nil)

// include them
all, err := psql.Fetch[Post](ctx, nil, psql.IncludeDeleted())

// Restore: UPDATE "posts" SET "DeletedAt"=NULL WHERE ("ID"=?)
_, err = psql.Restore[Post](ctx, map[string]any{"ID": uint64(1)})

// ForceDelete: DELETE FROM "posts" WHERE ("ID"=?)
_, err = psql.ForceDelete[Post](ctx, map[string]any{"ID": uint64(1)})
```

- `Delete` adds `AND "DeletedAt" IS NULL`, so already deleted rows are not
  touched (and are not counted in `RowsAffected`). `Delete(ctx, nil)` soft
  deletes every live row.
- The filter applies to `Get`, `Fetch`, `FetchOne`, `Iter`, `IterErr`,
  `FetchMapped`, `FetchGrouped`, `Count`, lazy futures and association
  preloads. Preloads follow the option of the parent fetch: `Fetch[Author](ctx,
  nil, psql.IncludeDeleted(), psql.WithPreload("Books"))` also includes
  deleted books, and `psql.PreloadOpts(ctx, authors, psql.IncludeDeleted(),
  "Books")` does the same for an explicit preload.
- `psql.Restore(ctx, nil)` restores every row. On a table without soft
  delete, `Restore` returns `psql.ErrNotReady`.
- `ForceDelete` is `Delete` with the `HardDelete` fetch option; `DeleteOne`
  performs a soft delete on soft delete tables.
- The query builder does not know about soft delete: `psql.B().Select().From("posts")`
  returns deleted rows unless you add the condition yourself.
