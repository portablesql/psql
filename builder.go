package psql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// EscapeValueable is implemented by types that can render themselves as SQL expressions.
// Used by the query builder for WHERE conditions, field references, and values.
type EscapeValueable interface {
	EscapeValue() string
}

type escapeValueCtxable interface {
	escapeValueCtx(ctx *renderContext) string
}

// SortValueable is a kind of value that can be used for sorting
type SortValueable interface {
	sortEscapeValue() string
}

// EscapeTableable is a type of value that can be used as a table
type EscapeTableable interface {
	EscapeTable() string
}

// QueryBuilder constructs SQL queries using a fluent API. Create one with [B], then
// chain methods to build SELECT, INSERT, UPDATE, DELETE, or REPLACE queries.
// Execute with [QueryBuilder.RunQuery], [QueryBuilder.ExecQuery], or [QueryBuilder.Render].
//
//	rows, err := psql.B().Select("id", "name").From("users").
//	    Where(map[string]any{"active": true}).
//	    OrderBy(psql.S("name", "ASC")).
//	    Limit(10).
//	    RunQuery(ctx)
type QueryBuilder struct {
	Query       string            // query type: SELECT, INSERT, UPDATE, DELETE, REPLACE or INSERT_SELECT
	Fields      []any             // selected fields (SELECT / INSERT ... SELECT)
	Tables      []EscapeTableable // tables of the FROM / INTO / UPDATE clause
	FieldsSet   []any             // SET / INSERT values (map[string]any entries, see [QueryBuilder.Set])
	WhereData   WhereAND          // WHERE conditions, joined with AND
	GroupBy     []any             // GROUP BY expressions
	HavingData  WhereAND          // HAVING conditions, joined with AND
	OrderByData []SortValueable   // ORDER BY expressions
	LimitData   []int             // LIMIT as [count] or [offset, count] (see [QueryBuilder.Limit])
	renderData  []any             // JOIN clauses

	// conflict/upsert
	ConflictColumns    []string // ON CONFLICT (columns)
	ConflictConstraint string   // ON CONFLICT ON CONSTRAINT name (see [QueryBuilder.OnConflictConstraint])
	ConflictUpdate     []any    // DO UPDATE SET fields (map[string]any entries)
	ConflictWhere      WhereAND // DO UPDATE SET ... WHERE conditions (see [QueryBuilder.DoUpdateWhere])
	ConflictNothing    bool     // DO NOTHING / INSERT IGNORE

	// multi-row INSERT (see [QueryBuilder.InsertRows] and [QueryBuilder.Values])
	InsertColumns []string // column list of the VALUES rows
	InsertValues  [][]any  // one entry per row, in InsertColumns order

	// advanced clauses
	ReturningFields  []any        // RETURNING expressions (see [QueryBuilder.Returning])
	CTEs             []*cteClause // WITH clauses (see [QueryBuilder.With])
	DistinctOnFields []any        // DISTINCT ON expressions (see [QueryBuilder.DistinctOn])
	Lock             LockMode     // row lock mode (see [QueryBuilder.SetLockMode]); ForUpdate means LockUpdate
	LockTables       []string     // FOR UPDATE OF tables (see [QueryBuilder.LockOf])
	AsOf             string       // AS OF SYSTEM TIME expression (see [QueryBuilder.AsOfSystemTime])

	// flags
	Distinct      bool // SELECT DISTINCT
	CalcFoundRows bool // SELECT SQL_CALC_FOUND_ROWS (MySQL)
	UpdateIgnore  bool // UPDATE IGNORE (MySQL)
	InsertIgnore  bool // INSERT IGNORE / INSERT OR IGNORE / ON CONFLICT DO NOTHING
	ForUpdate     bool // SELECT ... FOR UPDATE (ignored on SQLite)
	SkipLocked    bool // FOR UPDATE SKIP LOCKED
	NoWait        bool // FOR UPDATE NOWAIT

	err error
}

// B creates a new empty [QueryBuilder]. Chain methods to build a query:
//
//	psql.B().Select("*").From("users").Where(...)
//	psql.B().Update("users").Set(...).Where(...)
//	psql.B().Delete().From("users").Where(...)
func B() *QueryBuilder {
	return new(QueryBuilder)
}

