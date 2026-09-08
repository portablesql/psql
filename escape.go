package psql

import (
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KarpelesLab/typutil"
)

// Escape takes any value and renders it as a SQL literal that can be embedded
// verbatim in a query: strings are quoted, binary data is rendered as hex,
// times as quoted timestamps, nil as NULL, and query builder expressions
// ([F], [Raw], [Equal], subqueries, ...) as SQL.
//
// The result is not engine specific. Use [QueryBuilder.Render] for
// engine-aware rendering and [QueryBuilder.RenderArgs] for parameterized
// queries, which is the preferred way to pass user-supplied values.
func Escape(val any) string {
	return escapeCtx(nil, val)
}

// escapeCtx renders val as a SQL value. When ctx is parameterized, plain
// values are bound as arguments and a placeholder is returned; expressions
// (fields, raw SQL, comparisons, subqueries...) are always rendered as SQL.
func escapeCtx(ctx *renderContext, val any) string {
	if ctx != nil && ctx.useArgs {
		switch v := val.(type) {
		case *fullField, fieldName, tableName:
			break // continue below
		case escapeValueCtxable:
			return v.escapeValueCtx(ctx)
		case *rawValue:
			return v.V
		default:
			return ctx.appendArg(val)
		}
	}
	// null check
	if val == nil {
		return "NULL"
	}

	switch v := val.(type) {
	case escapeValueCtxable:
		return v.escapeValueCtx(ctx)
	case EscapeValueable:
		return v.EscapeValue()
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		if v {
			return "TRUE"
		}
		return "FALSE"
	case []byte:
		return escapeBytes(ctx, v)
	case string:
		return escapeString(v)
	case time.Time:
		return escapeTime(ctx, v)
	case driver.Valuer:
		if typutil.IsNil(v) {
			return "NULL"
		}
		sub, err := v.Value()
		if err != nil {
			ctx.errorf("psql: %T.Value(): %w", val, err)
			return "NULL"
		}
		return escapeCtx(ctx, sub)
	}

	rv := reflect.ValueOf(val)
	if rv.Kind() == reflect.Ptr {
		// dereference pointers before looking at fmt.Stringer so that
		// *time.Time and friends render as their underlying value
		if rv.IsNil() {
			return "NULL"
		}
		return escapeCtx(ctx, rv.Elem().Interface())
	}

	if s, ok := val.(fmt.Stringer); ok {
		// String() is arbitrary text: always escape it as a string literal
		return escapeString(s.String())
	}

	switch rv.Kind() {
	case reflect.Bool:
		if rv.Bool() {
			return "TRUE"
		}
		return "FALSE"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(rv.Uint(), 10)
	case reflect.Float32:
		return strconv.FormatFloat(rv.Float(), 'g', -1, 32)
	case reflect.Float64:
		return strconv.FormatFloat(rv.Float(), 'g', -1, 64)
	case reflect.Complex64:
		return strconv.FormatComplex(rv.Complex(), 'g', -1, 64)
	case reflect.Complex128:
		return strconv.FormatComplex(rv.Complex(), 'g', -1, 128)
	case reflect.String:
		return escapeString(rv.String())
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct, reflect.Chan, reflect.Func:
		ctx.errorf("psql: cannot render %T as a SQL value", val)
		return fmt.Sprintf("%v", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// escapeString renders s as a SQL string literal. Backslashes are not
// special (NO_BACKSLASH_ESCAPES / standard SQL behavior).
func escapeString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// escapeBytes renders binary data as a literal for the context's engine.
func escapeBytes(ctx *renderContext, v []byte) string {
	if v == nil {
		return "NULL"
	}
	switch ctx.engine() {
	case EnginePostgreSQL:
		return `'\x` + hex.EncodeToString(v) + `'::bytea`
	default:
		return "x'" + hex.EncodeToString(v) + "'"
	}
}

// escapeTime renders a timestamp as a quoted literal for the context's engine.
// Times are always rendered in UTC.
func escapeTime(ctx *renderContext, v time.Time) string {
	switch ctx.engine() {
	case EngineSQLite:
		// SQLite stores timestamps as RFC3339 text; use the same fixed-width
		// format as the driver so that comparisons work lexicographically.
		if v.IsZero() {
			return "'0001-01-01T00:00:00.000000000Z'"
		}
		return v.UTC().Format("'2006-01-02T15:04:05.000000000Z07:00'")
	case EnginePostgreSQL:
		if v.IsZero() {
			return "'0001-01-01 00:00:00'"
		}
		return v.UTC().Format("'2006-01-02 15:04:05.999999'")
	default:
		if v.IsZero() {
			return "'0000-00-00 00:00:00.000000'"
		}
		return v.UTC().Format("'2006-01-02 15:04:05.999999'")
	}
}

// whereFail records a rendering error and returns a placeholder condition.
func whereFail(ctx *renderContext, format string, args ...any) string {
	ctx.errorf(format, args...)
	return "FALSE"
}

// boolCond returns the SQL constant for a condition that is statically
// true or false, taking negation into account.
func boolCond(v, not bool) string {
	if v != not {
		return "TRUE"
	}
	return "FALSE"
}

// renderIn renders fld IN(...) / fld NOT IN(...). An empty list is always
// false (or always true when negated).
func renderIn(ctx *renderContext, fld string, vals []any, not bool) string {
	if len(vals) == 0 {
		return boolCond(false, not)
	}
	b := &strings.Builder{}
	b.WriteString(fld)
	if not {
		b.WriteString(" NOT IN(")
	} else {
		b.WriteString(" IN(")
	}
	for n, sub := range vals {
		if n != 0 {
			b.WriteByte(',')
		}
		b.WriteString(escapeCtx(ctx, sub))
	}
	b.WriteByte(')')
	return b.String()
}

// renderNullCheck renders fld IS NULL / fld IS NOT NULL.
func renderNullCheck(fld string, not bool) string {
	if not {
		return fld + " IS NOT NULL"
	}
	return fld + " IS NULL"
}

// renderComparison renders fld <op> value, turning = NULL / != NULL into
// IS NULL / IS NOT NULL.
func renderComparison(ctx *renderContext, fld, op string, val any) string {
	if typutil.IsNil(val) {
		switch op {
		case "=":
			return fld + " IS NULL"
		case "!=", "<>":
			return fld + " IS NOT NULL"
		}
	}
	return fld + op + escapeCtx(ctx, val)
}

// renderLike renders a LIKE condition against an already rendered field
// expression. Case-insensitive matching renders as ILIKE on PostgreSQL,
// LOWER(field) LIKE LOWER(pattern) on SQLite and a plain LIKE on MySQL
// (whose default collations are case-insensitive).
func renderLike(ctx *renderContext, fieldExpr string, pattern any, caseInsensitive, not bool) string {
	keyword := "LIKE"
	lhs := fieldExpr
	rhs := escapeCtx(ctx, pattern)
	if caseInsensitive {
		switch ctx.engine() {
		case EnginePostgreSQL:
			keyword = "ILIKE"
		case EngineSQLite:
			lhs = "LOWER(" + lhs + ")"
			rhs = "LOWER(" + rhs + ")"
		}
	}
	if not {
		keyword = "NOT " + keyword
	}
	return lhs + " " + keyword + " " + rhs + " ESCAPE '\\'"
}

// renderFindInSet renders a "value is an element of the comma-separated
// list stored in field" condition. MySQL has FIND_IN_SET(); PostgreSQL and
// SQLite use portable equivalents.
func renderFindInSet(ctx *renderContext, fieldExpr, valueExpr string, not bool) string {
	switch ctx.engine() {
	case EnginePostgreSQL:
		expr := valueExpr + " = ANY(string_to_array(" + fieldExpr + ", ','))"
		if not {
			return "NOT (" + expr + ")"
		}
		return expr
	case EngineSQLite:
		like := "LIKE"
		if not {
			like = "NOT LIKE"
		}
		return "(',' || " + fieldExpr + " || ',') " + like + " '%,' || " + valueExpr + " || ',%'"
	default:
		expr := "FIND_IN_SET(" + valueExpr + "," + fieldExpr + ")"
		if not {
			return "NOT " + expr
		}
		return expr
	}
}

// escapeWhereSub renders a single key: value condition from a WHERE map.
func escapeWhereSub(ctx *renderContext, key string, val any) string {
	fld := fieldName(key).EscapeValue()
	not := false
	if n, ok := val.(*Not); ok {
		not = true
		if n == nil {
			val = nil
		} else {
			val = n.V
		}
	}
	eq := "="
	if not {
		eq = "!="
	}

	// Handle pointer wrapper types before Flatten (which dereferences pointers)
	switch v := val.(type) {
	case *SubIn:
		if v == nil || v.Sub == nil {
			return whereFail(ctx, "psql: nil subquery in condition on field %q", key)
		}
		if not {
			return fld + " NOT IN " + v.Sub.escapeValueCtx(ctx)
		}
		return fld + " IN " + v.Sub.escapeValueCtx(ctx)
	case *Any:
		return escapeAnyInWhere(ctx, key, v, not)
	case *Increment, *Decrement, *SetRaw:
		return whereFail(ctx, "psql: %T can only be used in a SET clause, not in a condition on field %q", val, key)
	}

	if typutil.IsNil(val) {
		return renderNullCheck(fld, not)
	}

	// driver.Valuer implementations (Set, Vector, Hex, V()...) are always a
	// single value, even when their underlying type is a slice.
	if _, ok := val.(driver.Valuer); ok {
		return fld + eq + escapeCtx(ctx, val)
	}

	flat := typutil.Flatten(val)
	switch v := flat.(type) {
	case nil:
		return renderNullCheck(fld, not)
	case Like:
		// ignore Field, the map key is the field
		return renderLike(ctx, fld, v.Like, v.CaseInsensitive, not)
	case FindInSet:
		// ignore Field, the map key is the field
		return renderFindInSet(ctx, fld, escapeCtx(ctx, v.Value), not)
	case Comparison:
		// ignore Field (A) and only use B + Op
		op, ok := v.opStr(not)
		if !ok {
			return whereFail(ctx, "psql: unsupported comparison operator %q in condition on field %q", v.Op, key)
		}
		if typutil.IsNil(v.B) && (op == "=" || op == "!=") {
			return renderNullCheck(fld, op == "!=")
		}
		return fld + " " + op + " " + escapeCtx(ctx, v.B)
	case betweenComp:
		// ignore Field (a) and only use start + end
		res := fld
		if not {
			res += " NOT"
		}
		return res + " BETWEEN " + escapeCtx(ctx, v.start) + " AND " + escapeCtx(ctx, v.end)
	case WhereOR:
		return renderWhereGroup(ctx, key, v, " OR ", false, not)
	case WhereAND:
		return renderWhereGroup(ctx, key, v, " AND ", true, not)
	case []any:
		return renderIn(ctx, fld, v, not)
	case []string:
		vals := make([]any, len(v))
		for i, s := range v {
			vals[i] = s
		}
		return renderIn(ctx, fld, vals, not)
	case map[string]any:
		// $gt/$lt/... operators, combined with AND
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		conds := make([]string, 0, len(keys))
		for _, k := range keys {
			var op string
			switch k {
			case "$gt":
				op = ">"
			case "$lt":
				op = "<"
			case "$gte":
				op = ">="
			case "$lte":
				op = "<="
			default:
				return whereFail(ctx, "psql: unknown operator %q in condition on field %q", k, key)
			}
			conds = append(conds, fld+op+escapeCtx(ctx, v[k]))
		}
		switch len(conds) {
		case 0:
			return boolCond(false, not)
		case 1:
			if not {
				return "NOT (" + conds[0] + ")"
			}
			return conds[0]
		default:
			res := "(" + strings.Join(conds, " AND ") + ")"
			if not {
				return "NOT " + res
			}
			return res
		}
	case []byte:
		// Binary data: render as a value, not as IN(byte1, byte2, ...).
		return fld + eq + escapeCtx(ctx, v)
	default:
		rv := reflect.ValueOf(flat)
		if rv.Kind() == reflect.Slice {
			// typed slices ([]int, []int64, etc.) → IN(...)
			vals := make([]any, rv.Len())
			for i := range vals {
				vals[i] = rv.Index(i).Interface()
			}
			return renderIn(ctx, fld, vals, not)
		}
		return fld + eq + escapeCtx(ctx, val)
	}
}

// renderWhereGroup renders a nested WhereOR/WhereAND applied to a single
// field: each element becomes a condition on that field, joined by glue.
// An empty group is TRUE for AND and FALSE for OR; the whole group is
// wrapped in NOT (...) when negated.
func renderWhereGroup(ctx *renderContext, key string, items []any, glue string, emptyValue, not bool) string {
	if len(items) == 0 {
		return boolCond(emptyValue, not)
	}
	parts := make([]string, len(items))
	for n, subv := range items {
		parts[n] = escapeWhereSub(ctx, key, subv)
	}
	res := "(" + strings.Join(parts, glue) + ")"
	if not {
		return "NOT " + res
	}
	return res
}

// escapeWhere renders a condition (or a list of conditions joined by glue).
func escapeWhere(ctx *renderContext, val any, glue string) string {
	switch v := val.(type) {
	case map[string]any:
		if len(v) == 0 {
			// empty where → match all
			return "TRUE"
		}
		// key = value
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, escapeWhereSub(ctx, key, v[key]))
		}
		return strings.Join(parts, glue)
	case []string:
		if len(v) == 0 {
			return "TRUE"
		}
		parts := make([]string, 0, len(v))
		for _, sub := range v {
			parts = append(parts, escapeWhere(ctx, sub, glue))
		}
		return strings.Join(parts, glue)
	case []any:
		if len(v) == 0 {
			return "TRUE"
		}
		parts := make([]string, 0, len(v))
		for _, sub := range v {
			parts = append(parts, escapeWhere(ctx, sub, glue))
		}
		return strings.Join(parts, glue)
	case string:
		return whereFail(ctx, "psql: bare string condition %q is not supported; use psql.Raw() for raw SQL or map[string]any for field conditions", v)
	default:
		return escapeCtx(ctx, val)
	}
}
