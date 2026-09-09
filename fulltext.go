package psql

import (
	"fmt"
	"strings"
)

// FullTextMode selects how a full-text query string is interpreted.
type FullTextMode int

const (
	// FullTextNatural matches documents containing the words of the query
	// (PostgreSQL plainto_tsquery, MySQL NATURAL LANGUAGE MODE). Default.
	FullTextNatural FullTextMode = iota
	// FullTextBoolean accepts search-engine style operators: quoted phrases,
	// -excluded words, OR (PostgreSQL websearch_to_tsquery, MySQL BOOLEAN MODE).
	FullTextBoolean
	// FullTextPhrase matches the words of the query as a consecutive phrase
	// (PostgreSQL phraseto_tsquery, MySQL BOOLEAN MODE with the query quoted).
	FullTextPhrase
)

// FullTextOption customizes a [FullTextSearch]; pass options after the fields
// of [FullText] and [FullTextRank].
type FullTextOption func(*FullTextSearch)

// FullTextLanguage sets the PostgreSQL text search configuration used to
// parse the documents and the query ("english", "french", ...; "simple" by
// default, which does no stemming). Ignored on MySQL/MariaDB.
func FullTextLanguage(config string) FullTextOption {
	return func(f *FullTextSearch) { f.Language = config }
}

// FullTextSearch is a full-text match condition over one or more text
// columns. Create it with [FullText]; [FullTextRank] wraps it as a relevance
// score.
//
// Rendering (fields "a" and "b", query bound as $1):
//
//	PostgreSQL/CockroachDB:
//	  to_tsvector('simple', coalesce("a",'') || ' ' || coalesce("b",'')) @@ plainto_tsquery('simple', $1)
//	  (websearch_to_tsquery in boolean mode, phraseto_tsquery in phrase mode)
//	MySQL/MariaDB:
//	  MATCH("a","b") AGAINST ($1 IN NATURAL LANGUAGE MODE)
//	  (IN BOOLEAN MODE in boolean mode; phrase mode quotes the query in boolean mode)
//	SQLite: not supported (full-text search needs an FTS virtual table)
//
// Without an engine (Escape, EscapeValue) the MySQL form is used. On MySQL the
// columns need a FULLTEXT key covering exactly the same column list; on
// PostgreSQL a GIN key on the same to_tsvector expression makes the search
// indexed (see [Key] and [KeyGIN]).
type FullTextSearch struct {
	Query    string
	Fields   []any // column names (strings) or expressions
	Language string
	Mode     FullTextMode
}

// FullText creates a full-text search condition matching query against the
// given columns. fields are column names or expressions, optionally followed
// by a [FullTextMode] and [FullTextOption] values:
//
//	psql.B().Select().From("posts").Where(psql.FullText("go generics", "Title", "Body"))
//	psql.FullText(`"exact phrase" -draft`, psql.F("Body"), psql.FullTextBoolean, psql.FullTextLanguage("english"))
//
// As the value of a WHERE map the map key is the (single) column searched:
//
//	Where(map[string]any{"Body": psql.FullText("go generics")})
func FullText(query string, fields ...any) *FullTextSearch {
	f := &FullTextSearch{Query: query}
	for _, fld := range fields {
		switch v := fld.(type) {
		case FullTextMode:
			f.Mode = v
		case FullTextOption:
			v(f)
		default:
			f.Fields = append(f.Fields, fld)
		}
	}
	return f
}

// String returns the MySQL form of the condition (see EscapeValue).
func (f *FullTextSearch) String() string { return f.EscapeValue() }

// EscapeValue renders the condition without engine context, in the MySQL
// MATCH ... AGAINST form.
func (f *FullTextSearch) EscapeValue() string { return f.escapeValueCtx(nil) }

func (f *FullTextSearch) escapeValueCtx(ctx *renderContext) string {
	fields, ok := f.fieldExprs(ctx, nil)
	if !ok {
		return "FALSE"
	}
	return f.renderMatch(ctx, fields, false)
}

func (f *FullTextSearch) escapeWhereMapValue(ctx *renderContext, fld string, not bool) string {
	if f == nil {
		return whereFail(ctx, "psql: nil FullText condition")
	}
	fields, ok := f.fieldExprs(ctx, []string{fld})
	if !ok {
		return "FALSE"
	}
	return f.renderMatch(ctx, fields, not)
}

// fieldExprs renders the searched columns, or returns override when given.
func (f *FullTextSearch) fieldExprs(ctx *renderContext, override []string) ([]string, bool) {
	if override != nil {
		return override, true
	}
	if len(f.Fields) == 0 {
		ctx.errorf("psql: full-text search needs at least one field")
		return nil, false
	}
	res := make([]string, len(f.Fields))
	for i, fld := range f.Fields {
		switch v := fld.(type) {
		case string:
			res[i] = fieldName(v).EscapeValue()
		default:
			res[i] = escapeCtx(ctx, fld)
		}
	}
	return res, true
}

// config returns the PostgreSQL text search configuration literal.
func (f *FullTextSearch) config() string {
	if f.Language == "" {
		return "'simple'"
	}
	return escapeString(f.Language)
}

