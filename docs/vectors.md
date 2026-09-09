# Vector Support

`psql.Vector` stores float32 vectors and, on PostgreSQL with the
[pgvector](https://github.com/pgvector/pgvector) extension (or CockroachDB's
native vectors), lets you order and filter by distance.

## Defining Vector Columns

```go
type Item struct {
    psql.Name `sql:"items"`
    ID        uint64      `sql:",key=PRIMARY"`
    Title     string      `sql:",type=VARCHAR,size=256"`
    Embedding psql.Vector `sql:",type=VECTOR,size=384"` // 384 dimensions
    Idx       psql.Key    `sql:"embedding_idx,type=VECTOR,fields='Embedding',opclass=vector_cosine_ops"`
}
```

`psql.Vector` is a `[]float32` implementing `sql.Scanner` and
`driver.Valuer`; its text form is `[1,2,3]`. A field of that type without
attributes is inferred as a nullable `VECTOR` column without dimensions.

| Engine | Column type | Distance operators | `VECTOR` keys |
|--------|-------------|--------------------|---------------|
| PostgreSQL / CockroachDB | `vector(N)` (pgvector) | `<->`, `<=>`, `<#>` | `CREATE INDEX ... USING hnsw` (`method=` and `opclass=` attributes) |
| MySQL | `vector(N)` (MySQL 9+) | not supported: the query fails to render | not supported: a `VECTOR` key breaks the automatic `CREATE TABLE` |
| SQLite | `text` | not supported: the query fails to render | ignored |

On MySQL and SQLite, vectors can be stored and read back but not compared
in SQL. Plain CRUD on a vector column works everywhere; the distance
expressions are PostgreSQL-only, and the `VECTOR` key should only be
declared on structs used with PostgreSQL (or SQLite, which skips it).

## Storing and Reading

```go
err := psql.Insert(ctx, &Item{ID: 1, Title: "Example", Embedding: psql.Vector{0.1, 0.2, 0.3}})

item, err := psql.Get[Item](ctx, map[string]any{"ID": uint64(1)})
fmt.Println(item.Embedding.Dimensions(), item.Embedding.String()) // 3 [0.1,0.2,0.3]
```

## Distance Expressions

| Function | pgvector operator | Meaning |
|----------|-------------------|---------|
| `psql.VecL2Distance(field, vec)` | `<->` | Euclidean distance |
| `psql.VecCosineDistance(field, vec)` | `<=>` | cosine distance |
| `psql.VecInnerProduct(field, vec)` | `<#>` | negative inner product |
| `psql.VecEqual(field, vec)`, `psql.VecNotEqual(field, vec)` | `=`, `<>` | exact (in)equality, works on every engine |

`psql.VecOrderBy(field, vec, op)` with `psql.VectorL2`, `psql.VectorCosine`
or `psql.VectorInnerProduct` returns a sort expression (nearest first).

```go
queryVec := psql.Vector{0.1, 0.2, 0.3}

// nearest neighbours
items, err := psql.RunQueryT[Item](ctx, psql.B().Select().From("items").
    OrderBy(psql.VecOrderBy(psql.F("Embedding"), queryVec, psql.VectorCosine)).
    Limit(10))
// SELECT * FROM "items" ORDER BY "Embedding" <=> $1 ASC LIMIT 10

// filter by threshold
close, err := psql.RunQueryT[Item](ctx, psql.B().Select().From("items").
    Where(psql.Lt(psql.VecCosineDistance(psql.F("Embedding"), queryVec), 0.5)))

// distance as a column
dist := psql.VecL2Distance(psql.F("Embedding"), queryVec)
rows, err := psql.B().Select("ID", "Title", dist).From("items").OrderBy(dist).Limit(10).RunQuery(ctx)

// exact match, any engine
same, err := psql.Fetch[Item](ctx, psql.VecEqual(psql.F("Embedding"), queryVec))
```

The vector is passed as a query argument in its `[..]` text form. The
`String()` form of a distance expression (used by `psql.Escape` and when no
engine is known) is a display-only `vec_cosine_distance(field, '[...]')`
call.
