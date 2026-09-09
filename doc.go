// Package psql is a portable SQL library for Go: object binding through
// struct tags, a query builder rendering engine-specific SQL, lifecycle
// hooks, associations, soft delete, transactions with savepoints and vector
// search, on MySQL/MariaDB, PostgreSQL/CockroachDB and SQLite.
//
// The database drivers live in separate modules, imported for their side
// effects; [New] then detects the engine from the DSN and [Backend.Plug]
// attaches the backend to a context that every operation takes:
//
//	import (
//	    "github.com/portablesql/psql"
//	    _ "github.com/portablesql/psql-sqlite" // or psql-mysql, psql-pgsql
//	)
//
//	be, err := psql.New(":memory:")
//	ctx := be.Plug(context.Background())
//
// Tables are Go structs. Column types are inferred for int64, uint64,
// float64, bool, time.Time, []byte and the psql types; other fields need an
// explicit type. The table is created, or missing columns and keys added,
// the first time it is used on a backend (see [WithSchemaCheck] and
// [Backend.CheckStructure] to control this):
//
//	type User struct {
//	    psql.Name `sql:"users"`
//	    ID        uint64 `sql:",key=PRIMARY"`
//	    Email     string `sql:",type=VARCHAR,size=255,key=UNIQUE:email"`
//	    Login     string `sql:",type=VARCHAR,size=128"`
//	}
//
//	err = psql.Insert(ctx, &User{ID: 1, Email: "alice@example.com", Login: "Alice"})
//	user, err := psql.Get[User](ctx, map[string]any{"ID": uint64(1)}) // os.ErrNotExist if none
//	user.Login = "Alice Smith"
//	err = psql.Update(ctx, user) // writes only the changed columns
//	users, err := psql.Fetch[User](ctx, nil, psql.Sort(psql.S("Login", "ASC")), psql.LimitFrom(20, 10))
//	_, err = psql.Delete[User](ctx, map[string]any{"ID": uint64(1)})
//
// The where argument of these functions accepts the same conditions as
// [QueryBuilder.Where]; [B] starts a query builder for anything else, and
// [Q] / [QT] run raw SQL.
//
// Beyond the portable core, the builder renders RETURNING, common table
// expressions ([QueryBuilder.With]), multi-row inserts
// ([QueryBuilder.InsertRows]), row lock modes ([QueryBuilder.SetLockMode]),
// DISTINCT ON, CockroachDB's AS OF SYSTEM TIME and EXPLAIN, plus JSON
// ([JSONGet], [JSONContains], [JSONSet]) and full-text ([FullText])
// expressions. Objects gain identity columns (the autoinc tag attribute),
// [BulkInsert], named locks ([NamedLock]) and transactions with options and
// automatic retries ([TxWithOptions]). [Backend.Variant] names the product
// behind the engine (PostgreSQL or CockroachDB, MySQL or MariaDB) and
// [Backend.Supports] reports which Feature* constants it has; a feature the
// product lacks fails with an error wrapping [ErrNotSupported]. The
// per-product feature matrix is in docs/advanced.md.
//
// The detailed reference lives in the docs directory of the repository
// (https://github.com/portablesql/psql/tree/master/docs): getting-started.md
// (drivers, DSNs, errors, pooling, logging, thread safety), object-binding.md
// (tags, types, keys, schema management, fetch options and iterators),
// query-builder.md, advanced.md, hooks.md, associations.md,
// transactions.md, soft-delete.md, scopes-lazy.md, vectors.md and
// naming-strategies.md.
package psql
