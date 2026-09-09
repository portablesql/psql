# Getting Started

## Installation

psql requires Go 1.24 or later. The core library and the database drivers are
separate modules; install the core plus the driver(s) you need:

```bash
go get github.com/portablesql/psql
go get github.com/portablesql/psql-mysql    # MySQL / MariaDB
go get github.com/portablesql/psql-pgsql    # PostgreSQL / CockroachDB
go get github.com/portablesql/psql-sqlite   # SQLite (modernc.org/sqlite, no cgo)
```

## Connecting

Import the driver module for its side effects; it registers a dialect and a
DSN matcher. `psql.New(dsn)` then picks the engine from the DSN:

```go
import (
    "github.com/portablesql/psql"
    _ "github.com/portablesql/psql-sqlite"
)

be, err := psql.New(":memory:")
```

| Engine | Driver module | DSN examples |
|--------|---------------|--------------|
| MySQL / MariaDB | `psql-mysql` | `user:pass@tcp(localhost:3306)/mydb` (go-sql-driver format) |
| PostgreSQL / CockroachDB | `psql-pgsql` | `postgresql://user:pass@localhost:5432/mydb`, `postgres://...`, `host=... dbname=...` |
| SQLite | `psql-sqlite` | `:memory:`, `sqlite:mydata.db`, `file:test.db?...`, any path ending in `.db`, `.sqlite` or `.sqlite3` |

If no imported driver matches, `psql.New` returns an error asking whether a
driver was imported.

### Engine-specific constructors

Each driver module also exposes a `New` function taking the native driver
configuration, for settings that do not fit in a DSN:

```go
import (
    "github.com/go-sql-driver/mysql"
    "github.com/jackc/pgx/v5/pgxpool"

    psqlmysql "github.com/portablesql/psql-mysql"
    psqlpg "github.com/portablesql/psql-pgsql"
    psqlsqlite "github.com/portablesql/psql-sqlite"
)

cfg, _ := mysql.ParseDSN("user:pass@tcp(localhost:3306)/mydb")
cfg.Params = map[string]string{"sql_mode": "'ANSI,NO_BACKSLASH_ESCAPES,STRICT_TRANS_TABLES'"}
be, err := psqlmysql.New(cfg)

pgcfg, _ := pgxpool.ParseConfig("postgresql://user:pass@localhost:5432/mydb")
pgcfg.MaxConns = 16
be, err = psqlpg.New(pgcfg)

be, err = psqlsqlite.New("file:/var/lib/app/data.db")
```

`psqlmysql.InitCfg(cfg)` and `psql.Init(dsn)` create a backend and store it
in `psql.DefaultBackend`, which is used by operations whose context carries
no backend.

MySQL connections get `charset=utf8mb4` and
`sql_mode='ANSI,NO_BACKSLASH_ESCAPES'` unless you set them: psql relies on
ANSI double-quoted identifiers and standard string escaping. Because a
session `sql_mode` replaces the server default, add `STRICT_TRANS_TABLES`
yourself (as above) if you want strict mode.

## Attaching to Context

Every operation finds its database through the `context.Context`:

```go
ctx := be.Plug(context.Background())
// equivalent:
ctx = psql.ContextBackend(context.Background(), be)
```

A context can also carry a `*sql.DB` (`psql.ContextDB`), a single
`*sql.Conn` (`psql.ContextConn`) or a transaction (`psql.ContextTx`, see
[Transactions](transactions.md)); operations use the innermost one.
`psql.GetBackend(ctx)` returns the backend found in the context, or
`psql.DefaultBackend`.

## Defining a Table and Using It

```go
type User struct {
    psql.Name `sql:"users"`
    ID        uint64 `sql:",key=PRIMARY"`
    Email     string `sql:",type=VARCHAR,size=255,key=UNIQUE:email"`
    Login     string `sql:",type=VARCHAR,size=128"`
    Age       int64
}

err := psql.Insert(ctx, &User{ID: 1, Email: "alice@example.com", Login: "Alice", Age: 30})

user, err := psql.Get[User](ctx, map[string]any{"ID": uint64(1)})

user.Login = "Alice Smith"
err = psql.Update(ctx, user) // writes only the changed columns

users, err := psql.Fetch[User](ctx, map[string]any{"Age": 30}, psql.Sort(psql.S("Login", "ASC")))

n, err := psql.Count[User](ctx, nil)

_, err = psql.Delete[User](ctx, map[string]any{"ID": uint64(1)})
```

The table is created (or missing columns and indexes are added) the first
time it is used on a backend. The full tag syntax, the type rules (`string`
and `int` need an explicit `type=`; `int64`, `uint64`, `float64`, `bool`,
`time.Time`, `[]byte` are inferred) and the schema management options are in
[Object Binding](object-binding.md).

## Errors

