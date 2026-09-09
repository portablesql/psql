# Query Builder

The query builder constructs SELECT, INSERT, UPDATE, DELETE and REPLACE
statements from Go values and renders them for the engine of the backend in
the context, with parameterized arguments. The generic CRUD functions use it
internally, and their `where` arguments accept everything described here.

## Basic Usage

```go
// SELECT "name","email" FROM "users"
q := psql.B().Select("name", "email").From("users")

// SELECT * FROM "users" WHERE ("status"=?)
q = psql.B().Select().From("users").Where(map[string]any{"status": "active"})

// UPDATE "users" SET "status"=? WHERE ("id"=?)
q = psql.B().Update("users").Set(map[string]any{"status": "inactive"}).Where(map[string]any{"id": 123})

// INSERT INTO "users" ... (SET syntax on MySQL, (cols) VALUES (...) elsewhere)
q = psql.B().Insert().Table("users").Set(map[string]any{"id": 1, "name": "Alice"})

// DELETE FROM "users" WHERE ("id"=?)
q = psql.B().Delete().From("users").Where(map[string]any{"id": 123})
```

Tables are given as strings to `From`, `Table`, `Update`, `InsertSelect` and
the join methods; a string may carry an alias (`"users AS u"` or `"users u"`),
rendered as `"users" AS "u"`. `Into` and `Replace` take a
`psql.EscapeTableable` (such as `psql.SubTable`, or your own implementation
of `EscapeTable() string`), not a string: use `.Table("users")` for a plain
table name.

## Executing Queries

```go
rows, err := q.RunQuery(ctx)   // SELECT: *sql.Rows, caller must Close()
res, err := q.ExecQuery(ctx)   // INSERT/UPDATE/DELETE: sql.Result
stmt, err := q.Prepare(ctx)    // prepared statement, caller must Close()

users, err := psql.RunQueryT[User](ctx, q)    // scan every row into []*User
user, err := psql.RunQueryTOne[User](ctx, q)  // first row, or os.ErrNotExist

sql, args, err := q.RenderArgs(ctx) // parameterized SQL + arguments
sql, err = q.Render(ctx)            // SQL with values embedded as literals
```

`RenderArgs` is what every execution path uses: values become `?`
placeholders (MySQL, SQLite) or `$1, $2, ...` (PostgreSQL) and are passed to
the driver. `Render` embeds the values as engine-aware literals and is meant
for logging and debugging. Both return an error if the query cannot be
rendered (bare string condition, unsupported operator, vector operator on an
engine without vector support, ...). `RunQuery` failures are returned as a
`*psql.Error` carrying the rendered query.

## Helper Functions

| Helper | Meaning |
|--------|---------|
| `psql.F("col")`, `psql.F("t.col")`, `psql.F("t", "col")`, `psql.F("t.*")` | Field reference, quoted |
| `psql.S("col", "DESC")`, `psql.S("t", "col", "ASC")` | Sort field for `OrderBy` and `psql.Sort` (direction optional) |
| `psql.V(value)` | Force a value to be treated as a literal, never as an expression or list |
| `psql.Raw("COUNT(*)")` | Raw SQL, inserted verbatim (never with user input) |
| `psql.Now()`, `psql.DateAdd(expr, d)`, `psql.DateSub(expr, d)` | Portable timestamp expressions |
| `psql.Coalesce(a, b, ...)`, `psql.Greatest(...)`, `psql.Least(...)` | Functions (`GREATEST`/`LEAST` become `MAX`/`MIN` on SQLite) |
| `psql.Case()...`, `psql.Case(operand)...` | `CASE WHEN ... THEN ... ELSE ... END` |
| `psql.Exists(sub)`, `psql.NotExists(sub)` | `EXISTS (subquery)` |
| `psql.SubTable(sub, "alias")` | Derived table for `From`/`Join` |
| `psql.Escape(v)` | Render any value as an engine-neutral SQL literal |

## WHERE Conditions

`Where` accepts any number of conditions, joined with `AND`. Each condition
is a `map[string]any`, a comparison expression, a `psql.WhereOR` /
`psql.WhereAND`, or a `psql.Raw`. **Bare strings are rejected** (the query
fails to render) because they would otherwise be bound as values; wrap raw
SQL in `psql.Raw`.

### Map syntax

Keys are column names, values decide the operator:

```go
psql.B().Select().From("users").Where(map[string]any{
    "id":         123,                                   // "id"=?
    "deleted_at": nil,                                   // "deleted_at" IS NULL
    "role":       []string{"admin", "staff"},            // "role" IN(?,?)   (any slice)
    "status":     psql.WhereOR{"active", "pending"},     // ("status"=? OR "status"=?)
    "age":        map[string]any{"$gte": 18, "$lt": 65}, // ("age">=? AND "age"<?)
    "name":       psql.Like{Like: "Jo%"},                // "name" LIKE ? ESCAPE '\'
    "email":      psql.Like{Like: "%@example.com", CaseInsensitive: true},
    "tags":       &psql.FindInSet{Value: "go"},          // element of a comma-separated list
    "score":      &psql.Not{V: nil},                     // "score" IS NOT NULL
    "team":       &psql.Not{V: []int{1, 2}},             // "team" NOT IN(?,?)
    "owner":      &psql.SubIn{Sub: psql.B().Select("user_id").From("orders")}, // IN (SELECT ...)
    "region":     &psql.Any{Values: []string{"eu", "us"}},                      // = ANY($n) on PostgreSQL, IN(...) elsewhere
})
```

