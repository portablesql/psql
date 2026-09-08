package psql

// SubTable creates a derived table (subquery) that can be used in FROM or JOIN
// positions. The alias is required and used to reference the subquery's columns:
//
//	psql.B().Select("u.id", "vc.vote_count").From("users AS u").LeftJoin(
//	    psql.SubTable(
//	        psql.B().Select("user_id", psql.Raw("COUNT(*) AS vote_count")).
//	            From("votes").GroupByFields("user_id"),
//	        "vc",
//	    ),
//	    psql.Equal(psql.F("u.id"), psql.F("vc.user_id")),
//	)
//	// → ... LEFT JOIN (SELECT "user_id",COUNT(*) AS vote_count FROM "votes" GROUP BY "user_id") AS "vc" ON ...
func SubTable(sub *QueryBuilder, alias string) EscapeTableable {
	return &subQueryTable{sub: sub, alias: alias}
}

type subQueryTable struct {
	sub   *QueryBuilder
	alias string
}

func (s *subQueryTable) EscapeTable() string {
	// Fallback for non-context rendering
	return s.escapeTableCtx(fallbackRenderContext())
}

func (s *subQueryTable) escapeTableCtx(ctx *renderContext) string {
	subSQL := s.sub.escapeValueCtx(ctx) // renders as "(SELECT ...)"
	return subSQL + " AS " + QuoteName(s.alias)
}

// escapeTableCtxable allows engine/arg-aware table rendering for subquery tables.
type escapeTableCtxable interface {
	escapeTableCtx(ctx *renderContext) string
}

// escapeTableWithCtx renders a table, using ctx-aware rendering if available.
func escapeTableWithCtx(ctx *renderContext, t EscapeTableable) string {
	if tc, ok := t.(escapeTableCtxable); ok {
		return tc.escapeTableCtx(ctx)
	}
	return t.EscapeTable()
}
