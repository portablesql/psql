# Naming Strategies

A `psql.Namer` maps the names declared in Go to the names used in SQL. It is
configured per backend, so the same struct can be used against databases
with different conventions.

## Setting a Naming Strategy

```go
be, _ := psql.New("postgresql://...")

be.SetNamer(&psql.DefaultNamer{})    // keep Go names
be.SetNamer(&psql.CamelSnakeNamer{}) // Camel_Snake_Case tables and columns
be.SetNamer(&psql.LegacyNamer{})     // default: Camel_Snake_Case tables, columns untouched

// or at construction time
be2 := psql.NewBackend(be.Engine(), be.DB(), psql.WithNamer(&psql.DefaultNamer{}))
```

The namer may be changed at any time; the resolved names are cached per
backend and rebuilt when the namer changes.

## What Gets Transformed

Only names that were *not* given explicitly go through the namer, and each
is transformed exactly once:

| Declaration | Applied method | Explicit name |
|-------------|----------------|---------------|
| Table without `psql.Name` (Go type name) | `TableName(typeName)` | `psql.Name` tag value, used as-is |
| Column without a name in its `sql` tag (Go field name) | `ColumnName(typeName, fieldName)` | first element of the `sql` tag, used as-is |

Key names (`key=name`, `psql.Key` tag), join table names in `many_to_many`
tags and the strings you pass to the query builder are never transformed.
The `psql.Table[T]()` metadata always reports the declared names;
`psql.Table[T]().FormattedName(be)` returns the table name used on a given
backend.

## Available Namers

Given this struct:

```go
type UserProfile struct {
    UserId      int64  `sql:",key=PRIMARY"`
    DisplayName string `sql:",type=VARCHAR,size=64"`
    AvatarURL   string `sql:"avatar_url,type=VARCHAR,size=255"`
}
```

| Namer | Table | `UserId` | `DisplayName` | `AvatarURL` (explicit) |
|-------|-------|----------|---------------|------------------------|
| `LegacyNamer` (default) | `User_Profile` | `UserId` | `DisplayName` | `avatar_url` |
| `DefaultNamer` | `UserProfile` | `UserId` | `DisplayName` | `avatar_url` |
| `CamelSnakeNamer` | `User_Profile` | `User_Id` | `Display_Name` | `avatar_url` |

`Camel_Snake_Case` inserts an underscore before every upper-case letter
except the first, drops characters that are neither letters nor digits, and
upper-cases the first letter: `HelloWorld` becomes `Hello_World`, `ABC`
becomes `A_B_C`, `Table1` stays `Table1`.

`LegacyNamer` uses the `psql.FormatTableName` variable for tables, so code
that overrides that variable keeps working. Explicit column names in tags
are the only way to get lower_snake_case columns with the built-in namers;
implement your own `Namer` for a global convention.

## Namer Interface

```go
type Namer interface {
    TableName(table string) string
    SchemaName(table string) string
    ColumnName(table, column string) string
    JoinTableName(joinTable string) string
    CheckerName(table, column string) string
    IndexName(table, column string) string
    UniqueName(table, column string) string
    EnumTypeName(table, column string) string
}
```

`TableName` and `ColumnName` are the methods used by the core; the others
exist for dialects and custom tooling (the built-in dialects name indexes
`<table>_<key>` and enum constraints `chk_enum_<hash>` regardless of the
namer). Stateless namers (empty structs) are compared by type when caching;
namers with fields are compared with `==`.