Several conditions on the same column combine with `psql.WhereAND{...}` or
`psql.WhereOR{...}` as the value. An empty slice, `WhereOR` or `Any` never
matches (`FALSE`); an empty `WhereAND` or empty map matches everything.
Values implementing `driver.Valuer` (`psql.Set`, `psql.Vector`, `psql.Hex`,
`psql.V(...)`) and `[]byte` are always compared as a single value, even
though they are slices.

Map keys are rendered in sorted order so that identical maps produce
identical SQL (prepared statement caches hit).

### Expressions

```go
psql.Equal(psql.F("status"), "active")          // "status"=?
psql.Equal(psql.F("manager_id"), nil)           // "manager_id" IS NULL
psql.Gt(psql.F("age"), 18)                      // "age">?     (Gte, Lt, Lte likewise)
psql.Between(psql.F("age"), 18, 65)             // "age" BETWEEN ? AND ?
psql.CILike(psql.F("name"), "john%")            // ILIKE / LOWER() LIKE LOWER() / LIKE per engine
&psql.Like{Field: psql.F("name"), Like: "John%"}
&psql.FindInSet{Field: psql.F("tags"), Value: "go"}
&psql.Not{V: psql.Equal(psql.F("a"), 1)}        // NOT ("a"=?)
psql.Raw(`"a" = "b" + 1`)                       // raw SQL
```

Multiple top-level conditions:

```go
psql.B().Select().From("users").Where(
    psql.Equal(psql.F("status"), "active"),
    psql.Gte(psql.F("age"), 18),
    psql.WhereOR{
        map[string]any{"role": "admin"},
        psql.Equal(psql.F("verified"), true),
    },
)
// WHERE ("status"=?) AND ("age">=?) AND (("role"=?) OR ("verified"=?))
```

Case-insensitive `LIKE` renders as `ILIKE` on PostgreSQL, `LOWER(f) LIKE
LOWER(p)` on SQLite and a plain `LIKE` on MySQL (whose default collations
are case-insensitive). Patterns use `\` as the escape character.

### Subqueries

```go
// IN (SELECT ...)
psql.B().Select().From("users").Where(map[string]any{
    "id": &psql.SubIn{Sub: psql.B().Select("user_id").From("orders")},
})

// EXISTS (SELECT 1 FROM "orders" WHERE "orders"."user_id"="users"."id")
psql.B().Select().From("users").Where(
    psql.Exists(psql.B().Select(psql.Raw("1")).From("orders").Where(
        psql.Equal(psql.F("orders.user_id"), psql.F("users.id")),
    )),
)

// scalar subquery in SELECT
psql.B().Select("id", psql.Coalesce(
    psql.B().Select(psql.Raw("COUNT(*)")).From("orders").Where(psql.Equal(psql.F("orders.user_id"), psql.F("users.id"))),
    0,
)).From("users")
```

Subqueries share the parent's argument list, so placeholders are numbered
correctly on PostgreSQL.

## ORDER BY, LIMIT and OFFSET

```go
psql.B().Select().From("users").
    OrderBy(psql.S("created_at", "DESC"), psql.S("name", "ASC")).
    Limit(10)
// ... ORDER BY "created_at" DESC,"name" ASC LIMIT 10

psql.B().Select().From("users").OrderBy(psql.S("id")).Limit(20, 10)
// ... ORDER BY "id" LIMIT 10 OFFSET 20
```

`Limit(count)` renders `LIMIT count`. `Limit(offset, count)` follows the
MySQL argument order (offset first, like `psql.LimitFrom(offset, count)`) and
renders `LIMIT count OFFSET offset` on every engine. `Limit()` with no
argument clears the clause. PostgreSQL does not support `LIMIT` on `DELETE`
and `UPDATE`; rendering such a query returns an error.

## JOINs

```go
psql.B().
    Select(psql.F("u", "name"), psql.F("o", "total")).
    From("users AS u").
    InnerJoin("orders o", psql.Equal(psql.F("u.id"), psql.F("o.user_id"))).
    LeftJoin("profiles", psql.Equal(psql.F("u.id"), psql.F("profiles.user_id"))).
    RightJoin("teams", map[string]any{"teams.id": psql.Raw(`"u"."team_id"`)})
```

`Join(joinType, table, conditions...)` is the general form; conditions
accept the same values as `Where` and are joined with `AND`. Join a derived
table with `psql.SubTable`:

```go
psql.B().Select("u.id", "vc.vote_count").From("users AS u").LeftJoin(
    psql.SubTable(
        psql.B().Select("user_id", psql.Raw("COUNT(*) AS vote_count")).From("votes").GroupByFields("user_id"),
        "vc",
    ),
    psql.Equal(psql.F("u.id"), psql.F("vc.user_id")),
)
```

## GROUP BY, HAVING, DISTINCT

```go
psql.B().
    Select("status", psql.Raw("COUNT(*) AS cnt")).
    From("users").
    GroupByFields("status").
    Having(psql.Gt(psql.Raw("COUNT(*)"), 5))