// Select sets the query type to SELECT and specifies the fields to retrieve.
// String arguments are treated as field names. Pass no arguments to select all (*).
// When called after [QueryBuilder.InsertSelect], the query type is preserved.
func (q *QueryBuilder) Select(fields ...any) *QueryBuilder {
	if q.Query != "INSERT_SELECT" {
		q.Query = "SELECT"
	}
	if len(fields) > 0 {
		q.Fields = make([]any, 0, len(fields))
		for _, field := range fields {
			switch v := field.(type) {
			case string:
				// consider it to be a field name by default
				q.Fields = append(q.Fields, fieldName(v))
			case any:
				q.Fields = append(q.Fields, v)
			default:
				q.errorf("Unsupported field type %T for select", v)
				return q
			}
		}
	}
	return q
}

func (q *QueryBuilder) errorf(msg string, arg ...any) {
	q.err = fmt.Errorf(msg, arg...)
}

// AlsoSelect adds additional fields to an existing SELECT query.
func (q *QueryBuilder) AlsoSelect(fields ...any) *QueryBuilder {
	if q.Query != "SELECT" {
		q.err = errors.New("invalid QueryBuilder operation")
	}
	q.Fields = append(q.Fields, fields...)
	return q
}

// Update sets the query type to UPDATE and specifies the target table.
// Use [QueryBuilder.Set] to specify the fields to update.
func (q *QueryBuilder) Update(table any) *QueryBuilder {
	q.Query = "UPDATE"
	return q.Table(table)
}

// Replace sets the query type to REPLACE (MySQL) or equivalent upsert.
func (q *QueryBuilder) Replace(table EscapeTableable) *QueryBuilder {
	q.Query = "REPLACE"
	q.Tables = append(q.Tables, table)
	return q
}

// Delete sets the query type to DELETE. Use [QueryBuilder.From] to specify the table.
func (q *QueryBuilder) Delete() *QueryBuilder {
	q.Query = "DELETE"
	return q
}

// Insert sets the query type to INSERT and specifies the fields/values to insert.
func (q *QueryBuilder) Insert(fields ...any) *QueryBuilder {
	q.Query = "INSERT"
	q.FieldsSet = append(q.FieldsSet, fields...)
	return q
}

// Into specifies the target table for INSERT queries.
func (q *QueryBuilder) Into(table EscapeTableable) *QueryBuilder {
	q.Tables = append(q.Tables, table)
	return q
}

// From specifies the source table. Accepts a string (table name) or [EscapeTableable].
func (q *QueryBuilder) From(table any) *QueryBuilder {
	return q.Table(table)
}

// Table adds a table to the query. Accepts a string or [EscapeTableable].
// A string may carry an alias ("users AS u" or "users u"), rendered as
// "users" AS "u".
func (q *QueryBuilder) Table(table any) *QueryBuilder {
	switch v := table.(type) {
	case EscapeTableable:
		q.Tables = append(q.Tables, v)
	case string:
		q.Tables = append(q.Tables, parseTableRef(v))
	default:
		q.errorf("unsupported type %T passed as table", v)
	}
	return q
}

// Limit sets the LIMIT clause. With one argument, Limit(count) limits the
// number of rows. With two arguments, Limit(offset, count) skips offset rows
// and returns at most count rows, following the MySQL "LIMIT offset, count"
// convention (and [LimitFrom]). It renders as LIMIT count OFFSET offset on
// every engine.
func (q *QueryBuilder) Limit(v ...int) *QueryBuilder {
	switch len(v) {
	case 0:
		q.LimitData = nil
		return q
	case 1, 2:
		q.LimitData = v
		return q
	default:
		panic("invalid arguments for limit")
	}
}

// Set specifies the fields to update in UPDATE or INSERT queries.
// Typically pass a map[string]any: Set(map[string]any{"name": "Alice"}).
func (q *QueryBuilder) Set(fields ...any) *QueryBuilder {
	q.FieldsSet = append(q.FieldsSet, fields...)
	return q
}

