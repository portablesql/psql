# CLAUDE.md - Development Guidelines

## Layout
- This module (`github.com/portablesql/psql`) is the engine-independent core: object binding, query builder, hooks, associations, transactions.
- Database drivers are sibling modules in the parent directory, tied together by the `go.work` at the project root:
  `../psql-mysql` (go-sql-driver/mysql), `../psql-pgsql` (pgx/v5), `../psql-sqlite` (modernc.org/sqlite). Each registers a `Dialect` and a `BackendFactory` in `init()` and defines engine-specific magic types (`DATETIME`, `JSON`).
- `../psql-test` holds the integration tests (package `ptest`) that need a real database; `getTestBackend(t)` returns a backend from `PSQL_TEST_DSN`, defaulting to in-memory SQLite.
- Documentation: `README.md` (overview), `doc.go` (package overview), `docs/*.md` (reference). Every behavior is documented in exactly one of these; keep them in sync with the code.

## Build & Test Commands
- `make test`: `go test -race ./...` in this module; `make fmt`, `make vet`, `make lint` (staticcheck when installed), `make deps`.
- Single test: `go test -race -run TestName ./...`.
- Whole workspace (core + drivers + integration tests on SQLite): `cd .. && go test ./...`.
- Integration tests against another engine: `cd ../psql-test && PSQL_TEST_DSN='user:pass@tcp(127.0.0.1:3306)/testdb' go test ./...` (MySQL) or `PSQL_TEST_DSN='postgresql://user:pass@localhost:5432/testdb' go test ./...` (PostgreSQL / CockroachDB). Tests create and drop their own tables; run them against a scratch database.

## Code Style & Conventions
- Go 1.24+ (generics, range iterators, `weak` pointers, `reflect.TypeFor`).
- Package name `psql`, tests in the separate `psql_test` package; integration tests in `psql-test` only.
- Query failures are returned as `*Error` (wrapping the driver error, with the rendered query); sentinel errors are package-level `Err*` vars; helpers such as `IsNotExist`/`IsDuplicate` delegate to the dialects.
- Object binding: structs with `sql` tags (`sql:",key=PRIMARY"`, `sql:"Name,type=VARCHAR,size=64"`, quoted lists such as `values='a,b'`). Only `int64`, `uint64`, `float64`, `bool`, `time.Time`, `[]byte`, `psql.Set`, `psql.Vector` (and pointers) infer a column type; other fields need `type=` or `import=`. Scannable field kinds: `sql.Scanner` implementations, `time.Time`, `bool`, `string`, int/uint kinds, `float32/64`, `[]byte`, pointers to these, and `format=json` fields.
- A table whose `psql.Name` tag carries `check=0` is never created or altered by the automatic schema check (used for system views and existing tables).
- Query builder: start with `psql.B()` then `.Select()`, `.From()`, `.Where()`...; fields are `psql.F("Name")`, values `psql.V(...)`, raw SQL `psql.Raw(...)`; bare strings are not accepted as conditions. SQL functions are struct types (`Like`, `FindInSet`, `Comparison`, ...) implementing `EscapeValueable`, with an `escapeValueCtx` method for engine-aware, parameterized rendering.
- Engine-specific SQL belongs in the driver modules through the optional dialect interfaces (`TypeMapper`, `KeyRenderer`, `UpsertRenderer`, `SchemaChecker`, `ErrorClassifier`, `VectorRenderer`); the core only switches on `Engine` for rendering details.
