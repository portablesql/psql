package psql

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// This file holds the advanced, PostgreSQL / CockroachDB oriented features
// of the query builder: RETURNING, upsert extensions, CTEs, DISTINCT ON,
// row locking modes, AS OF SYSTEM TIME, EXPLAIN and multi-row inserts. Each
// feature renders natively where the product supports it and otherwise
// fails with an error wrapping [ErrNotSupported].

// ---------------------------------------------------------------------------
// RETURNING
// ---------------------------------------------------------------------------

// Returning appends a RETURNING clause to an INSERT, INSERT ... SELECT,
// UPDATE, DELETE or REPLACE query. Strings are column names ("*" selects
// every column); expressions ([F], [Raw], [Coalesce], ...) are accepted too.
// The returned rows are read with [QueryBuilder.RunQuery] or, to scan them
// into objects by column name, [RunQueryT] / [RunQueryTOne]:
//
//	users, err := psql.RunQueryT[User](ctx, psql.B().Update("users").
//	    Set(map[string]any{"active": false}).
//	    Where(map[string]any{"id": 42}).
//	    Returning("*"))
//
// RETURNING is supported on PostgreSQL, CockroachDB and SQLite for every
// statement, on MariaDB for INSERT/REPLACE and DELETE only, and not at all
// on MySQL; rendering fails with an error wrapping [ErrNotSupported] where
// it is not available. Check [Backend.Supports] with [FeatureReturning] to
// pick a strategy up front.
func (q *QueryBuilder) Returning(fields ...any) *QueryBuilder {
	for _, f := range fields {
		switch v := f.(type) {
		case string:
			q.ReturningFields = append(q.ReturningFields, fieldName(v))
		default:
			q.ReturningFields = append(q.ReturningFields, v)
		}
	}
	return q
}

// returningStatement maps the query type to the statement name used by
// [ReturningStatements] ("INSERT", "UPDATE" or "DELETE"), or "" when the
// query type cannot carry a RETURNING clause.
func (q *QueryBuilder) returningStatement() string {
	switch q.Query {
	case "INSERT", "INSERT_SELECT", "REPLACE":
		return "INSERT"
	case "UPDATE", "DELETE":
		return q.Query
	default:
		return ""
	}
}

// supportsReturning reports whether the rendering target supports RETURNING
// on the given statement. A dialect implementing [ReturningStatements] or
// [ReturningRenderer] answers authoritatively; otherwise the detected product
// decides: PostgreSQL, CockroachDB and SQLite support every statement,
// MariaDB INSERT and DELETE, MySQL none. Rendering without a backend (unknown
// engine) is permissive.
func (ctx *renderContext) supportsReturning(stmt string) bool {
	if rs, ok := ctx.d.(ReturningStatements); ok {
		return rs.SupportsReturningFor(stmt)
	}
	if rr, ok := ctx.d.(ReturningRenderer); ok {
		return rr.SupportsReturning()
	}
	switch ctx.v {
	case VariantPostgreSQL, VariantCockroachDB, VariantSQLite, VariantUnknown:
		return true
	case VariantMariaDB:
		return stmt == "INSERT" || stmt == "DELETE"
	default:
		return false
	}
}

// renderReturning appends the RETURNING clause, if any, to the query.
func (q *QueryBuilder) renderReturning(ctx *renderContext) error {
	if len(q.ReturningFields) == 0 {
		return nil
	}
	stmt := q.returningStatement()
	if stmt == "" {
		return fmt.Errorf("psql: RETURNING is not available on a %s query", q.Query)
	}
	if !ctx.supportsReturning(stmt) {
		return fmt.Errorf("psql: %s ... RETURNING: %w on %s", stmt, ErrNotSupported, ctx.v)
	}
	ctx.append("RETURNING")
	return ctx.appendCommaValues(q.ReturningFields...)
}

// ---------------------------------------------------------------------------
// Feature checks
// ---------------------------------------------------------------------------

// supports reports whether the rendering target supports a Feature* constant.
// A dialect implementing [VariantAware] answers authoritatively, otherwise
// the engine-level defaults apply. Rendering without a backend (unknown
// variant) is permissive so that queries can still be previewed.
func (ctx *renderContext) supports(feature string) bool {
	if ctx.v == VariantUnknown {
		return true
	}
	if va, ok := ctx.d.(VariantAware); ok {
		return va.SupportsFeature(ctx.v, feature)
	}
	return defaultSupports(ctx.v, feature)
}

