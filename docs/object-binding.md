# Object Binding

psql maps Go structs to database tables using struct tags. This page is the
reference for struct declarations, column types, keys, schema management and
the generic CRUD functions.

## Declaring a Table

```go
type User struct {
    psql.Name `sql:"users"`                       // table name, used as-is
    ID        uint64 `sql:",key=PRIMARY"`          // BIGINT(20) NOT NULL, primary key
    Email     string `sql:",type=VARCHAR,size=255,key=UNIQUE:email"`
    Login     string `sql:",type=VARCHAR,size=128"`
    Age       *int64                                // BIGINT NULL (pointer = nullable)
    Created   time.Time                             // DATETIME / TIMESTAMP / TEXT per engine
}
```

- `psql.Name` is optional. Without it the table name is derived from the Go
  type name through the backend's [naming strategy](naming-strategies.md)
  (`UserProfile` becomes `User_Profile` by default). A name given in the
  `psql.Name` tag is never transformed.
- The embedded field is itself called `Name`, so a struct embedding
  `psql.Name` cannot declare another field named `Name` (Go rejects the
  duplicate). To get a column called `Name`, give the column name in the
  tag: ``FullName string `sql:"Name,type=VARCHAR,size=128"` ``.
- Embedding `psql.Name` (or `psql.Key`) also gives the struct the row state
  used by [change tracking](#change-tracking-and-update).
- `psql.Table[T]()` builds the metadata for `T` (once, safe for concurrent
  use) and panics if `T` is not a struct, has no column fields, or has a field
  whose type cannot be scanned (see [Supported field types](#supported-field-types)).
  It does not touch the database.

## The `sql` Tag

Format: `sql:"[column],attr=value,attr=value,..."`

- The first element is the column name. Leave it empty (`sql:",type=INT"`) to
  use the Go field name. An explicit column name is used as-is; an implicit
  one goes through the namer's `ColumnName` (unchanged with the default
  `LegacyNamer`).
- Values containing commas must be quoted: `values='a,b,c'`,
  `fields='UserID,GroupID'`.
- `sql:"-"` excludes the field from the table.
- Fields with a `psql` tag are [associations](associations.md), not columns.
- Unexported fields are ignored.

| Attribute | Meaning |
|-----------|---------|
| `type` | SQL column type (`VARCHAR`, `BIGINT`, `TEXT`, `enum`, `set`, `VECTOR`, ...). Case-insensitive. |
| `size` | Length or precision: `VARCHAR(255)`, `DATETIME(6)`, `VECTOR(384)`. PostgreSQL ignores it on integer/float types. |
| `null` | `0` renders `NOT NULL`, `1` renders `NULL`. Inferred types set it automatically. |
| `default` | Default value (rendered as a quoted literal). `default=\N` means `DEFAULT NULL`. Also used by `psql.Factory[T]()`. |
| `values` | Allowed values for `enum` and `set` columns: `values='a,b,c'`. |
| `key` | Declares a key on this column, see [Keys and Indexes](#keys-and-indexes). |
| `import` | Import a predefined column definition, see [Import types](#import-types). |
| `collation` | `COLLATE` clause, MySQL only (ignored elsewhere). |
| `format=json` | Store the field as JSON, see [JSON fields](#json-fields). |
| `softdelete` | Marks a `*time.Time` field as the [soft delete](soft-delete.md) column. |
| `check=0` | On `psql.Name`: never create or alter this table, see [Schema management](#schema-management). |

`scale`, `unsigned` and `validator` are accepted by the parser (and appear in
some import types) but are not used by any engine.

## Column Types

### Type inference

A field with **no attributes at all** gets its column definition from its Go
type. Only these types are inferred:

| Go type | MySQL | PostgreSQL | SQLite |
|---------|-------|------------|--------|
| `int64`, `*int64` | `BIGINT(21)` | `bigint` | `integer` |
| `uint64`, `*uint64` | `BIGINT(20)` | `bigint` | `integer` |
| `float64`, `*float64` | `DOUBLE` | `double` (see note) | `real` |
| `bool`, `*bool` | `TINYINT(1)` | `tinyint` (see note) | `integer` |
| `time.Time`, `*time.Time` | `DATETIME(6)` | `TIMESTAMP(6) DEFAULT '1970-01-01 00:00:00.000000'` | `TEXT` |
| `time.Time` in a field named `Stamp` | `TIMESTAMP(6)` | `TIMESTAMP(6)` | `TEXT` |
| `[]byte` | `BLOB` | `BYTEA` | `blob` |
| `psql.Set`, `*psql.Set` | `SET(...)` (needs `values=`) | `varchar(128)` | `text` |
| `psql.Vector` | `vector(N)` | `vector(N)` | `text` |
| `xuid.XUID`, `*xuid.XUID` | `CHAR(36)` (UUID) | `char(36)` | `text` |

Non-pointer types are `NOT NULL`; pointer types are nullable. The inferred
`bool` and `float64` definitions render `tinyint` and `double` on
PostgreSQL, which are not PostgreSQL types, so the automatic `CREATE TABLE`
fails there: on PostgreSQL declare such fields explicitly
(`type=BOOLEAN`, `type=DOUBLE PRECISION`), which also works on the other
engines.

Inference only happens when the tag has no attribute other than `key` and
`softdelete`: ``ID uint64 `sql:",key=PRIMARY"` `` is still inferred, while
a field with `size=`, `null=` or `default=` must also carry `type=` or
`import=`.

Every other Go type, including `string`, `int`, `int32`, `uint8`, `float32`
and custom `sql.Scanner` types, has no inferred SQL type: give it an explicit
`type=` or `import=`, otherwise the column is silently left out of `CREATE
TABLE` and every operation on the table fails.

```go
type Product struct {
    psql.Name   `sql:"products"`
    ID          uint64   `sql:",key=PRIMARY"`
    ProductName string   `sql:"Name,type=VARCHAR,size=128"` // column "Name"
    Price       float64                                    // DOUBLE, inferred
    Stock       int32    `sql:",type=INT"`                 // explicit type required
    Description *string  `sql:",type=TEXT"`                // nullable
    Hidden      bool     `sql:"-"`                         // not a column
}
```

### Import types

`import=NAME` copies a predefined definition; attributes given next to it
override the imported ones (`import=[]uint8,null=1` is a nullable BLOB).

| Import | Definition |
|--------|------------|
| `INT` | `INT(11)` |
| `BIGINT` | `BIGINT(20)` |
| `KEY` | `BIGINT(20) NOT NULL` |
| `FLOAT`, `DOUBLE` | `FLOAT`, `DOUBLE` |
| `TEXT`, `LONGTEXT` | `TEXT`, `LONGTEXT` |
| `DATE` | `DATE` |
| `TS` | `TIMESTAMP(6)` |
| `DATETIME` | MySQL `DATETIME(6)`; PostgreSQL `TIMESTAMP(6)` with a 1970 default; SQLite `TEXT` |
| `JSON` | MySQL `LONGTEXT`; PostgreSQL `JSONB`; SQLite `TEXT`. Combine with an explicit `format=json` (see [JSON fields](#json-fields)) |
| `UUID` | `CHAR(36) DEFAULT '00000000-0000-0000-0000-000000000000'` |
| `CURRENCY`, `COUNTRY`, `LANGUAGE` | `CHAR(5) DEFAULT 'USD'`, `CHAR(3) DEFAULT 'US'`, `CHAR(5) DEFAULT 'en-US'` |
| `IP`, `CIDR` | `VARCHAR(39)`, `VARCHAR(43)` |
| `SHA1`, `SHA256` | `CHAR(40)`, `CHAR(64)` |
| Go type names (`int64`, `[]uint8`, `time.Time`, ...) | The inferred definitions of the table above |

SQLite maps every type to its affinity (`integer`, `real`, `blob` or `text`),
so sizes are not enforced there. Register your own definitions with
`psql.DefineMagicType("mypkg.Money", "type=DECIMAL,size=12")` (applies
automatically to fields of that Go type, or through `import=mypkg.Money`) and
`psql.DefineMagicTypeEngine(engine, name, def)` for engine-specific
variants; call them during initialization, before tables are used.

### Supported field types

Whatever the column type, the Go field must be scannable. Supported are:

- types implementing `sql.Scanner` (with a pointer receiver), such as
  `psql.Set`, `psql.Hex`, `psql.Vector` or your own types,
- `time.Time`,
- `bool`, `string`, every `int`/`uint` kind, `float32`, `float64`, `[]byte`,
- pointers to any of the above (NULL becomes a nil pointer; for non-pointer
  fields NULL becomes the zero value),
- any type that `encoding/json` can handle, when the field is tagged
  `format=json`.

Any other field type (a `map`, a `chan`, a nested struct without
`format=json`, ...) makes `psql.Table[T]()` panic with a message naming the
field, e.g. `psql: cannot use field Config.Weird as a column: unsupported
type chan int (...)`.

On write, nil pointers, nil slices and nil maps are stored as `NULL`, unless
the type implements `driver.Valuer` (a nil `psql.Set` is stored as `''`).

### Time values

`time.Time` values are always stored in UTC and read back as UTC.

| Engine | Stored as | Zero `time.Time` |
|--------|-----------|------------------|
| MySQL | `DATETIME(6)`, `2006-01-02 15:04:05.999999` | `0000-00-00 00:00:00.000000` |
| PostgreSQL | `TIMESTAMP(6)`, `2006-01-02 15:04:05.999999` | `0001-01-01 00:00:00.000000` |
| SQLite | `TEXT`, fixed-width RFC 3339 `2006-01-02T15:04:05.000000000Z` | `0001-01-01T00:00:00.000000000Z` |

The fixed-width SQLite format keeps lexical ordering equal to chronological
ordering. On MySQL the zero date requires a `sql_mode` without
`NO_ZERO_DATE`; the driver's default `sql_mode` allows it.

### JSON fields

`format=json` fields are marshaled with `encoding/json` on write and
unmarshaled on read, so any JSON-compatible type (maps, slices, structs) can
be a column. Change tracking compares their JSON encoding.

```go
type Document struct {
    psql.Name `sql:"documents"`
    ID        uint64         `sql:",key=PRIMARY"`
    Meta      map[string]any `sql:",import=JSON,format=json"`   // LONGTEXT / JSONB / TEXT per engine
    Tags      []string       `sql:",type=TEXT,format=json"`     // explicit type
}
```

`format=json` must appear literally in the tag: the JSON codec is selected
when the struct is registered, before engine imports are resolved, so
`import=JSON` alone makes `psql.Table[T]()` panic for a map or struct field.
An empty or NULL column yields the zero value (a nil map or slice).

### Enum and set columns

```go
type StatusEnum string

const (
    StatusPending StatusEnum = "pending"
    StatusActive  StatusEnum = "active"
)

type Account struct {
    psql.Name `sql:"accounts"`
    ID        uint64     `sql:",key=PRIMARY"`
    Status    StatusEnum `sql:",type=enum,values='pending,active,inactive'"`
    Perms     psql.Set   `sql:",type=set,values='read,write,admin'"`
}
```

The `values` list must be quoted. Per engine:

| Engine | `enum` | `set` |
|--------|--------|-------|
| MySQL | native `ENUM('pending','active','inactive')`, validated by the server | native `SET(...)` |
| PostgreSQL | `varchar(64)` (or `varchar(size)`) plus a table `CHECK` constraint named `chk_enum_<hash>` shared by every column with the same values; no custom type is created | `varchar(128)`, no validation |
| SQLite | `text`, no validation | `text`, no validation |

On PostgreSQL, changing the value list adds a new CHECK constraint but does
not drop the previous one; drop it by hand before inserting the new values.
Since only MySQL and PostgreSQL reject invalid values, validate in Go where it
matters:

```go
func (a *Account) BeforeSave(ctx context.Context) error {
    status := psql.Table[Account]().FieldByColumn("Status")
    return psql.ValidateEnum(status, string(a.Status))
}
```

`psql.ValidateEnum` returns nil for non-enum fields and an error wrapping
`psql.ErrInvalidEnumValue` otherwise.

### Custom types

- **`psql.Set`** (`[]string`): comma-separated list with `Set`, `Unset`
  and `Has` helpers. In a WHERE map it compares the whole value; use
  `psql.FindInSet` to test for one element.
- **`psql.Hex`** (`[]byte`): stored as a hexadecimal *string*, so the column
  must hold twice the byte length: a 32-byte hash needs `type=CHAR,size=64`
  (or `import=SHA256`), not `BINARY(32)`.
- **`psql.Vector`** (`[]float32`): see [Vectors](vectors.md).
- **`[]byte`**: stored as binary (`BLOB`/`BYTEA`); compared as a single
  value in WHERE maps.

## Keys and Indexes

### On a column

| Tag | Effect |
|-----|--------|
| `key=PRIMARY` | Primary key. Several columns with `key=PRIMARY` form a composite primary key, in declaration order. |
| `key=UNIQUE:name` | Unique index called `name`. Columns sharing the same name form one composite unique index. |
| `key=name` | Accepted as a plain (non-unique) index called `name`, but in the current version the column form leaves the key type unset and no engine creates it. Declare plain indexes with an embedded `psql.Key` (below). |

```go
type Event struct {
    psql.Name `sql:"events"`
    ID        uint64   `sql:",key=PRIMARY"`
    UserID    uint64
    EventDate string   `sql:",type=DATE"`
    Slug      string   `sql:",type=VARCHAR,size=64,key=UNIQUE:slug"` // unique index slug(Slug)
    ByUser    psql.Key `sql:"user_date,fields='UserID,EventDate'"`     // index user_date(UserID, EventDate)
}
```

A bare `key=UNIQUE` (without `:name`) is *not* a unique index either.

### With an embedded `psql.Key`

For keys that need a type other than PRIMARY/UNIQUE/INDEX, or to declare a
composite key in one place, embed a `psql.Key` field. Its tag gives the key
name (defaults to the field name), the `type` (`PRIMARY`, `UNIQUE`, `INDEX`
(default), `FULLTEXT`, `SPATIAL`, `VECTOR`) and the quoted `fields` list:

```go
type Membership struct {
    UserID  uint64
    GroupID uint64
    Role    string   `sql:",type=VARCHAR,size=32"`
    PK      psql.Key `sql:"PRIMARY,type=PRIMARY,fields='UserID,GroupID'"`
    RoleIdx psql.Key `sql:",fields='Role'"`               // INDEX "RoleIdx" (Role)
    RoleUq  psql.Key `sql:"role_uq,type=UNIQUE,fields='UserID,Role'"`
}
```

A `psql.Key` field, like `psql.Name`, carries the row state for change
tracking.

### How keys are created

The "main key" of a table is its primary key or, failing that, its first
unique key. `Update`, `Replace` and [associations](associations.md) require
one.

| Engine | PRIMARY | UNIQUE | INDEX | FULLTEXT / SPATIAL | VECTOR |
|--------|---------|--------|-------|--------------------|--------|
| MySQL | inline | inline `UNIQUE INDEX name` | inline `INDEX name` | inline | ignored |
| PostgreSQL | inline | constraint `<table>_<name>` | `CREATE INDEX "<table>_<name>"` | ignored | `CREATE INDEX ... USING hnsw` (`method=`, `opclass=` attributes) |
| SQLite | inline | `CREATE UNIQUE INDEX "<table>_<name>"` | `CREATE INDEX "<table>_<name>"` | ignored | ignored |

## Schema Management

`psql.Table[T]()` only builds metadata. The first operation on a table
through a given backend runs that engine's *schema check*: it creates the
table if it is missing, otherwise compares columns and keys and adds what is
missing. The check is serialized per table and remembered per backend once
it succeeds; if it fails (connection down, permission denied) the operation
proceeds, usually failing with the underlying error, and the check is
retried by the next operation.

What the check does when the table exists differs per engine:

| Engine | Missing column | Column differs | Missing key |
|--------|----------------|----------------|-------------|
| MySQL | `ALTER TABLE ... ADD` | `ALTER TABLE ... MODIFY` | `ALTER TABLE ... ADD INDEX` |
| PostgreSQL | `ALTER TABLE ... ADD` | logged as a warning, left unchanged | `CREATE INDEX` / missing enum `CHECK` constraints added |
| SQLite | `ALTER TABLE ... ADD COLUMN` (with a default for NOT NULL columns) | left unchanged | `CREATE INDEX` |

Nothing is ever dropped: extra columns and keys in the database are reported
with a warning and kept.

To control the check:

- `psql.NewBackend(engine, db, psql.WithSchemaCheck(false))` disables all
  implicit DDL for that backend (`be.SchemaCheckEnabled()` reports it). The
  driver constructors (`sqlite.New`, `mysql.New`, `pgsql.New`) do not take
  options, so wrap the connection yourself: `psql.NewBackend(be.Engine(),
  be.DB(), psql.WithSchemaCheck(false), psql.WithDriverData(be.DriverData()))`.
- `be.CheckStructure(ctx, psql.Table[User]())` runs the check explicitly
  (typically at startup) whether or not the automatic check is enabled.
- ``psql.Name `sql:"name,check=0"` `` opts a table out: the check is a no-op
  for it on every engine, which is how you map an existing table or a view.

```go
be, _ := psql.New(dsn)
be = psql.NewBackend(be.Engine(), be.DB(), psql.WithSchemaCheck(false), psql.WithDriverData(be.DriverData()))
ctx := be.Plug(context.Background())
for _, tbl := range []psql.TableView{psql.Table[User](), psql.Table[Order]()} {
    if err := be.CheckStructure(ctx, tbl); err != nil {
        log.Fatal(err)
    }
}
```

## CRUD Functions

All functions take a `context.Context` carrying the backend (see
[Getting Started](getting-started.md#attaching-to-context)). The `where`
argument accepts anything the [query builder](query-builder.md) accepts in
`Where`: a `map[string]any` (Go field or column names as keys), comparison
expressions, `psql.WhereOR`, or `nil` for no condition. Every function also
exists as a method on `psql.Table[T]()`.

```go
// Insert one or more objects
err := psql.Insert(ctx, &User{ID: 1, Email: "alice@example.com", Login: "Alice"})

// Get: first matching record, os.ErrNotExist if none
user, err := psql.Get[User](ctx, map[string]any{"ID": uint64(1)})

// FetchOne: like Get, but scans into an existing value
var u User
err = psql.FetchOne(ctx, &u, map[string]any{"Email": "alice@example.com"})

// Fetch: all matching records (nil where = all rows)
users, err := psql.Fetch[User](ctx, map[string]any{"Login": "Alice"})

// Update: writes the changed columns of previously loaded objects
user.Login = "Alice Smith"
err = psql.Update(ctx, user)

// Replace: insert or overwrite the row with the same key
err = psql.Replace(ctx, &User{ID: 1, Email: "alice@new.example", Login: "Alice"})

// InsertIgnore: skip rows that conflict with an existing key
err = psql.InsertIgnore(ctx, &User{ID: 1, Email: "dup@example.com"})

// Delete: soft or hard delete, see soft-delete.md. nil deletes every row!
_, err = psql.Delete[User](ctx, map[string]any{"ID": uint64(1)})

// DeleteOne: delete in a transaction, roll back unless exactly one row matched
err = psql.DeleteOne[User](ctx, map[string]any{"ID": uint64(2)})

// Count
n, err := psql.Count[User](ctx, map[string]any{"Login": "Alice"})
```

### Insert

- Each object is written with its own statement (one prepared statement per
  call). The [hooks](hooks.md) run per object, and the first error stops
  the batch without rolling back the objects already written.
- On PostgreSQL the statement uses `RETURNING`, and each object is refreshed
  with the stored row (server defaults included).
- On MySQL and SQLite, when the primary key is a single integer column that
  is still zero (or a nil pointer), it is populated from `LastInsertId`
  (`AUTO_INCREMENT` / rowid). Note that psql does not add `AUTO_INCREMENT`
  itself; on MySQL create the column that way when you rely on this.
- `InsertIgnore` renders `INSERT IGNORE` (MySQL), `INSERT OR IGNORE` (SQLite)
  or `ON CONFLICT DO NOTHING` (PostgreSQL). It fires the same hooks as `Insert`.
- `Replace` renders `REPLACE INTO` (MySQL), `INSERT OR REPLACE` (SQLite) or
  `INSERT ... ON CONFLICT (key) DO UPDATE SET ...` (PostgreSQL) on the main
  key. It fires only the `BeforeSave`/`AfterSave` hooks.

### Change tracking and Update

Objects whose struct embeds `psql.Name` or `psql.Key` remember the values
they had when they were last scanned from, or written to, the database.

- `psql.HasChanged(obj)` reports whether any column differs from that
  state. Objects without a state field, or never loaded/saved, always report
  `true`.
- `psql.Update(ctx, obj)` writes only the changed columns of tracked objects
  (all columns for untracked ones) and skips objects with no change, in
  which case the `After*` hooks do not fire. It needs a main key.

### Delete

`Delete(ctx, nil)` affects the whole table. A `psql.Limit` option is
rendered as `DELETE ... LIMIT n`, which only MySQL accepts: PostgreSQL
rejects it when rendering and the SQLite build in use rejects it at
execution. Tables with a soft delete column are updated instead of deleted,
see [Soft Delete](soft-delete.md).

### Factory

`psql.Factory[T](ctx)` returns a new `*T` with every field that has a
`default=` attribute pre-filled.

## Reading Rows

### Fetch options

`Fetch`, `Get`, `FetchOne`, `Iter`, `IterErr`, `FetchMapped`, `FetchGrouped`
and `Count` accept any number of `*psql.FetchOptions`:

```go
users, err := psql.Fetch[User](ctx, nil,
    psql.Sort(psql.S("Login", "ASC"), psql.S("ID", "DESC")),
    psql.Limit(10),            // LIMIT 10
)
page, err := psql.Fetch[User](ctx, nil, psql.LimitFrom(20, 10)) // LIMIT 10 OFFSET 20
locked, err := psql.Fetch[User](ctx, nil, psql.FetchLock)       // FOR UPDATE (inside a transaction)
```

| Option | Effect |
|--------|--------|
| `psql.Sort(fields...)` | `ORDER BY`; use `psql.S("Col", "ASC"/"DESC")`. Also honored by `Get`. |
| `psql.Limit(count)` | `LIMIT count` |
| `psql.LimitFrom(offset, count)` | `LIMIT count OFFSET offset`, on every engine |
| `psql.FetchLock` | `FOR UPDATE`; `psql.FetchLockSkipLocked` and `psql.FetchLockNoWait` add `SKIP LOCKED` / `NOWAIT`. Silently omitted on SQLite. |
| `psql.IncludeDeleted()` | Include soft-deleted rows (also passed on to preloads) |
| `psql.WithPreload("Field", ...)` | Load [associations](associations.md) after fetching |
| `psql.WithScope(scopes...)` | Apply [scopes](scopes-lazy.md) |

Options combine: later `Limit`/`Sort` values are appended or override as you
would expect; `nil` options are ignored.

### Iterators

```go
it, err := psql.IterErr[User](ctx, map[string]any{"Age": 30})
if err != nil {
    return err
}
for user, err := range it {
    if err != nil {
        return err // scan or connection error; iteration stops
    }
    fmt.Println(user.Login)
}
```

- `IterErr` returns an `iter.Seq2[*T, error]`: errors while reading rows
  (including `rows.Err()`) are yielded as values, after which the iteration
  stops.
- `Iter` returns a plain `func(yield func(*T) bool)` for the common case.
  Because that signature cannot report errors, a row error makes it
  **panic**.
- Both execute the query when called and hold the `*sql.Rows` until the
  loop finishes or breaks out: always consume (or break out of) the
  iterator, otherwise the connection stays busy. On an in-memory SQLite
  backend (single connection) an unconsumed iterator deadlocks the next query.
- `Fetch` and the other slice-returning functions also check `rows.Err()`
  and return connection failures that happen mid-stream.

### Mapped and grouped results

```go
// map keyed by the string form of a field; last row wins on duplicates
byEmail, err := psql.FetchMapped[User](ctx, nil, "Email")

// map of slices, in query order
byAge, err := psql.FetchGrouped[User](ctx, nil, "Age")
```

The key may be a Go field name or a column name; an unknown key returns
`psql.ErrUnknownField`.

### Scanning arbitrary queries

`psql.RunQueryT[T](ctx, query)`, `psql.RunQueryTOne[T]` (query builder) and
`psql.QT[T](sql, args...).All/Single/Each` (raw SQL) scan result columns
into `T` by column name; columns that match no field are ignored, and
`psql.Table[T]().ScanTo(ctx, rows, &v)` does the same for one row of a
`*sql.Rows` you manage yourself. See [Query Builder](query-builder.md).