// Where adds conditions to the WHERE clause. Accepts map[string]any for equality
// conditions, [EscapeValueable] for comparisons (e.g., [Equal], [Gt], [Like]),
// or multiple arguments which are joined with AND.
//
// Bare strings are not accepted as conditions (they would otherwise be bound
// as values); wrap raw SQL in [Raw] instead.
func (q *QueryBuilder) Where(where ...any) *QueryBuilder {
	q.checkConditions("Where", where)
	q.WhereData = append(q.WhereData, where...)
	return q
}

// checkConditions records an error if any of the conditions is a bare string.
func (q *QueryBuilder) checkConditions(method string, conds []any) {
	for _, c := range conds {
		if s, ok := c.(string); ok {
			q.errorf("psql: %s(): bare string condition %q is not supported; use psql.Raw() for raw SQL or map[string]any for field conditions", method, s)
			return
		}
	}
}

// OrderBy adds ORDER BY clauses. Use [S] to create sort fields:
// OrderBy(psql.S("name", "ASC"), psql.S("created_at", "DESC"))
func (q *QueryBuilder) OrderBy(field ...SortValueable) *QueryBuilder {
	q.OrderByData = append(q.OrderByData, field...)
	return q
}

// GroupByFields adds GROUP BY clause to the query.
func (q *QueryBuilder) GroupByFields(fields ...any) *QueryBuilder {
	for _, field := range fields {
		switch v := field.(type) {
		case string:
			q.GroupBy = append(q.GroupBy, fieldName(v))
		default:
			q.GroupBy = append(q.GroupBy, v)
		}
	}
	return q
}

// Having adds a HAVING clause to the query (used with GROUP BY). It accepts
// the same condition types as [QueryBuilder.Where].
func (q *QueryBuilder) Having(having ...any) *QueryBuilder {
	q.checkConditions("Having", having)
	q.HavingData = append(q.HavingData, having...)
	return q
}

// SetDistinct enables the DISTINCT keyword in the query.
func (q *QueryBuilder) SetDistinct() *QueryBuilder {
	q.Distinct = true
	return q
}

// OnConflict specifies the conflict columns for INSERT ... ON CONFLICT.
func (q *QueryBuilder) OnConflict(columns ...string) *QueryBuilder {
	q.ConflictColumns = columns
	return q
}

// DoUpdate specifies the fields to update on conflict. Accepts map[string]any
// entries, similar to [QueryBuilder.Set]. On PostgreSQL, CockroachDB and
// SQLite the conflict target must be given with [QueryBuilder.OnConflict] or
// [QueryBuilder.OnConflictConstraint], otherwise rendering fails; MySQL and
// MariaDB render ON DUPLICATE KEY UPDATE. Use [Excluded] values to refer to
// the row that would have been inserted; this renders correctly on every
// engine and is the recommended way to write a portable upsert:
//
//	psql.B().Insert(map[string]any{"id": 1, "hits": 1}).Table("t").
//	    OnConflict("id").DoUpdate(map[string]any{"hits": psql.Excluded("hits")})
//
// [QueryBuilder.DoUpdateWhere] restricts the update to matching rows.
func (q *QueryBuilder) DoUpdate(fields ...any) *QueryBuilder {
	q.ConflictUpdate = append(q.ConflictUpdate, fields...)
	return q
}

// DoNothing sets the ON CONFLICT action to DO NOTHING (PostgreSQL and
// CockroachDB, with the [QueryBuilder.OnConflict] target when one is given),
// INSERT OR IGNORE (SQLite) or INSERT IGNORE (MySQL).
func (q *QueryBuilder) DoNothing() *QueryBuilder {
	q.ConflictNothing = true
	return q
}

// SetForUpdate adds FOR UPDATE locking to the query; it is equivalent to
// SetLockMode([LockUpdate]). See [QueryBuilder.SetLockMode] for the
// rendering on each engine.
func (q *QueryBuilder) SetForUpdate() *QueryBuilder {
	q.ForUpdate = true
	return q
}

