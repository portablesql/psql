package psql

import (
	"sort"
	"strings"
)

// renderAssignments renders a list of SET entries (as collected by
// [QueryBuilder.Set], [QueryBuilder.Insert] or [QueryBuilder.DoUpdate]) as a
// comma-separated list of "col"=value assignments. Unlike WHERE rendering,
// values are always rendered as a single value: nil becomes NULL, slices and
// [driver.Valuer] implementations ([Set], [Vector], ...) are bound as one
// value, and [Increment], [Decrement] and [SetRaw] are expanded.
//
// Entries may be map[string]any (one assignment per key, in sorted key
// order) or an expression ([EscapeValueable]) that is emitted as-is, for
// example psql.Raw(`"a"="b"+1`). Bare strings are rejected.
func renderAssignments(ctx *renderContext, entries []any) string {
	var parts []string
	for _, entry := range entries {
		switch e := entry.(type) {
		case map[string]any:
			keys := make([]string, 0, len(e))
			for k := range e {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				parts = append(parts, renderAssignment(ctx, k, e[k]))
			}
		case string:
			ctx.errorf("psql: bare string %q is not supported in a SET clause; use map[string]any or psql.Raw()", e)
		case escapeValueCtxable, EscapeValueable:
			parts = append(parts, escapeCtx(ctx, entry))
		default:
			ctx.errorf("psql: unsupported type %T in SET clause; use map[string]any", entry)
		}
	}
	if len(parts) == 0 {
		ctx.errorf("psql: no fields to set")
	}
	return strings.Join(parts, ",")
}

// renderAssignment renders a single "col"=value assignment.
func renderAssignment(ctx *renderContext, key string, val any) string {
	return fieldName(key).EscapeValue() + "=" + renderAssignmentValue(ctx, key, val)
}

// renderAssignmentValue renders the right-hand side of an assignment to key.
// It is shared by SET clauses and INSERT ... VALUES rendering.
func renderAssignmentValue(ctx *renderContext, key string, val any) string {
	switch v := val.(type) {
	case *Increment:
		if v == nil {
			return "NULL"
		}
		return fieldName(key).EscapeValue() + "+(" + escapeCtx(ctx, v.Value) + ")"
	case *Decrement:
		if v == nil {
			return "NULL"
		}
		return fieldName(key).EscapeValue() + "-(" + escapeCtx(ctx, v.Value) + ")"
	case *SetRaw:
		if v == nil {
			return "NULL"
		}
		return v.SQL
	case *Not:
		ctx.errorf("psql: psql.Not cannot be assigned to field %q", key)
		return "NULL"
	case *Any:
		if v == nil {
			return "NULL"
		}
		// Any is a WHERE helper; in an assignment the wrapped value is used directly.
		return escapeCtx(ctx, v.Values)
	default:
		return escapeCtx(ctx, val)
	}
}