// notSupported returns an error wrapping [ErrNotSupported] for the named
// feature on the rendering target.
func (ctx *renderContext) notSupported(what string) error {
	return fmt.Errorf("psql: %s: %w on %s", what, ErrNotSupported, ctx.v)
}

// ---------------------------------------------------------------------------
// Upsert extensions
// ---------------------------------------------------------------------------

// Excluded references the value that would have been inserted for column in
// the update part of an upsert. It renders as EXCLUDED."col" on PostgreSQL,
// CockroachDB and SQLite (ON CONFLICT ... DO UPDATE) and as VALUES("col")
// on MySQL and MariaDB (ON DUPLICATE KEY UPDATE), which makes
// [QueryBuilder.DoUpdate] with Excluded values the portable way to write an
// upsert:
//
//	psql.B().Insert(map[string]any{"id": 1, "hits": 1, "name": "x"}).Into("t").
//	    OnConflict("id").
//	    DoUpdate(map[string]any{"hits": psql.Excluded("hits"), "name": psql.Excluded("name")})
//	// PostgreSQL: ... ON CONFLICT ("id") DO UPDATE SET "hits"=EXCLUDED."hits","name"=EXCLUDED."name"
//	// MySQL:      ... ON DUPLICATE KEY UPDATE "hits"=VALUES("hits"),"name"=VALUES("name")
func Excluded(column string) EscapeValueable {
	return excludedValue(column)
}

type excludedValue string

// EscapeValue renders the reference with the ON CONFLICT (EXCLUDED) syntax.
func (e excludedValue) EscapeValue() string {
	return e.escapeValueCtx(nil)
}

func (e excludedValue) escapeValueCtx(ctx *renderContext) string {
	// MySQL, and the unknown engine which renders INSERT in the MySQL form,
	// use ON DUPLICATE KEY UPDATE with VALUES(); everything else (including
	// the context-less EscapeValue) uses the standard EXCLUDED pseudo-table.
	if ctx != nil && (ctx.e == EngineMySQL || ctx.e == EngineUnknown) {
		return "VALUES(" + QuoteName(string(e)) + ")"
	}
	return "EXCLUDED." + QuoteName(string(e))
}

// OnConflictConstraint names the unique or exclusion constraint whose
// violation triggers the ON CONFLICT action, instead of listing columns
// with [QueryBuilder.OnConflict]. It renders as ON CONFLICT ON CONSTRAINT
// "name" and is available on PostgreSQL and CockroachDB only; rendering
// fails with an error wrapping [ErrNotSupported] elsewhere.
func (q *QueryBuilder) OnConflictConstraint(name string) *QueryBuilder {
	q.ConflictConstraint = name
	return q
}

// DoUpdateWhere restricts the DO UPDATE action of an upsert to the rows
// matching the conditions (the same forms as [QueryBuilder.Where]), rendered
// as DO UPDATE SET ... WHERE .... Both the target row and the [Excluded]
// values may be referenced. Supported on PostgreSQL, CockroachDB and SQLite;
// MySQL and MariaDB have no equivalent and rendering fails with an error
// wrapping [ErrNotSupported].
func (q *QueryBuilder) DoUpdateWhere(conds ...any) *QueryBuilder {
	q.checkConditions("DoUpdateWhere", conds)
	q.ConflictWhere = append(q.ConflictWhere, conds...)
	return q
}

// renderConflictTarget renders the ON CONFLICT target: "(cols)" or
// "ON CONSTRAINT name". It returns "" when neither was given.
func (q *QueryBuilder) renderConflictTarget(ctx *renderContext) (string, error) {
	if q.ConflictConstraint != "" {
		if len(q.ConflictColumns) > 0 {
			return "", errors.New("psql: OnConflict columns and OnConflictConstraint cannot be combined")
		}
		if ctx.e != EnginePostgreSQL {
			return "", ctx.notSupported("ON CONFLICT ON CONSTRAINT")
		}
		return "ON CONSTRAINT " + QuoteName(q.ConflictConstraint), nil
	}
	if len(q.ConflictColumns) == 0 {
		return "", nil
	}
	cols := make([]string, len(q.ConflictColumns))
	for i, c := range q.ConflictColumns {
		cols[i] = QuoteName(c)
	}
	return "(" + strings.Join(cols, ",") + ")", nil
}