// SetSkipLocked adds SKIP LOCKED after the lock clause (FOR UPDATE unless
// another mode was chosen with [QueryBuilder.SetLockMode]). Rows locked by
// other transactions are skipped instead of blocking.
func (q *QueryBuilder) SetSkipLocked() *QueryBuilder {
	if q.Lock == LockNone {
		q.ForUpdate = true
	}
	q.SkipLocked = true
	return q
}

// SetNoWait adds NOWAIT after the lock clause (FOR UPDATE unless another
// mode was chosen with [QueryBuilder.SetLockMode]). The query fails
// immediately if any selected row is locked by another transaction.
func (q *QueryBuilder) SetNoWait() *QueryBuilder {
	if q.Lock == LockNone {
		q.ForUpdate = true
	}
	q.NoWait = true
	return q
}

// Join adds a JOIN clause to the query. The table can be a string (table name,
// optionally with an alias as in "orders o") or an [EscapeTableable] such as
// [SubTable] for subquery joins. Conditions accept the same types as
// [QueryBuilder.Where] and are joined with AND:
//
//	q.Join("LEFT", "orders", psql.Equal(psql.F("orders.user_id"), psql.F("users.id")))
//	q.Join("LEFT", psql.SubTable(subQuery, "sq"), psql.Equal(psql.F("sq.id"), psql.F("t.id")))
func (q *QueryBuilder) Join(joinType string, table any, condition ...any) *QueryBuilder {
	var tbl EscapeTableable
	switch v := table.(type) {
	case string:
		tbl = parseTableRef(v)
	case EscapeTableable:
		tbl = v
	default:
		q.errorf("unsupported type %T passed as join table", table)
		return q
	}
	q.checkConditions("Join", condition)
	q.renderData = append(q.renderData, &joinClause{
		joinType:  joinType,
		table:     tbl,
		condition: condition,
	})
	return q
}

// LeftJoin adds a LEFT JOIN clause. The table can be a string or [EscapeTableable].
func (q *QueryBuilder) LeftJoin(table any, condition ...any) *QueryBuilder {
	return q.Join("LEFT", table, condition...)
}

// InnerJoin adds an INNER JOIN clause. The table can be a string or [EscapeTableable].
func (q *QueryBuilder) InnerJoin(table any, condition ...any) *QueryBuilder {
	return q.Join("INNER", table, condition...)
}

// RightJoin adds a RIGHT JOIN clause. The table can be a string or [EscapeTableable].
func (q *QueryBuilder) RightJoin(table any, condition ...any) *QueryBuilder {
	return q.Join("RIGHT", table, condition...)
}

// InsertSelect creates an INSERT ... SELECT query. Specify the destination table,
// then chain [QueryBuilder.Select], [QueryBuilder.From], and [QueryBuilder.Where]
// to define the source query:
//
//	psql.B().InsertSelect("archive").Select("id", "name").From("users").
//	    Where(map[string]any{"active": false})
//	// → INSERT INTO "archive" SELECT "id","name" FROM "users" WHERE ("active"=FALSE)
func (q *QueryBuilder) InsertSelect(destTable any) *QueryBuilder {
	q.Query = "INSERT_SELECT"
	return q.Table(destTable)
}

type joinClause struct {
	joinType  string
	table     EscapeTableable
	condition []any
}

// SubIn wraps a [QueryBuilder] subquery for use with IN (subquery) in WHERE conditions:
//
//	psql.B().Select().From("users").Where(map[string]any{
//	    "id": &psql.SubIn{psql.B().Select("user_id").From("orders")},
//	})
//	// → ... WHERE "id" IN (SELECT "user_id" FROM "orders")
type SubIn struct {
	Sub *QueryBuilder
}

// escapeValueCtx renders the QueryBuilder as a parenthesized subquery, sharing
// the parent context's args slice so parameter numbering continues correctly.
// Rendering errors are recorded on the context so the parent query fails.
func (q *QueryBuilder) escapeValueCtx(ctx *renderContext) string {
	if ctx == nil {
		ctx = fallbackRenderContext()
	}
	savedReq := ctx.req
	err := q.render(ctx)
	subSQL := strings.Join(ctx.req, " ")
	ctx.req = savedReq
	if err != nil {
		ctx.setErr(err)
		return "NULL"
	}
	return "(" + subSQL + ")"
}

