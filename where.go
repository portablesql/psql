package psql

import (
	"strings"
)

// WhereAND is a list of conditions joined with AND. It is the type of
// [QueryBuilder.WhereData] and can also be nested inside a WHERE map to
// apply several conditions to the same field:
//
//	psql.B().Select().From("t").Where(map[string]any{
//	    "age": psql.WhereAND{map[string]any{"$gte": 18}, map[string]any{"$lt": 65}},
//	})
//
// An empty WhereAND renders as TRUE.
type WhereAND []any

// WhereOR is a list of conditions joined with OR. It can be passed to
// [QueryBuilder.Where] or nested inside a WHERE map to match any of several
// values for the same field:
//
//	psql.B().Select().From("t").Where(psql.WhereOR{
//	    map[string]any{"status": "active"},
//	    map[string]any{"admin": true},
//	})
//
// An empty WhereOR renders as FALSE.
type WhereOR []any

// String renders the conditions as non-parameterized SQL (see EscapeValue).
func (w WhereAND) String() string {
	return w.EscapeValue()
}

// EscapeValue renders the conditions as non-parameterized SQL, each
// condition wrapped in parentheses and joined with AND.
func (w WhereAND) EscapeValue() string {
	return w.escapeValueCtx(nil)
}

func (w WhereAND) escapeValueCtx(ctx *renderContext) string {
	if len(w) == 0 {
		return "TRUE"
	}
	b := &strings.Builder{}

	for n, v := range w {
		if n > 0 {
			b.WriteString(" AND ")
		}
		b.WriteByte('(')
		b.WriteString(escapeWhere(ctx, v, " AND "))
		b.WriteByte(')')
	}

	return b.String()
}

// String renders the conditions as non-parameterized SQL (see EscapeValue).
func (w WhereOR) String() string {
	return w.EscapeValue()
}

// EscapeValue renders the conditions as non-parameterized SQL, each
// condition wrapped in parentheses and joined with OR.
func (w WhereOR) EscapeValue() string {
	return w.escapeValueCtx(nil)
}

func (w WhereOR) escapeValueCtx(ctx *renderContext) string {
	if len(w) == 0 {
		return "FALSE"
	}
	b := &strings.Builder{}

	for n, v := range w {
		if n > 0 {
			b.WriteString(" OR ")
		}
		b.WriteByte('(')
		b.WriteString(escapeWhere(ctx, v, " OR "))
		b.WriteByte(')')
	}

	return b.String()
}