// renderOnConflict renders the conflict clause of an INSERT for the engines
// using the ON CONFLICT syntax (PostgreSQL, CockroachDB, SQLite). The DO
// NOTHING form is emitted when nothing is updated and the caller asks for it
// (SQLite uses INSERT OR IGNORE instead, see render).
func (q *QueryBuilder) renderOnConflict(ctx *renderContext, doNothing bool) error {
	target, err := q.renderConflictTarget(ctx)
	if err != nil {
		return err
	}
	if len(q.ConflictUpdate) == 0 {
		if !doNothing {
			return nil
		}
		if target == "" {
			ctx.append("ON CONFLICT DO NOTHING")
		} else {
			ctx.append("ON CONFLICT " + target + " DO NOTHING")
		}
		return nil
	}
	if target == "" {
		return fmt.Errorf("psql: DoUpdate requires OnConflict columns on %s", ctx.e)
	}
	ctx.append("ON CONFLICT " + target + " DO UPDATE SET")
	ctx.append(renderAssignments(ctx, q.ConflictUpdate))
	if len(q.ConflictWhere) > 0 {
		ctx.append("WHERE", q.ConflictWhere.escapeValueCtx(ctx))
	}
	return nil
}

// renderOnDuplicateKey renders the MySQL / MariaDB conflict clause.
func (q *QueryBuilder) renderOnDuplicateKey(ctx *renderContext) error {
	if q.ConflictConstraint != "" {
		return ctx.notSupported("ON CONFLICT ON CONSTRAINT")
	}
	if len(q.ConflictWhere) > 0 {
		return ctx.notSupported("DO UPDATE ... WHERE")
	}
	if len(q.ConflictUpdate) > 0 {
		ctx.append("ON DUPLICATE KEY UPDATE")
		ctx.append(renderAssignments(ctx, q.ConflictUpdate))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Common table expressions
// ---------------------------------------------------------------------------

// cteClause is one WITH entry: "name" [(columns)] AS (sub).
type cteClause struct {
	name      string
	columns   []string
	sub       any // *QueryBuilder or an expression (EscapeValueable)
	recursive bool
}

// With adds a common table expression, rendered as WITH "name" [("c1","c2")]
// AS (<sub>) ahead of the main query. The CTE can then be used as a table
// with [QueryBuilder.From] or [QueryBuilder.Join]. sub is normally a
// *QueryBuilder; an expression such as [Raw] is accepted for queries the
// builder cannot express. Arguments of the CTE are numbered before those of
// the main query. CTEs are supported by every engine (MySQL 8+, MariaDB
// 10.2+, SQLite 3.8.3+, PostgreSQL, CockroachDB).
//
//	active := psql.B().Select("id").From("users").Where(map[string]any{"active": true})
//	psql.B().With("active_users", active).
//	    Select("*").From("orders").
//	    Where(map[string]any{"user_id": &psql.SubIn{psql.B().Select("id").From("active_users")}})
//	// → WITH "active_users" AS (SELECT "id" FROM "users" WHERE ("active"=TRUE)) SELECT * FROM "orders" WHERE ...
func (q *QueryBuilder) With(name string, sub any, columns ...string) *QueryBuilder {
	return q.addCTE(name, sub, columns, false)
}

// WithRecursive adds a recursive common table expression. The whole WITH
// clause is rendered as WITH RECURSIVE when at least one CTE is recursive.
// Since the builder has no UNION support, the recursive query is usually
// written with [Raw]:
//
//	psql.B().WithRecursive("tree", psql.Raw(`SELECT "id","parent" FROM "nodes" WHERE "id"=1 UNION ALL SELECT n."id",n."parent" FROM "nodes" n JOIN "tree" t ON n."parent"=t."id"`), "id", "parent").
//	    Select("*").From("tree")
func (q *QueryBuilder) WithRecursive(name string, sub any, columns ...string) *QueryBuilder {
	return q.addCTE(name, sub, columns, true)
}

func (q *QueryBuilder) addCTE(name string, sub any, columns []string, recursive bool) *QueryBuilder {
	switch sub.(type) {
	case *QueryBuilder, escapeValueCtxable, EscapeValueable:
	default:
		q.errorf("psql: unsupported type %T for CTE %q; use *QueryBuilder or psql.Raw()", sub, name)
		return q
	}
	q.CTEs = append(q.CTEs, &cteClause{name: name, columns: columns, sub: sub, recursive: recursive})
	return q
}

// renderWith renders the WITH clause (or "" when the query has no CTE). It
// is called before the main query so that CTE arguments are numbered first.
func (q *QueryBuilder) renderWith(ctx *renderContext) string {
	if len(q.CTEs) == 0 {
		return ""
	}
	b := &strings.Builder{}
	b.WriteString("WITH ")
	for _, c := range q.CTEs {
		if c.recursive {
			b.WriteString("RECURSIVE ")
			break
		}
	}
	for i, c := range q.CTEs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(QuoteName(c.name))
		if len(c.columns) > 0 {
			cols := make([]string, len(c.columns))
			for j, col := range c.columns {
				cols[j] = QuoteName(col)
			}
			b.WriteString(" (" + strings.Join(cols, ",") + ")")
		}
		b.WriteString(" AS ")
		switch s := c.sub.(type) {
		case *QueryBuilder:
			b.WriteString(s.escapeValueCtx(ctx)) // renders "(SELECT ...)"
		default:
			b.WriteString("(" + escapeCtx(ctx, s) + ")")
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// DISTINCT ON
// ---------------------------------------------------------------------------

// DistinctOn keeps the first row of each group of rows sharing the given
// expressions, rendered as SELECT DISTINCT ON ("a","b") .... Strings are
// column names. The leading ORDER BY expressions should match the DISTINCT
// ON expressions. Available on PostgreSQL and CockroachDB only; elsewhere
// rendering fails with an error wrapping [ErrNotSupported] (see
// [FeatureDistinctOn]).
func (q *QueryBuilder) DistinctOn(fields ...any) *QueryBuilder {
	for _, f := range fields {
		switch v := f.(type) {
		case string:
			q.DistinctOnFields = append(q.DistinctOnFields, fieldName(v))
		default:
			q.DistinctOnFields = append(q.DistinctOnFields, v)
		}
	}
	return q
}

// renderDistinct appends the DISTINCT / DISTINCT ON (...) keywords.
func (q *QueryBuilder) renderDistinct(ctx *renderContext) error {
	if len(q.DistinctOnFields) > 0 {
		if !ctx.supports(FeatureDistinctOn) {
			return ctx.notSupported("DISTINCT ON")
		}
		parts := make([]string, len(q.DistinctOnFields))
		for i, f := range q.DistinctOnFields {
			parts[i] = escapeCtx(ctx, f)
		}
		ctx.append("DISTINCT ON (" + strings.Join(parts, ",") + ")")
		return nil
	}
	if q.Distinct {
		ctx.append("DISTINCT")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Row locking
// ---------------------------------------------------------------------------

// LockMode selects the row lock taken by a SELECT (see
// [QueryBuilder.SetLockMode] and [FetchOptions.LockMode]).
type LockMode int

const (
	LockNone        LockMode = iota // no row lock
	LockUpdate                      // FOR UPDATE: exclusive lock, blocks concurrent updates and locks
	LockShare                       // FOR SHARE: shared lock, blocks updates but not other shared locks
	LockNoKeyUpdate                 // FOR NO KEY UPDATE (PostgreSQL): like FOR UPDATE but does not block FOR KEY SHARE
	LockKeyShare                    // FOR KEY SHARE (PostgreSQL): weakest lock, blocks only key changes and deletes
)

// String returns the PostgreSQL spelling of the lock clause.
func (m LockMode) String() string {
	switch m {
	case LockUpdate:
		return "FOR UPDATE"
	case LockShare:
		return "FOR SHARE"
	case LockNoKeyUpdate:
		return "FOR NO KEY UPDATE"
	case LockKeyShare:
		return "FOR KEY SHARE"
	default:
		return ""
	}
}

// SetLockMode sets the row lock taken by the SELECT. Rendering per product:
//
//   - PostgreSQL, CockroachDB: FOR UPDATE, FOR SHARE, FOR NO KEY UPDATE or
//     FOR KEY SHARE, followed by OF "table" ([QueryBuilder.LockOf]) and
//     SKIP LOCKED / NOWAIT ([QueryBuilder.SetSkipLocked], [QueryBuilder.SetNoWait]).
//   - MySQL 8: FOR UPDATE or FOR SHARE [OF t] [SKIP LOCKED|NOWAIT].
//   - MariaDB and MySQL 5.7: FOR UPDATE or LOCK IN SHARE MODE; OF is not
//     available and fails with an error wrapping [ErrNotSupported].
//   - SQLite: locks are taken at the database level, the clause is omitted.
//
// [LockNoKeyUpdate] and [LockKeyShare] only exist on PostgreSQL and
// CockroachDB; MySQL and MariaDB fall back to FOR UPDATE and the share lock
// respectively. [QueryBuilder.SetForUpdate] is equivalent to
// SetLockMode([LockUpdate]).
func (q *QueryBuilder) SetLockMode(mode LockMode) *QueryBuilder {
	q.Lock = mode
	q.ForUpdate = mode == LockUpdate
	return q
}

// LockOf restricts the row lock to the rows of the given tables (or
// aliases), rendered as FOR UPDATE OF "t". Available on PostgreSQL,
// CockroachDB and MySQL 8. Implies [LockUpdate] when no lock mode was set.
func (q *QueryBuilder) LockOf(tables ...string) *QueryBuilder {
	q.LockTables = append(q.LockTables, tables...)
	if q.lockMode() == LockNone {
		q.ForUpdate = true
	}
	return q
}

// lockMode returns the effective lock mode ([QueryBuilder.ForUpdate] means
// [LockUpdate] when no explicit mode was set).
func (q *QueryBuilder) lockMode() LockMode {
	if q.Lock != LockNone {
		return q.Lock
	}
	if q.ForUpdate {
		return LockUpdate
	}
	return LockNone
}

// renderLock appends the row locking clause for the rendering target.
func (q *QueryBuilder) renderLock(ctx *renderContext) error {
	mode := q.lockMode()
	if mode == LockNone || ctx.e == EngineSQLite {
		// SQLite uses file/WAL-level locking, so FOR UPDATE is silently
		// omitted — users shouldn't need to worry about the engine.
		return nil
	}
	var clause string
	lockOf := true
	switch ctx.e {
	case EngineMySQL:
		legacyShare := ctx.v == VariantMariaDB || !ctx.serverVersionAtLeast(8, 0, 0)
		switch mode {
		case LockShare, LockKeyShare:
			if legacyShare {
				clause = "LOCK IN SHARE MODE"
			} else {
				clause = "FOR SHARE"
			}
		default:
			clause = "FOR UPDATE"
		}
		lockOf = !legacyShare
	default:
		clause = mode.String()
	}
	if len(q.LockTables) > 0 {
		if !lockOf {
			return ctx.notSupported(clause + " OF")
		}
		tables := make([]string, len(q.LockTables))
		for i, t := range q.LockTables {
			tables[i] = QuoteName(t)
		}
		clause += " OF " + strings.Join(tables, ",")
	}
	ctx.append(clause)
	if q.SkipLocked {
		ctx.append("SKIP LOCKED")
	} else if q.NoWait {
		ctx.append("NOWAIT")
	}
	return nil
}

// ---------------------------------------------------------------------------
// AS OF SYSTEM TIME
// ---------------------------------------------------------------------------

// AsOfSystemTime makes a SELECT read from a historical snapshot, rendered as
// AS OF SYSTEM TIME '<expr>' after the FROM tables and joins. expr is an
// interval relative to now ("-10s") or a timestamp ("2024-01-15 12:00:00")
// and is embedded as a string literal. Available on CockroachDB only (see
// [FeatureAsOfSystemTime]); rendering fails with an error wrapping
// [ErrNotSupported] elsewhere. Follower reads with a small negative interval
// are the typical use; the query must not run inside a transaction unless
// the transaction itself was started AS OF SYSTEM TIME.
func (q *QueryBuilder) AsOfSystemTime(expr string) *QueryBuilder {
	q.AsOf = expr
	return q
}

// renderAsOf appends the AS OF SYSTEM TIME clause, if any.
func (q *QueryBuilder) renderAsOf(ctx *renderContext) error {
	if q.AsOf == "" {
		return nil
	}
	// defaultSupports limits the feature to VariantCockroachDB; a VariantAware
	// dialect may refine the answer.
	if !ctx.supports(FeatureAsOfSystemTime) {
		return ctx.notSupported("AS OF SYSTEM TIME")
	}
	ctx.append("AS OF SYSTEM TIME", escapeString(q.AsOf))
	return nil
}

// ---------------------------------------------------------------------------
// EXPLAIN
// ---------------------------------------------------------------------------

// Explain runs the query plan statement for the query and returns the plan
// as text, one plan row per line (columns of multi-column plans are joined
// with tabs). With analyze the query is actually executed and timing
// information is included; note that this executes INSERT/UPDATE/DELETE
// queries for real.
//
// Statement per product: EXPLAIN [ANALYZE] on PostgreSQL and CockroachDB;
// EXPLAIN or EXPLAIN ANALYZE (8.0.18+, older servers fail with an error
// wrapping [ErrNotSupported]) on MySQL; EXPLAIN or ANALYZE on MariaDB;
// EXPLAIN QUERY PLAN on SQLite, where analyze is ignored.
func (q *QueryBuilder) Explain(ctx context.Context, analyze bool) (string, error) {
	be := GetBackend(ctx)
	query, args, err := q.RenderArgs(ctx)
	if err != nil {
		return "", err
	}
	var prefix string
	switch be.Variant() {
	case VariantSQLite:
		prefix = "EXPLAIN QUERY PLAN "
	case VariantMariaDB:
		if analyze {
			prefix = "ANALYZE "
		} else {
			prefix = "EXPLAIN "
		}
	case VariantMySQL:
		if analyze {
			if !serverVersionAtLeast(be.ServerVersion(), 8, 0, 18) {
				return "", fmt.Errorf("psql: EXPLAIN ANALYZE: %w on MySQL %s", ErrNotSupported, be.ServerVersion())
			}
			prefix = "EXPLAIN ANALYZE "
		} else {
			prefix = "EXPLAIN "
		}
	default:
		if analyze {
			prefix = "EXPLAIN ANALYZE "
		} else {
			prefix = "EXPLAIN "
		}
	}
	query = prefix + query

	rows, err := doQueryContext(ctx, query, args...)
	if err != nil {
		return "", &Error{query, err}
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	var lines []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return "", err
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = explainValue(v)
		}
		lines = append(lines, strings.Join(parts, "\t"))
	}
	if err := rows.Err(); err != nil {
		return "", &Error{query, err}
	}
	return strings.Join(lines, "\n"), nil
}

// explainValue formats a scanned plan cell as text.
func explainValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		return string(x)
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

// ---------------------------------------------------------------------------
// Multi-row inserts
// ---------------------------------------------------------------------------

// InsertRows adds rows to an INSERT (or REPLACE) query in column-list form:
// every row must have one value per column, in the same order. It renders
// as INSERT INTO "t" ("a","b") VALUES (1,2),(3,4) on every engine, the form
// [QueryBuilder.Insert] cannot express for more than one row. Values are
// rendered like SET assignments ([Raw], [Increment], ... are expanded) and
// bound as arguments by [QueryBuilder.RenderArgs]. Conflict clauses
// ([QueryBuilder.OnConflict], [QueryBuilder.DoUpdate], [QueryBuilder.DoNothing])
// and [QueryBuilder.Returning] apply to all rows. Sets the query type to
// INSERT unless [QueryBuilder.Replace] was called first; it cannot be mixed
// with [QueryBuilder.Insert] / [QueryBuilder.Set] values.
//
//	psql.B().InsertRows([]string{"id", "name"}, []any{1, "a"}, []any{2, "b"}).Into("users")
func (q *QueryBuilder) InsertRows(columns []string, rows ...[]any) *QueryBuilder {
	if q.Query == "" {
		q.Query = "INSERT"
	}
	if len(q.InsertColumns) == 0 {
		q.InsertColumns = columns
	} else if !equalStrings(q.InsertColumns, columns) {
		q.errorf("psql: InsertRows: columns %v differ from the previous rows' columns %v", columns, q.InsertColumns)
		return q
	}
	for _, r := range rows {
		if len(r) != len(columns) {
			q.errorf("psql: InsertRows: row has %d values for %d columns", len(r), len(columns))
			return q
		}
	}
	q.InsertValues = append(q.InsertValues, rows...)
	return q
}

// Values adds rows given as maps to an INSERT query (see
// [QueryBuilder.InsertRows]). The columns are the sorted keys of the first
// row; every row must have exactly the same keys.
//
//	psql.B().Values(map[string]any{"id": 1, "name": "a"}, map[string]any{"id": 2, "name": "b"}).Into("users")
//	// → INSERT INTO "users" ("id","name") VALUES (1,'a'),(2,'b')
func (q *QueryBuilder) Values(rows ...map[string]any) *QueryBuilder {
	if len(rows) == 0 {
		return q
	}
	columns := q.InsertColumns
	if len(columns) == 0 {
		columns = make([]string, 0, len(rows[0]))
		for k := range rows[0] {
			columns = append(columns, k)
		}
		sort.Strings(columns)
	}
	vals := make([][]any, len(rows))
	for i, row := range rows {
		if len(row) != len(columns) {
			q.errorf("psql: Values: row %d has %d columns, expected %d (%v)", i, len(row), len(columns), columns)
			return q
		}
		vals[i] = make([]any, len(columns))
		for j, c := range columns {
			v, ok := row[c]
			if !ok {
				q.errorf("psql: Values: row %d is missing column %q", i, c)
				return q
			}
			vals[i][j] = v
		}
	}
	return q.InsertRows(columns, vals...)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// renderInsertRows renders InsertColumns / InsertValues as
// ("c1","c2") VALUES (v1,v2),(v3,v4).
func (q *QueryBuilder) renderInsertRows(ctx *renderContext) string {
	if len(q.FieldsSet) > 0 {
		ctx.errorf("psql: InsertRows/Values cannot be combined with Insert/Set values")
		return ""
	}
	if len(q.InsertValues) == 0 {
		ctx.errorf("psql: no rows to insert")
		return ""
	}
	cols := make([]string, len(q.InsertColumns))
	for i, c := range q.InsertColumns {
		cols[i] = QuoteName(c)
	}
	b := &strings.Builder{}
	b.WriteString("(" + strings.Join(cols, ",") + ") VALUES ")
	for i, row := range q.InsertValues {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		for j, v := range row {
			if j > 0 {
				b.WriteByte(',')
			}
			b.WriteString(renderAssignmentValue(ctx, q.InsertColumns[j], v))
		}
		b.WriteByte(')')
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Server version helpers
// ---------------------------------------------------------------------------

// serverVersionAtLeast reports whether the recorded server version is at
// least major.minor.patch. An empty or unparsable version is assumed to be
// recent (true), so that a missing detection never disables a feature.
func serverVersionAtLeast(version string, major, minor, patch int) bool {
	maj, min, pat, ok := parseServerVersion(version)
	if !ok {
		return true
	}
	if maj != major {
		return maj > major
	}
	if min != minor {
		return min > minor
	}
	return pat >= patch
}

// serverVersionAtLeast is the context form of [serverVersionAtLeast].
func (ctx *renderContext) serverVersionAtLeast(major, minor, patch int) bool {
	return serverVersionAtLeast(ctx.sv, major, minor, patch)
}

// parseServerVersion extracts the leading numeric version from strings such
// as "8.0.32-log", "10.6.12-MariaDB" or "CockroachDB CCL v24.1.0".
func parseServerVersion(s string) (major, minor, patch int, ok bool) {
	start := -1
	for i, r := range s {
		if r >= '0' && r <= '9' {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, 0, 0, false
	}
	end := start
	for end < len(s) && (s[end] >= '0' && s[end] <= '9' || s[end] == '.') {
		end++
	}
	parts := strings.Split(s[start:end], ".")
	nums := make([]int, 3)
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			if i == 0 {
				return 0, 0, 0, false
			}
			break
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], true
}