// EscapeValue renders the QueryBuilder as a parenthesized subquery
// (non-parameterized, engine-neutral). Rendering errors are not reported;
// use [QueryBuilder.Render] to get them.
func (q *QueryBuilder) EscapeValue() string {
	return q.escapeValueCtx(nil)
}

// Apply runs the given scopes on this query builder, returning the modified builder.
// Scopes can add WHERE, ORDER BY, LIMIT, or any other clause.
func (q *QueryBuilder) Apply(scopes ...Scope) *QueryBuilder {
	for _, s := range scopes {
		q = s(q)
	}
	return q
}

// Render generates the SQL query string for the current engine. Values are
// embedded directly (not parameterized). For parameterized queries, use [QueryBuilder.RenderArgs].
func (q *QueryBuilder) Render(ctx context.Context) (string, error) {
	// Generate the actual SQL query
	rctx := newBackendRenderContext(GetBackend(ctx), false)
	err := q.render(rctx)
	if err != nil {
		return "", err
	}
	return strings.Join(rctx.req, " "), nil
}

// RenderArgs generates the SQL query string with parameterized placeholders and
// returns the arguments separately. Uses $1/$2/... for PostgreSQL and ? for MySQL/SQLite.
func (q *QueryBuilder) RenderArgs(ctx context.Context) (string, []any, error) {
	// Generate the actual SQL query
	rctx := newBackendRenderContext(GetBackend(ctx), true)
	err := q.render(rctx)
	if err != nil {
		return "", nil, err
	}
	return strings.Join(rctx.req, " "), rctx.args, nil
}

// RunQuery executes the query and returns *sql.Rows for reading results.
// The caller must close the returned rows. Typically used for SELECT queries.
func (q *QueryBuilder) RunQuery(ctx context.Context) (*sql.Rows, error) {
	query, args, err := q.RenderArgs(ctx)
	if err != nil {
		return nil, err
	}

	res, err := doQueryContext(ctx, query, args...)
	if err != nil {
		return nil, &Error{query, err}
	}
	return res, nil
}

// ExecQuery executes the query and returns sql.Result. Used for INSERT, UPDATE,
// DELETE, and other non-row-returning queries.
func (q *QueryBuilder) ExecQuery(ctx context.Context) (sql.Result, error) {
	query, args, err := q.RenderArgs(ctx)
	if err != nil {
		return nil, err
	}
	return ExecContext(ctx, query, args...)
}

// Prepare creates a prepared statement from the query. The caller must close
// the returned statement.
func (q *QueryBuilder) Prepare(ctx context.Context) (*sql.Stmt, error) {
	query, _, err := q.RenderArgs(ctx)
	if err != nil {
		return nil, err
	}

	return doPrepareContext(ctx, query)
}

