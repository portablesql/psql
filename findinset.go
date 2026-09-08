package psql

// FindInSet checks whether Value is one of the elements of the comma-separated
// list stored in Field (the format used by [Set] columns). It renders as
// FIND_IN_SET(value, field) on MySQL and as portable equivalents elsewhere:
//
//	// MySQL:      FIND_IN_SET('go',"tags")
//	// PostgreSQL: 'go' = ANY(string_to_array("tags", ','))
//	// SQLite:     (',' || "tags" || ',') LIKE '%,' || 'go' || ',%'
//
// When used as the value of a WHERE map, Field is ignored and the map key is
// used instead:
//
//	psql.B().Select().From("posts").Where(map[string]any{"tags": &psql.FindInSet{Value: "go"}})
type FindInSet struct {
	Field any
	Value string
}

// String renders the condition as non-parameterized SQL (see EscapeValue).
func (f *FindInSet) String() string {
	return f.EscapeValue()
}

// EscapeValue renders the condition as non-parameterized, engine-neutral
// (MySQL FIND_IN_SET) SQL.
func (f *FindInSet) EscapeValue() string {
	return f.escapeValueCtx(nil)
}

func (f *FindInSet) escapeValueCtx(ctx *renderContext) string {
	return renderFindInSet(ctx, escapeCtx(ctx, f.Field), escapeCtx(ctx, f.Value), false)
}
