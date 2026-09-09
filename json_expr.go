package psql

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// JSON expressions. Every helper accepts the JSON column either as a column
// name string or as an expression ([F], [Raw], a subquery...). Path elements
// address object keys; elements that parse as an integer address array
// positions. Paths are rendered as escaped literals (so that the database can
// use JSON indexes), while values are bound as parameters by
// [QueryBuilder.RenderArgs].
//
// Rendering per engine (paths a.b, value bound as $1):
//
//	                 PostgreSQL                        MySQL/MariaDB                             SQLite
//	JSONGet          "f"->'a'->'b'                     JSON_EXTRACT("f",'$.a.b')                 json_extract("f",'$.a.b')
//	JSONGetText      "f"->'a'->>'b'                    JSON_UNQUOTE(JSON_EXTRACT("f",'$.a.b'))   json_extract("f",'$.a.b')
//	JSONContains     "f" @> $1::jsonb                  JSON_CONTAINS("f",$1)                     not supported
//	JSONHasKey       "f" ? $1                          JSON_CONTAINS_PATH("f",'one','$.a')       json_type("f",'$.a') IS NOT NULL
//	JSONSet          jsonb_set("f",'{a,b}',$1::jsonb,true)  JSON_SET("f",'$.a.b',CAST($1 AS JSON))  json_set("f",'$.a.b',json($1))
//
// Without an engine (Escape, EscapeValue) the MySQL form is used.

// jsonFieldExpr renders the JSON column argument of a JSON helper: a string
// is a column name, anything else is rendered as an expression.
func jsonFieldExpr(ctx *renderContext, field any) string {
	switch f := field.(type) {
	case string:
		return fieldName(f).EscapeValue()
	case nil:
		ctx.errorf("psql: JSON expression without a field")
		return "NULL"
	default:
		return escapeCtx(ctx, field)
	}
}