func (q *QueryBuilder) render(ctx *renderContext) error {
	if q.err != nil {
		return q.err
	}

	// CTEs are rendered first so that their arguments are numbered before
	// those of the main query; the WITH clause prefixes every statement,
	// except that MySQL only accepts it ahead of the SELECT part of an
	// INSERT ... SELECT.
	var prefix []string
	with := q.renderWith(ctx)
	if ctx.err != nil {
		return ctx.err
	}
	mysqlInsertSelect := q.Query == "INSERT_SELECT" && ctx.e == EngineMySQL
	if with != "" && !mysqlInsertSelect {
		prefix = []string{with}
	}
	// start resets the query to the WITH prefix followed by words.
	start := func(words ...string) {
		ctx.req = append(append([]string(nil), prefix...), words...)
	}
	start(q.Query)
	var err error

	switch q.Query {
	case "SELECT":
		if err = q.renderDistinct(ctx); err != nil {
			return err
		}
		if q.CalcFoundRows {
			ctx.append("SQL_CALC_FOUND_ROWS")
		}
		err = q.renderFields(ctx)
		if err != nil {
			return err
		}
		ctx.append("FROM")
		err = q.renderTables(ctx)
		if err != nil {
			return err
		}
		if err = q.renderAsOf(ctx); err != nil {
			return err
		}
	case "DELETE":
		ctx.append("FROM")
		err = q.renderTables(ctx)
		if err != nil {
			return err
		}
	case "UPDATE":
		if q.UpdateIgnore {
			ctx.append("IGNORE")
		}
		err = q.renderTables(ctx)
		if err != nil {
			return err
		}
		ctx.append("SET")
		ctx.append(renderAssignments(ctx, q.FieldsSet))
	case "REPLACE":
		if len(q.InsertValues) > 0 {
			// column-list form, valid on MySQL, MariaDB and SQLite
			ctx.append("INTO")
		}
		err = q.renderTables(ctx)
		if err != nil {
			return err
		}
		if len(q.InsertValues) > 0 {
			ctx.append(q.renderInsertRows(ctx))
		} else {
			ctx.append("SET")
			ctx.append(renderAssignments(ctx, q.FieldsSet))
		}
	case "INSERT":
		switch ctx.e {
		case EnginePostgreSQL:
			// PostgreSQL: use (cols) VALUES (vals) format
			ctx.append("INTO")
			err = q.renderTables(ctx)
			if err != nil {
				return err
			}
			ctx.append(q.renderInsertBody(ctx))
			// ON CONFLICT clause
			err = q.renderOnConflict(ctx, q.InsertIgnore || q.ConflictNothing)
			if err != nil {
				return err
			}
		case EngineSQLite:
			// SQLite: use (cols) VALUES (vals) format
			if q.InsertIgnore || q.ConflictNothing {
				start("INSERT", "OR", "IGNORE")
			}
			ctx.append("INTO")
			err = q.renderTables(ctx)
			if err != nil {
				return err
			}
			ctx.append(q.renderInsertBody(ctx))
			err = q.renderOnConflict(ctx, false)
			if err != nil {
				return err
			}
		default:
			// MySQL / Unknown: use SET syntax (MySQL-native) for a single
			// row, the column-list form for InsertRows
			if q.InsertIgnore || q.ConflictNothing {
				ctx.append("IGNORE")
			}
			ctx.append("INTO")
			err = q.renderTables(ctx)
			if err != nil {
				return err
			}
			if len(q.InsertValues) > 0 {
				ctx.append(q.renderInsertRows(ctx))
			} else {
				ctx.append("SET")
				ctx.append(renderAssignments(ctx, q.FieldsSet))
			}
			err = q.renderOnDuplicateKey(ctx)
			if err != nil {
				return err
			}
		}
	case "INSERT_SELECT":
		if len(q.Tables) < 2 {
			return fmt.Errorf("INSERT SELECT requires at least two tables")
		}
		start("INSERT")
		ignore := q.InsertIgnore || q.ConflictNothing
		switch ctx.e {
		case EngineSQLite:
			if ignore {
				ctx.append("OR IGNORE")
			}
		case EnginePostgreSQL:
			// rendered as ON CONFLICT DO NOTHING after the SELECT
		default:
			if ignore {
				ctx.append("IGNORE")
			}
		}
		table := q.Tables[0]
		ctx.append("INTO", escapeTableWithCtx(ctx, table))
		if mysqlInsertSelect && with != "" {
			ctx.append(with)
		}
		ctx.append("SELECT")
		if err = q.renderDistinct(ctx); err != nil {
			return err
		}
		err = q.renderFields(ctx)
		if err != nil {
			return err
		}
		ctx.append("FROM")
		// Render only source tables (skip destination table at index 0)
		err = q.renderTablesFrom(ctx, 1)
		if err != nil {
			return err
		}
	}

	if len(q.WhereData) > 0 {
		ctx.append("WHERE", q.WhereData.escapeValueCtx(ctx))
	}
	if len(q.GroupBy) > 0 {
		ctx.append("GROUP BY")
		err = ctx.appendCommaValues(q.GroupBy...)
		if err != nil {
			return err
		}
	}
	if len(q.HavingData) > 0 {
		ctx.append("HAVING", q.HavingData.escapeValueCtx(ctx))
	}
	if len(q.OrderByData) > 0 {
		ctx.append("ORDER BY")
		err = ctx.appendCommaValuesSort(q.OrderByData...)
		if err != nil {
			return err
		}
	}
	if len(q.LimitData) > 0 && ctx.e != EngineMySQL && ctx.e != EngineUnknown && (q.Query == "DELETE" || q.Query == "UPDATE") {
		// PostgreSQL has no DELETE/UPDATE ... LIMIT and the modernc SQLite
		// build is compiled without SQLITE_ENABLE_UPDATE_DELETE_LIMIT
		return fmt.Errorf("psql: %s ... LIMIT is not supported on %s", q.Query, ctx.e)
	}
	switch len(q.LimitData) {
	case 1:
		ctx.append("LIMIT", strconv.Itoa(q.LimitData[0]))
	case 2:
		// LimitData is [offset, count]; LIMIT count OFFSET offset works on every engine
		ctx.append("LIMIT", strconv.Itoa(q.LimitData[1]), "OFFSET", strconv.Itoa(q.LimitData[0]))
	}
	if err = q.renderLock(ctx); err != nil {
		return err
	}
	if q.Query == "INSERT_SELECT" {
		// conflict clause of INSERT ... SELECT comes after the SELECT
		switch ctx.e {
		case EnginePostgreSQL:
			err = q.renderOnConflict(ctx, q.InsertIgnore || q.ConflictNothing)
		case EngineSQLite:
			err = q.renderOnConflict(ctx, false)
		default:
			err = q.renderOnDuplicateKey(ctx)
		}
		if err != nil {
			return err
		}
	}
	if err = q.renderReturning(ctx); err != nil {
		return err
	}

	return ctx.err
}