psql.B().Select("name").From("users").SetDistinct() // SELECT DISTINCT "name" ...
```

## Locking

```go
psql.B().Select().From("jobs").Where(map[string]any{"state": "queued"}).SetForUpdate()   // FOR UPDATE
psql.B().Select().From("jobs").Where(map[string]any{"state": "queued"}).SetSkipLocked()  // FOR UPDATE SKIP LOCKED
psql.B().Select().From("jobs").Where(map[string]any{"state": "queued"}).SetNoWait()      // FOR UPDATE NOWAIT
```

`FOR UPDATE` is silently omitted on SQLite, which locks at the database
level. The fetch options `psql.FetchLock`, `psql.FetchLockSkipLocked` and
`psql.FetchLockNoWait` set the same flags.

## INSERT and Upserts

```go
// INSERT ... ON CONFLICT DO NOTHING / INSERT IGNORE / INSERT OR IGNORE
psql.B().Insert().Table("users").
    Set(map[string]any{"id": 1, "name": "Alice"}).
    DoNothing()

// PostgreSQL/SQLite: INSERT ... ON CONFLICT ("id") DO UPDATE SET "name"=?
// MySQL:             INSERT ... ON DUPLICATE KEY UPDATE "name"=?
psql.B().Insert().Table("users").
    Set(map[string]any{"id": 1, "name": "Alice"}).
    OnConflict("id").
    DoUpdate(map[string]any{"name": "Alice"})

// INSERT INTO "archive" SELECT "id","name" FROM "users" WHERE ("active"=?)
psql.B().InsertSelect("archive").Select("id", "name").From("users").
    Where(map[string]any{"active": false})
```

`DoUpdate` requires `OnConflict` columns on PostgreSQL and SQLite (rendering
fails without them); MySQL ignores them.

## SET Expressions

`Set` (and `DoUpdate`) take `map[string]any` entries or `psql.Raw`
expressions; bare strings are rejected. Values are always bound as a single
value (slices and `driver.Valuer`s included), and three wrappers expand
into expressions:

```go
psql.B().Update("counters").
    Set(map[string]any{
        "views":     psql.Incr(1),                       // "views"="views"+(?)
        "stock":     psql.Decr(1),                       // "stock"="stock"-(?)
        "last_seen": &psql.SetRaw{SQL: "NOW()"},         // "last_seen"=NOW()
        "expires":   psql.DateAdd(psql.Now(), time.Hour), // engine-specific interval
    }).
    Where(map[string]any{"id": 42})
```

## Portable Timestamp Arithmetic

`psql.Now()`, `psql.DateAdd(expr, d)` and `psql.DateSub(expr, d)` render
per engine:

```go
psql.DateSub(psql.Now(), 24*time.Hour)
// MySQL:      NOW() - INTERVAL 1 DAY
// PostgreSQL: NOW() - INTERVAL '1 day'
// SQLite:     datetime(CURRENT_TIMESTAMP,'-1 days')

psql.DateAdd(psql.F("created_at"), 90*time.Minute)
// MySQL:      "created_at" + INTERVAL 90 MINUTE
// PostgreSQL: "created_at" + INTERVAL '90 minute'
// SQLite:     datetime("created_at",'+90 minutes')
```

The duration is expressed in the largest unit that divides it exactly
(day, hour, minute, second), or in microseconds below one second.

```go
// rows created in the last 24 hours
psql.B().Select().From("events").
    Where(psql.Gt(psql.F("created_at"), psql.DateSub(psql.Now(), 24*time.Hour)))
```

## Scopes

```go
var Active psql.Scope = func(q *psql.QueryBuilder) *psql.QueryBuilder {
    return q.Where(map[string]any{"status": "active"})
}

q := psql.B().Select().From("users").Apply(Active)
```

See [Scopes & Lazy](scopes-lazy.md).

## Raw SQL

```go
err := psql.Q(`DROP TABLE IF EXISTS "old_table"`).Exec(ctx)

err = psql.Q(`SELECT "id","name" FROM "users" WHERE "age" > ?`, 18).Each(ctx, func(rows *sql.Rows) error {
    var id int64
    var name string
    if err := rows.Scan(&id, &name); err != nil {
        return err
    }
    if id > 100 {
        return psql.ErrBreakLoop // stop without error
    }
    return nil
})

users, err := psql.QT[User](`SELECT * FROM "users" WHERE "age" > ?`, 18).All(ctx)
user, err := psql.QT[User](`SELECT * FROM "users" WHERE "id" = ?`, 1).Single(ctx) // os.ErrNotExist if none
```

Raw queries run against whatever the context carries (transaction,
connection or backend) and must use the engine's placeholder style
(`?` or `$1`). `psql.ExecContext(ctx, sql, args...)` is the lowest-level
equivalent.
