package psql

// CILike creates a case-insensitive [Like] condition. It renders as ILIKE on
// PostgreSQL, LOWER(field) LIKE LOWER(pattern) on SQLite and a plain LIKE on
// MySQL (whose default collations are case-insensitive):
//
//	psql.CILike(psql.F("name"), "john%")
func CILike(field any, pattern string) *Like {
	return &Like{Field: field, Like: pattern, CaseInsensitive: true}
}