// renderInsertBody renders the values part of an INSERT in column-list form:
// the InsertRows/Values rows when given, the single Insert/Set row otherwise.
func (q *QueryBuilder) renderInsertBody(ctx *renderContext) string {
	if len(q.InsertValues) > 0 {
		return q.renderInsertRows(ctx)
	}
	return q.renderInsertColsVals(ctx)
}

func (q *QueryBuilder) renderFields(ctx *renderContext) error {
	if len(q.Fields) == 0 {
		ctx.append("*")
		return nil
	}
	return ctx.appendCommaValues(q.Fields...)
}

// renderInsertColsVals renders FieldsSet as "(col1,col2) VALUES (val1,val2)" format.
// It iterates the FieldsSet entries (which must be map[string]any) and extracts
// sorted columns and their corresponding values. Values are rendered like SET
// assignments ([Increment], [Decrement] and [SetRaw] are expanded).
func (q *QueryBuilder) renderInsertColsVals(ctx *renderContext) string {
	var cols []string
	var vals []string

	for _, fs := range q.FieldsSet {
		m, ok := fs.(map[string]any)
		if !ok {
			ctx.errorf("psql: unsupported type %T in INSERT values; use map[string]any", fs)
			continue
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			cols = append(cols, QuoteName(k))
			vals = append(vals, renderAssignmentValue(ctx, k, m[k]))
		}
	}

	if len(cols) == 0 {
		ctx.errorf("psql: no fields to insert")
		return ""
	}

	return "(" + strings.Join(cols, ",") + ") VALUES (" + strings.Join(vals, ",") + ")"
}

func (q *QueryBuilder) renderTables(ctx *renderContext) error {
	return q.renderTablesFrom(ctx, 0)
}

func (q *QueryBuilder) renderTablesFrom(ctx *renderContext, startIdx int) error {
	b := &strings.Builder{}

	first := true
	for i := startIdx; i < len(q.Tables); i++ {
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.WriteString(escapeTableWithCtx(ctx, q.Tables[i]))
	}

	// Append JOIN clauses
	for _, rd := range q.renderData {
		if j, ok := rd.(*joinClause); ok {
			b.WriteByte(' ')
			b.WriteString(j.joinType)
			b.WriteString(" JOIN ")
			b.WriteString(escapeTableWithCtx(ctx, j.table))
			if len(j.condition) > 0 {
				b.WriteString(" ON ")
				b.WriteString(escapeWhere(ctx, j.condition, " AND "))
			}
		}
	}

	ctx.append(b.String())
	return nil
}