- A `Get`, `FetchOne`, `RunQueryTOne`, `QT(...).Single` or
  `Future.Resolve` that finds no row returns `os.ErrNotExist`
  (`errors.Is(err, os.ErrNotExist)`, or `psql.IsNotExist(err)`).
- `psql.IsDuplicate(err)` is true for a unique key violation on every engine
  (MySQL error 1062, PostgreSQL SQLSTATE 23505, SQLite "UNIQUE constraint
  failed"), looking through wrapped and joined errors.
- `psql.IsNotExist(err)` is true for `os.ErrNotExist`/`fs.ErrNotExist`, for
  MySQL "unknown database/table/column/key" errors (1049, 1051, 1054, 1091,
  1109, 1146, 1176) and for PostgreSQL `undefined_table` (42P01) and
  `undefined_column` (42703). The SQLite driver has no error classifier, so
  a SQLite "no such table" error is not recognized.
- `psql.ErrorNumber(err)` returns the MySQL error number, `0` for a nil
  error and `0xffff` when there is none (always the case on PostgreSQL:
  use `pgsql.SQLState(err)` there).
- Failed statements issued by the ORM (`Insert`, `InsertIgnore`, `Replace`,
  `Update`, `Delete`, `DeleteOne`, `Restore`) and by
  `QueryBuilder.RunQuery` (hence `Get`, `Fetch`, `Iter`, `Count`, ...) are
  returned as a `*psql.Error` carrying the rendered query; `errors.As(err,
  &perr)` gives access to `perr.Query`, and `errors.Is`/`Unwrap` reach the
  driver error. Raw SQL helpers (`psql.Q(...).Exec/Each`, `psql.QT`,
  `QueryBuilder.ExecQuery`, `psql.ExecContext`) return the driver error
  unchanged.

Sentinel errors: `psql.ErrNotReady` (nil table/backend, or `Restore` on a
table without soft delete, or a `Future` resolved without a backend),
`psql.ErrNotNillable`, `psql.ErrTxAlreadyProcessed`, `psql.ErrDeleteBadAssert`,
`psql.ErrBreakLoop` (return it from an `Each` callback to stop early),
`psql.ErrUnknownField` (bad key in `FetchMapped`/`FetchGrouped`) and
`psql.ErrInvalidEnumValue` (from `psql.ValidateEnum`).

## Production Notes

### Thread safety

`*psql.Backend` and the `*psql.TableMeta[T]` returned by `psql.Table[T]()`
are safe for concurrent use; table registration and the first-use schema
check are serialized internally. A `*psql.QueryBuilder`, a `*psql.TxProxy`
and the row objects you fetch are not: do not share them between goroutines
without your own synchronization.

### Connection pooling

- MySQL and PostgreSQL backends are created with `psql.WithPoolDefaults`:
  128 max open connections, 32 idle, 3 minute max lifetime. Adjust through
  `be.DB().SetMaxOpenConns(...)` and friends, or configure the pool in the
  native config (`pgxpool.Config.MaxConns` for PostgreSQL).
- SQLite file databases use a pool of 8 connections opened in WAL mode with
  `foreign_keys=ON`, `_txlock=immediate` and a 10 second `busy_timeout`
  (DSN `_pragma` parameters you set yourself take precedence).
- SQLite `:memory:` databases are private to one connection, so the pool is
  limited to a single connection. Any query issued while a `*sql.Rows` (or
  an unconsumed iterator) is still open, or issued outside a transaction
  while one is active, waits forever for that connection.

### Logging

`psql.SetLogger(l)` installs a debug logger (anything with
`DebugContext`, such as `slog.Default().With("component", "psql")`) that
receives every statement together with its arguments, plus a stack trace for
each failed query. Arguments can contain sensitive data, so enable it
deliberately. Independently of that logger, every failed query produces one
`slog.Error` line on the default `slog` logger (query text and error, with
`event` and `psql.table` attributes) and schema check failures are logged
the same way.

### Deprecated functions

`psql.Exec`, `psql.Query` and `psql.QueryContext` run raw queries against
`psql.DefaultBackend`; use `psql.Q(...).Exec(ctx)` and `psql.Q(...).Each(ctx,
cb)` instead. `Dialect.LimitOffset` is no longer called by the query builder.

## Next Steps

- [Object Binding](object-binding.md) - Struct tags, types, keys, schema management, CRUD
- [Query Builder](query-builder.md) - Building SQL queries
- [Hooks](hooks.md) - Lifecycle callbacks
- [Associations](associations.md) - Relationships and preloading
- [Transactions](transactions.md) - Transactions and savepoints
- [Soft Delete](soft-delete.md) - Soft delete, restore, force delete
- [Scopes & Lazy](scopes-lazy.md) - Reusable query modifiers and batched lazy loading
- [Vectors](vectors.md) - Vector columns and similarity search
- [Naming Strategies](naming-strategies.md) - Table and column naming
