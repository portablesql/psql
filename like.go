package psql

// Like represents a SQL LIKE condition. Use in WHERE clauses:
//
//	psql.B().Select().From("users").Where(&psql.Like{Field: psql.F("name"), Like: "John%"})
//
// Set CaseInsensitive to true for case-insensitive matching. This renders as
// ILIKE on PostgreSQL, LIKE on MySQL (case-insensitive by default collation),
// and LOWER(field) LIKE LOWER(pattern) on SQLite.
//
// The pattern always uses backslash as the escape character (ESCAPE '\').
type Like struct {
	Field           any
	Like            string
	CaseInsensitive bool
}

// String renders the condition as non-parameterized SQL (see EscapeValue).
func (l *Like) String() string {
	return l.EscapeValue()
}

// EscapeValue renders the condition as non-parameterized, engine-neutral SQL.
func (l *Like) EscapeValue() string {
	return l.escapeValueCtx(nil)
}

func (l *Like) escapeValueCtx(ctx *renderContext) string {
	return renderLike(ctx, escapeCtx(ctx, l.Field), l.Like, l.CaseInsensitive, false)
}
