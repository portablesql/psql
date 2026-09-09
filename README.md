[![Go Reference](https://pkg.go.dev/badge/github.com/portablesql/psql.svg)](https://pkg.go.dev/github.com/portablesql/psql)
[![Build Status](https://github.com/portablesql/psql/actions/workflows/test.yml/badge.svg)](https://github.com/portablesql/psql/actions/workflows/test.yml)
[![Coverage Status](https://coveralls.io/repos/github/portablesql/psql/badge.svg?branch=master)](https://coveralls.io/github/portablesql/psql?branch=master)

# psql

Portable SQL library for Go: object binding through struct tags, a query
builder that renders engine-specific SQL, lifecycle hooks, associations,
soft delete, transactions with savepoints and vector search. One code base
runs on MySQL/MariaDB, PostgreSQL/CockroachDB and SQLite.

Similar in scope to GORM, built on generics and range iterators with a
smaller footprint. Requires Go 1.24+.

## Quick Start

```go
package main

import (
    "context"
    "fmt"

    "github.com/portablesql/psql"
    _ "github.com/portablesql/psql-sqlite" // or psql-mysql, psql-pgsql
)

type User struct {
    psql.Name `sql:"users"`
    ID        uint64 `sql:",key=PRIMARY"`
    Email     string `sql:",type=VARCHAR,size=255,key=UNIQUE:email"`
    Login     string `sql:",type=VARCHAR,size=128"`
}

func main() {
    be, err := psql.New(":memory:") // engine detected from the DSN
    if err != nil {
        panic(err)
    }
    ctx := be.Plug(context.Background())

    // the table is created on first use
    if err := psql.Insert(ctx, &User{ID: 1, Login: "Alice", Email: "alice@example.com"}); err != nil {
        panic(err)
    }

    user, err := psql.Get[User](ctx, map[string]any{"ID": uint64(1)})
    if err != nil {
        panic(err)
    }
    user.Login = "Alice Smith"
    if err := psql.Update(ctx, user); err != nil { // writes only the changed column
        panic(err)
    }

    users, _ := psql.Fetch[User](ctx, nil, psql.Sort(psql.S("Login", "ASC")), psql.Limit(10))
    fmt.Println(len(users), users[0].Login)
}
```

The database drivers are separate modules; import the one you need with a
blank identifier:

```go
import _ "github.com/portablesql/psql-mysql"   // MySQL / MariaDB
import _ "github.com/portablesql/psql-pgsql"   // PostgreSQL / CockroachDB
import _ "github.com/portablesql/psql-sqlite"  // SQLite (pure Go)
```

## Features

- **Object binding**: `sql` struct tags declare columns, keys and indexes;
  tables are created and missing columns added on first use (or explicitly,
  or never). Generic `Insert`, `Get`, `Fetch`, `Update`, `Replace`, `Delete`,
  `Count`, iterators, change tracking. See [Object Binding](docs/object-binding.md).
- **Query builder**: `psql.B().Select().From().Where()...` with map or
  expression conditions, joins, subqueries, upserts, locking and portable
  date arithmetic, rendered with placeholders for the target engine. See
  [Query Builder](docs/query-builder.md).
- **Hooks**: `BeforeSave`, `AfterInsert`, `AfterScan`, ... as methods on
  your types. See [Hooks](docs/hooks.md).
- **Associations**: `belongs_to`, `has_one`, `has_many`, `many_to_many`
  with batched preloading. See [Associations](docs/associations.md).
- **Transactions**: context-based, nested through savepoints, with an
  escape hatch for audit logs. See [Transactions](docs/transactions.md).
- **Soft delete**: a `DeletedAt *time.Time` field turns `Delete` into an
  update and filters reads. See [Soft Delete](docs/soft-delete.md).
- **Scopes and lazy loading**: reusable query modifiers, and futures that
  resolve many lookups with one `IN` query. See [Scopes & Lazy](docs/scopes-lazy.md).
- **Vectors**: `psql.Vector` columns and pgvector distance operators on
  PostgreSQL. See [Vectors](docs/vectors.md).
- **Naming strategies**: Go names, `Camel_Snake_Case`, or your own. See
  [Naming Strategies](docs/naming-strategies.md).

## Documentation

| Topic | Description |
|-------|-------------|
| [Getting Started](docs/getting-started.md) | Installation, drivers and DSNs, context, errors, pooling, logging, thread safety |
| [Object Binding](docs/object-binding.md) | Struct tags, column types, keys, enums, schema management, CRUD and fetch options |
| [Query Builder](docs/query-builder.md) | SELECT, WHERE, JOIN, GROUP BY, subqueries, upserts, raw SQL |
| [Hooks](docs/hooks.md) | Lifecycle callbacks and their order |
| [Associations](docs/associations.md) | belongs_to, has_one, has_many, many_to_many, preloading |
| [Transactions](docs/transactions.md) | Transactions, savepoints, running queries outside a transaction |
| [Soft Delete](docs/soft-delete.md) | Soft delete, restore, force delete |
| [Scopes & Lazy](docs/scopes-lazy.md) | Scopes, lazy futures and batches, change detection |
| [Vectors](docs/vectors.md) | Vector columns and similarity search |
| [Naming Strategies](docs/naming-strategies.md) | LegacyNamer, DefaultNamer, CamelSnakeNamer |

Package documentation: [pkg.go.dev/github.com/portablesql/psql](https://pkg.go.dev/github.com/portablesql/psql).