// pgVector renders the to_tsvector(...) document expression.
func (f *FullTextSearch) pgVector(fields []string) string {
	b := &strings.Builder{}
	b.WriteString("to_tsvector(")
	b.WriteString(f.config())
	b.WriteString(", ")
	for i, fld := range fields {
		if i > 0 {
			b.WriteString(" || ' ' || ")
		}
		b.WriteString("coalesce(")
		b.WriteString(fld)
		b.WriteString(",'')")
	}
	b.WriteByte(')')
	return b.String()
}

// pgQuery renders the tsquery expression for the search mode.
func (f *FullTextSearch) pgQuery(ctx *renderContext) string {
	fn := "plainto_tsquery"
	switch f.Mode {
	case FullTextBoolean:
		fn = "websearch_to_tsquery"
	case FullTextPhrase:
		fn = "phraseto_tsquery"
	}
	return fn + "(" + f.config() + ", " + escapeCtx(ctx, f.Query) + ")"
}

// mysqlMatch renders MATCH(...) AGAINST (...).
func (f *FullTextSearch) mysqlMatch(ctx *renderContext, fields []string) string {
	query := f.Query
	mode := " IN NATURAL LANGUAGE MODE"
	switch f.Mode {
	case FullTextBoolean:
		mode = " IN BOOLEAN MODE"
	case FullTextPhrase:
		mode = " IN BOOLEAN MODE"
		query = `"` + strings.ReplaceAll(query, `"`, " ") + `"`
	}
	return "MATCH(" + strings.Join(fields, ",") + ") AGAINST (" + escapeCtx(ctx, query) + mode + ")"
}

// renderMatch renders the match condition for the context's engine.
func (f *FullTextSearch) renderMatch(ctx *renderContext, fields []string, not bool) string {
	var res string
	switch ctx.engine() {
	case EnginePostgreSQL:
		res = f.pgVector(fields) + " @@ " + f.pgQuery(ctx)
	case EngineSQLite:
		ctx.setErr(fmt.Errorf("%w: full-text search on SQLite (use an FTS virtual table with psql.Raw)", ErrNotSupported))
		return "FALSE"
	default:
		res = f.mysqlMatch(ctx, fields)
	}
	if not {
		return "NOT (" + res + ")"
	}
	return res
}

// renderRank renders the relevance score for the context's engine.
func (f *FullTextSearch) renderRank(ctx *renderContext) string {
	fields, ok := f.fieldExprs(ctx, nil)
	if !ok {
		return "0"
	}
	switch ctx.engine() {
	case EnginePostgreSQL:
		return "ts_rank(" + f.pgVector(fields) + ", " + f.pgQuery(ctx) + ")"
	case EngineSQLite:
		ctx.setErr(fmt.Errorf("%w: full-text ranking on SQLite", ErrNotSupported))
		return "0"
	default:
		return f.mysqlMatch(ctx, fields)
	}
}

// FullTextScore is the relevance score of a full-text search, created with
// [FullTextRank]. It can be selected or used as an ORDER BY key.
type FullTextScore struct {
	Search *FullTextSearch
	order  string
}

// FullTextRank returns the relevance of each row for a full-text query, for
// use in a SELECT list or ORDER BY. It takes the same arguments as
// [FullText]; rendering is ts_rank(to_tsvector(...), plainto_tsquery(...))
// on PostgreSQL and MATCH(...) AGAINST (...) on MySQL:
//
//	rank := psql.FullTextRank("go generics", "Title", "Body")
//	psql.B().Select(psql.F("*"), rank).From("posts").
//	    Where(psql.FullText("go generics", "Title", "Body")).
//	    OrderBy(rank.Desc())
func FullTextRank(query string, fields ...any) *FullTextScore {
	return &FullTextScore{Search: FullText(query, fields...)}
}

// Desc returns the score as a descending sort key (most relevant first).
func (s *FullTextScore) Desc() SortValueable {
	return &FullTextScore{Search: s.Search, order: "DESC"}
}

// Asc returns the score as an ascending sort key.
func (s *FullTextScore) Asc() SortValueable {
	return &FullTextScore{Search: s.Search, order: "ASC"}
}

// String returns the MySQL form of the expression (see EscapeValue).
func (s *FullTextScore) String() string { return s.EscapeValue() }

// EscapeValue renders the score without engine context, in the MySQL
// MATCH ... AGAINST form.
func (s *FullTextScore) EscapeValue() string { return s.escapeValueCtx(nil) }

func (s *FullTextScore) escapeValueCtx(ctx *renderContext) string {
	if s.Search == nil {
		ctx.errorf("psql: FullTextRank without a search")
		return "0"
	}
	return s.Search.renderRank(ctx)
}

func (s *FullTextScore) sortEscapeValue() string { return s.sortEscapeValueCtx(nil) }

func (s *FullTextScore) sortEscapeValueCtx(ctx *renderContext) string {
	res := s.escapeValueCtx(ctx)
	if s.order != "" {
		return res + " " + s.order
	}
	return res
}
