# Hooks

Hooks are lifecycle callbacks implemented as methods on the row type. psql
checks for them with type assertions on the `*T` passed to (or produced by)
an operation, so they must be declared on the pointer receiver.

## Available Hooks

| Interface | Method | Fires on |
|-----------|--------|----------|
| `BeforeSaveHook` | `BeforeSave(ctx) error` | `Insert`, `InsertIgnore`, `Replace`, `Update` |
| `BeforeInsertHook` | `BeforeInsert(ctx) error` | `Insert`, `InsertIgnore` |
| `AfterInsertHook` | `AfterInsert(ctx) error` | `Insert`, `InsertIgnore` |
| `BeforeUpdateHook` | `BeforeUpdate(ctx) error` | `Update` |
| `AfterUpdateHook` | `AfterUpdate(ctx) error` | `Update`, only when a statement was executed |
| `AfterSaveHook` | `AfterSave(ctx) error` | `Insert`, `InsertIgnore`, `Replace`, `Update` (when a statement was executed) |
| `AfterScanHook` | `AfterScan(ctx) error` | every row scanned by `Get`, `Fetch`, `FetchOne`, `Iter`, `IterErr`, `FetchMapped`, `FetchGrouped`, `RunQueryT`, `RunQueryTOne`, `QT(...).All/Single/Each`, `ScanTo`, association preloads and lazy futures |

The `ctx` is the one given to the operation, so a hook runs inside the same
transaction, if any.

## Execution Order

```
Insert / InsertIgnore:  BeforeSave -> BeforeInsert -> INSERT -> AfterInsert -> AfterSave
Update:                 BeforeSave -> BeforeUpdate -> UPDATE -> AfterUpdate -> AfterSave
Replace:                BeforeSave -> REPLACE / UPSERT -> AfterSave
Fetch & co:             SELECT -> scan row -> AfterScan
```

Notes:

- `Update` skips objects that have no changed column (see
  [change tracking](object-binding.md#change-tracking-and-update)): the
  `Before*` hooks still run (and may modify the object, which counts as a
  change), but `AfterUpdate` and `AfterSave` do not.
- On PostgreSQL, `Insert` and `Replace` refresh the object from the
  `RETURNING` row before the `After*` hooks run; that refresh does not fire
  `AfterScan`.
- `Delete`, `Restore` and `ForceDelete` operate by `where` clause and do
  not load objects, so no hook fires for them.

## Error Handling

- A `Before*` hook returning an error aborts the operation for that object
  and is returned to the caller; nothing is written for it.
- An `After*` hook returning an error is returned to the caller, but the
  statement has already been executed.
- An `AfterScan` error makes the fetch fail; `Get`/`FetchOne` return the
  error, `Fetch` returns the error with the rows scanned so far discarded,
  `IterErr` yields it and `Iter` panics.

With several objects, hooks run per object in order and the first error
stops the batch. Objects already written stay written unless the whole call
is inside a [transaction](transactions.md).

## Examples

### Defaults and validation

```go
type Article struct {
    psql.Name `sql:"articles"`
    ID        uint64    `sql:",key=PRIMARY"`
    Title     string    `sql:",type=VARCHAR,size=256"`
    Slug      string    `sql:",type=VARCHAR,size=256"`
    CreatedAt time.Time
}

func (a *Article) BeforeInsert(ctx context.Context) error {
    if a.Slug == "" {
        a.Slug = strings.ToLower(strings.ReplaceAll(a.Title, " ", "-"))
    }
    if a.CreatedAt.IsZero() {
        a.CreatedAt = time.Now()
    }
    return nil
}

func (a *Article) BeforeSave(ctx context.Context) error {
    if a.Title == "" {
        return errors.New("title is required")
    }
    return nil
}
```

Changes made by a `Before*` hook are what gets written.

### Post-load processing

```go
type Config struct {
    psql.Name `sql:"configs"`
    ID        uint64 `sql:",key=PRIMARY"`
    RawJSON   string `sql:",type=TEXT"`
    Parsed    map[string]any `sql:"-"`
}

func (c *Config) AfterScan(ctx context.Context) error {
    return json.Unmarshal([]byte(c.RawJSON), &c.Parsed)
}
```

(For plain JSON columns, prefer a `format=json` field, which needs no hook.)

### Audit logging

```go
func (u *User) AfterUpdate(ctx context.Context) error {
    slog.InfoContext(ctx, "user updated", "user_id", u.ID)
    return nil
}
```
