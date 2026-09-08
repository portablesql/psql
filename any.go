package psql

import (
	"reflect"

	"github.com/KarpelesLab/typutil"
)

// Any wraps a slice value for use with PostgreSQL's = ANY($1) syntax.
// On PostgreSQL with parameterized queries, it renders as = ANY($N) passing
// the slice as a single array parameter. On MySQL/SQLite (or non-parameterized),
// it expands to IN(v1,v2,...). An empty slice never matches (FALSE), a nil
// Values renders as IS NULL, and a non-slice value is compared with =.
type Any struct {
	Values any // must be a slice
}

// escapeAnyInWhere renders the ANY/IN expression for a given field key.
func escapeAnyInWhere(ctx *renderContext, key string, v *Any, not bool) string {
	fld := fieldName(key).EscapeValue()
	if v == nil || typutil.IsNil(v.Values) {
		return renderNullCheck(fld, not)
	}

	rv := reflect.ValueOf(v.Values)
	if rv.Kind() != reflect.Slice {
		// fallback: treat as equality
		if not {
			return fld + "!=" + escapeCtx(ctx, v.Values)
		}
		return fld + "=" + escapeCtx(ctx, v.Values)
	}

	if rv.Len() == 0 {
		return boolCond(false, not)
	}

	// PostgreSQL with parameterized queries: use = ANY($N)
	if ctx != nil && ctx.useArgs && ctx.e == EnginePostgreSQL {
		if not {
			return fld + " != ALL(" + ctx.appendArg(v.Values) + ")"
		}
		return fld + " = ANY(" + ctx.appendArg(v.Values) + ")"
	}

	// MySQL/SQLite or non-parameterized: expand to IN(...)
	vals := make([]any, rv.Len())
	for i := range vals {
		vals[i] = rv.Index(i).Interface()
	}
	return renderIn(ctx, fld, vals, not)
}