// jsonPathIndex reports whether a path element addresses an array position.
func jsonPathIndex(elem string) (int, bool) {
	if elem == "" || (elem[0] == '0' && len(elem) > 1) {
		return 0, false
	}
	n, err := strconv.Atoi(elem)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// jsonPathSimple reports whether a key can appear unquoted in a MySQL/SQLite
// JSON path ($.key) or a PostgreSQL array literal ({key}).
func jsonPathSimple(elem string) bool {
	if elem == "" || (elem[0] >= '0' && elem[0] <= '9') {
		return false
	}
	for _, c := range elem {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return true
}

// jsonQuoteKey double-quotes a key for use inside a JSON path or a
// PostgreSQL array literal, escaping backslashes and double quotes.
func jsonQuoteKey(elem string) string {
	elem = strings.ReplaceAll(elem, `\`, `\\`)
	elem = strings.ReplaceAll(elem, `"`, `\"`)
	return `"` + elem + `"`
}

// jsonPathString renders path as a MySQL/SQLite JSON path literal ('$.a[0].b').
func jsonPathString(path []string) string {
	b := &strings.Builder{}
	b.WriteByte('$')
	for _, elem := range path {
		if n, ok := jsonPathIndex(elem); ok {
			b.WriteByte('[')
			b.WriteString(strconv.Itoa(n))
			b.WriteByte(']')
			continue
		}
		b.WriteByte('.')
		if jsonPathSimple(elem) {
			b.WriteString(elem)
		} else {
			b.WriteString(jsonQuoteKey(elem))
		}
	}
	return escapeString(b.String())
}

// jsonPathArray renders path as a PostgreSQL text[] literal ('{a,0,"b c"}')
// for jsonb_set and the #> operators.
func jsonPathArray(path []string) string {
	parts := make([]string, len(path))
	for i, elem := range path {
		if _, ok := jsonPathIndex(elem); ok || jsonPathSimple(elem) {
			parts[i] = elem
		} else {
			parts[i] = jsonQuoteKey(elem)
		}
	}
	return escapeString("{" + strings.Join(parts, ",") + "}")
}

// jsonPathPg renders a PostgreSQL operator chain (->'a'->0->'b'), using lastOp
// for the final element (-> or ->>).
func jsonPathPg(path []string, lastOp string) string {
	b := &strings.Builder{}
	for i, elem := range path {
		if i == len(path)-1 {
			b.WriteString(lastOp)
		} else {
			b.WriteString("->")
		}
		if n, ok := jsonPathIndex(elem); ok {
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteString(escapeString(elem))
		}
	}
	return b.String()
}

// jsonValueText converts a Go value to the JSON text to send to the database:
// strings, []byte and json.RawMessage are used as they are (they must already
// be JSON), anything else is marshaled with encoding/json.
func jsonValueText(ctx *renderContext, value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case []byte:
		return string(v), true
	case json.RawMessage:
		return string(v), true
	}
	data, err := json.Marshal(value)
	if err != nil {
		ctx.errorf("psql: cannot marshal %T as JSON: %w", value, err)
		return "", false
	}
	return string(data), true
}

// jsonValueExpr renders a JSON value as a bound parameter (or literal), with
// the cast the engine needs to read it as JSON.
func jsonValueExpr(ctx *renderContext, value any) string {
	text, ok := jsonValueText(ctx, value)
	if !ok {
		return "NULL"
	}
	v := escapeCtx(ctx, text)
	switch ctx.engine() {
	case EnginePostgreSQL:
		return v + "::jsonb"
	case EngineSQLite:
		return "json(" + v + ")"
	default:
		return "CAST(" + v + " AS JSON)"
	}
}

// JSONPath extracts a value from a JSON column. Create it with [JSONGet]
// (JSON-typed result) or [JSONGetText] (text result).
type JSONPath struct {
	Field any      // column name (string) or expression
	Path  []string // object keys or array indexes, outermost first
	Text  bool     // extract as text rather than JSON
}

// JSONGet extracts the JSON value at path from a JSON column, keeping the
// JSON type (objects and arrays stay JSON, strings stay quoted on
// PostgreSQL and MySQL):
//
//	psql.JSONGet("Data", "address", "city")   // "Data"->'address'->'city'
//	psql.JSONGet(psql.F("t", "Data"), "tags", "0")   // "t"."Data"->'tags'->0
//
// Use it in SELECT lists, or compare it in a WHERE clause:
//
//	psql.Equal(psql.JSONGetText("Data", "status"), "active")
func JSONGet(field any, path ...string) *JSONPath {
	return &JSONPath{Field: field, Path: path}
}

// JSONGetText extracts the value at path from a JSON column as text: strings
// are unquoted, which makes the result comparable to plain string values.
// It renders as "f"->'a'->>'b' on PostgreSQL, JSON_UNQUOTE(JSON_EXTRACT(...))
// on MySQL and json_extract(...) on SQLite (which already returns text).
func JSONGetText(field any, path ...string) *JSONPath {
	return &JSONPath{Field: field, Path: path, Text: true}
}

// String returns the MySQL form of the expression (see EscapeValue).
func (j *JSONPath) String() string { return j.EscapeValue() }

// EscapeValue renders the expression without engine context, in the MySQL
// JSON_EXTRACT form.
func (j *JSONPath) EscapeValue() string { return j.escapeValueCtx(nil) }

func (j *JSONPath) escapeValueCtx(ctx *renderContext) string {
	fld := jsonFieldExpr(ctx, j.Field)
	switch ctx.engine() {
	case EnginePostgreSQL:
		if len(j.Path) == 0 {
			if j.Text {
				return fld + "#>>'{}'"
			}
			return fld
		}
		if j.Text {
			return fld + jsonPathPg(j.Path, "->>")
		}
		return fld + jsonPathPg(j.Path, "->")
	case EngineSQLite:
		return "json_extract(" + fld + "," + jsonPathString(j.Path) + ")"
	default:
		res := "JSON_EXTRACT(" + fld + "," + jsonPathString(j.Path) + ")"
		if j.Text {
			return "JSON_UNQUOTE(" + res + ")"
		}
		return res
	}
}

func (j *JSONPath) sortEscapeValue() string { return j.EscapeValue() }

func (j *JSONPath) sortEscapeValueCtx(ctx *renderContext) string { return j.escapeValueCtx(ctx) }

// JSONContainment is a "JSON column contains value" condition, created with
// [JSONContains].
type JSONContainment struct {
	Field any // column name (string) or expression; nil when used as a WHERE map value
	Value any // JSON text (string, []byte, json.RawMessage) or a value to marshal
}

// JSONContains matches rows whose JSON column contains value, following the
// containment semantics of PostgreSQL's @> operator and MySQL's
// JSON_CONTAINS(): every key of value must be present with the same value,
// and array elements of value must all appear in the column's array.
//
//	psql.JSONContains("Data", map[string]any{"role": "admin"})
//	// PostgreSQL: "Data" @> $1::jsonb   with $1 = {"role":"admin"}
//	// MySQL:      JSON_CONTAINS("Data",?)
//
// value is marshaled with encoding/json unless it is a string, []byte or
// json.RawMessage, which are sent as they are. SQLite has no containment
// operator: rendering fails with [ErrNotSupported].
//
// As the value of a WHERE map the map key is the column:
//
//	Where(map[string]any{"Data": psql.JSONContains(nil, value)})
func JSONContains(field any, value any) *JSONContainment {
	return &JSONContainment{Field: field, Value: value}
}

// String returns the MySQL form of the condition (see EscapeValue).
func (j *JSONContainment) String() string { return j.EscapeValue() }

// EscapeValue renders the condition without engine context, in the MySQL
// JSON_CONTAINS form.
func (j *JSONContainment) EscapeValue() string { return j.escapeValueCtx(nil) }

func (j *JSONContainment) escapeValueCtx(ctx *renderContext) string {
	return j.render(ctx, jsonFieldExpr(ctx, j.Field), false)
}

func (j *JSONContainment) escapeWhereMapValue(ctx *renderContext, fld string, not bool) string {
	if j == nil {
		return whereFail(ctx, "psql: nil JSONContains condition")
	}
	return j.render(ctx, fld, not)
}

func (j *JSONContainment) render(ctx *renderContext, fld string, not bool) string {
	var res string
	switch ctx.engine() {
	case EnginePostgreSQL:
		text, ok := jsonValueText(ctx, j.Value)
		if !ok {
			return "FALSE"
		}
		res = fld + " @> " + escapeCtx(ctx, text) + "::jsonb"
	case EngineSQLite:
		ctx.setErr(fmt.Errorf("%w: JSON containment (JSONContains) on SQLite", ErrNotSupported))
		return "FALSE"
	default:
		text, ok := jsonValueText(ctx, j.Value)
		if !ok {
			return "FALSE"
		}
		res = "JSON_CONTAINS(" + fld + "," + escapeCtx(ctx, text) + ")"
	}
	if not {
		return "NOT (" + res + ")"
	}
	return res
}

// JSONKeyCheck is a "JSON object has key" condition, created with [JSONHasKey].
type JSONKeyCheck struct {
	Field any // column name (string) or expression; nil when used as a WHERE map value
	Key   string
}

// JSONHasKey matches rows whose JSON column is an object with the given
// top-level key (or an array containing that string, on PostgreSQL):
//
//	psql.JSONHasKey("Data", "email")
//	// PostgreSQL: "Data" ? $1
//	// MySQL:      JSON_CONTAINS_PATH("Data",'one','$.email')
//	// SQLite:     json_type("Data",'$.email') IS NOT NULL
//
// As the value of a WHERE map the map key is the column:
//
//	Where(map[string]any{"Data": psql.JSONHasKey(nil, "email")})
func JSONHasKey(field any, key string) *JSONKeyCheck {
	return &JSONKeyCheck{Field: field, Key: key}
}

// String returns the MySQL form of the condition (see EscapeValue).
func (j *JSONKeyCheck) String() string { return j.EscapeValue() }

// EscapeValue renders the condition without engine context, in the MySQL
// JSON_CONTAINS_PATH form.
func (j *JSONKeyCheck) EscapeValue() string { return j.escapeValueCtx(nil) }

func (j *JSONKeyCheck) escapeValueCtx(ctx *renderContext) string {
	return j.render(ctx, jsonFieldExpr(ctx, j.Field), false)
}

func (j *JSONKeyCheck) escapeWhereMapValue(ctx *renderContext, fld string, not bool) string {
	if j == nil {
		return whereFail(ctx, "psql: nil JSONHasKey condition")
	}
	return j.render(ctx, fld, not)
}

func (j *JSONKeyCheck) render(ctx *renderContext, fld string, not bool) string {
	switch ctx.engine() {
	case EnginePostgreSQL:
		res := fld + " ? " + escapeCtx(ctx, j.Key)
		if not {
			return "NOT (" + res + ")"
		}
		return res
	case EngineSQLite:
		res := "json_type(" + fld + "," + jsonPathString([]string{j.Key}) + ")"
		if not {
			return res + " IS NULL"
		}
		return res + " IS NOT NULL"
	default:
		res := "JSON_CONTAINS_PATH(" + fld + ",'one'," + jsonPathString([]string{j.Key}) + ")"
		if not {
			return "NOT " + res
		}
		return res
	}
}

// JSONUpdate is a "JSON column with the value at path replaced" expression,
// created with [JSONSet].
type JSONUpdate struct {
	Field any      // column name (string) or expression
	Path  []string // object keys or array indexes, outermost first
	Value any      // JSON text (string, []byte, json.RawMessage) or a value to marshal
}

// JSONSet returns the JSON column with the value at path set to value,
// creating the key when it is missing. It is meant as a SET value:
//
//	psql.B().Update("users").Set(map[string]any{
//	    "Data": psql.JSONSet("Data", []string{"prefs", "theme"}, "\"dark\""),
//	}).Where(map[string]any{"ID": 1})
//	// PostgreSQL: "Data"=jsonb_set("Data",'{prefs,theme}',$1::jsonb,true)
//	// MySQL:      "Data"=JSON_SET("Data",'$.prefs.theme',CAST(? AS JSON))
//	// SQLite:     "Data"=json_set("Data",'$.prefs.theme',json(?))
//
// value is marshaled with encoding/json unless it is a string, []byte or
// json.RawMessage, which must already be JSON text (a Go string "dark" is
// therefore passed as "\"dark\""; pass a non-string type or json.Marshal the
// value to set a JSON string). On PostgreSQL the column must be jsonb.
func JSONSet(field any, path []string, value any) *JSONUpdate {
	return &JSONUpdate{Field: field, Path: path, Value: value}
}

// String returns the MySQL form of the expression (see EscapeValue).
func (j *JSONUpdate) String() string { return j.EscapeValue() }

// EscapeValue renders the expression without engine context, in the MySQL
// JSON_SET form.
func (j *JSONUpdate) EscapeValue() string { return j.escapeValueCtx(nil) }

func (j *JSONUpdate) escapeValueCtx(ctx *renderContext) string {
	fld := jsonFieldExpr(ctx, j.Field)
	switch ctx.engine() {
	case EnginePostgreSQL:
		return "jsonb_set(" + fld + "," + jsonPathArray(j.Path) + "," + jsonValueExpr(ctx, j.Value) + ",true)"
	case EngineSQLite:
		return "json_set(" + fld + "," + jsonPathString(j.Path) + "," + jsonValueExpr(ctx, j.Value) + ")"
	default:
		return "JSON_SET(" + fld + "," + jsonPathString(j.Path) + "," + jsonValueExpr(ctx, j.Value) + ")"
	}
}
