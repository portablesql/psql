package psql_test

import (
	"errors"
	"testing"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFullTextRender(t *testing.T) {
	q := psql.B().Select().From("posts").Where(psql.FullText("go generics", "Title", "Body"))

	pg := ctxForEngine(psql.EnginePostgreSQL)
	sql, args, err := q.RenderArgs(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "posts" WHERE (to_tsvector('simple', coalesce("Title",'') || ' ' || coalesce("Body",'')) @@ plainto_tsquery('simple', $1))`, sql)
	assert.Equal(t, []any{"go generics"}, args)

	sql, err = q.Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "posts" WHERE (to_tsvector('simple', coalesce("Title",'') || ' ' || coalesce("Body",'')) @@ plainto_tsquery('simple', 'go generics'))`, sql)

	my := ctxForEngine(psql.EngineMySQL)
	sql, args, err = q.RenderArgs(my)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "posts" WHERE (MATCH("Title","Body") AGAINST (? IN NATURAL LANGUAGE MODE))`, sql)
	assert.Equal(t, []any{"go generics"}, args)

	// CockroachDB shares the PostgreSQL rendering
	crdb := psql.NewBackend(psql.EnginePostgreSQL, nil, psql.WithVariant(psql.VariantCockroachDB)).Plug(t.Context())
	sql, err = q.Render(crdb)
	require.NoError(t, err)
	assert.Contains(t, sql, "@@ plainto_tsquery('simple', 'go generics')")

	// SQLite needs an FTS virtual table
	_, err = q.Render(ctxForEngine(psql.EngineSQLite))
	require.Error(t, err)
	assert.True(t, errors.Is(err, psql.ErrNotSupported), err)

	// engine-neutral form
	assert.Equal(t, `MATCH("Title","Body") AGAINST ('go generics' IN NATURAL LANGUAGE MODE)`, psql.FullText("go generics", "Title", "Body").EscapeValue())
}

func TestFullTextModesAndOptions(t *testing.T) {
	pg := ctxForEngine(psql.EnginePostgreSQL)
	my := ctxForEngine(psql.EngineMySQL)

	boolean := psql.FullText(`"exact phrase" -draft`, psql.F("Body"), psql.FullTextBoolean, psql.FullTextLanguage("english"))
	sql, err := psql.B().Select().From("posts").Where(boolean).Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "posts" WHERE (to_tsvector('english', coalesce("Body",'')) @@ websearch_to_tsquery('english', '"exact phrase" -draft'))`, sql)
	sql, err = psql.B().Select().From("posts").Where(boolean).Render(my)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "posts" WHERE (MATCH("Body") AGAINST ('"exact phrase" -draft' IN BOOLEAN MODE))`, sql)

	phrase := psql.FullText(`hello "world`, "Body", psql.FullTextPhrase)
	sql, err = psql.B().Select().From("posts").Where(phrase).Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "posts" WHERE (to_tsvector('simple', coalesce("Body",'')) @@ phraseto_tsquery('simple', 'hello "world'))`, sql)
	sql, args, err := psql.B().Select().From("posts").Where(phrase).RenderArgs(my)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "posts" WHERE (MATCH("Body") AGAINST (? IN BOOLEAN MODE))`, sql)
	assert.Equal(t, []any{`"hello  world"`}, args)

	// language literal is escaped
	sql, err = psql.B().Select().From("posts").Where(psql.FullText("x", "Body", psql.FullTextLanguage("it's"))).Render(pg)
	require.NoError(t, err)
	assert.Contains(t, sql, `to_tsvector('it''s',`)

	// the struct can be built directly
	direct := &psql.FullTextSearch{Query: "x", Fields: []any{"Body"}, Mode: psql.FullTextBoolean}
	assert.Equal(t, `MATCH("Body") AGAINST ('x' IN BOOLEAN MODE)`, direct.EscapeValue())

	// no fields is an error
	_, err = psql.B().Select().From("posts").Where(psql.FullText("x")).Render(pg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one field")
}

func TestFullTextWhereMapValue(t *testing.T) {
	// the map key is the searched column
	sql, err := psql.B().Select().From("posts").Where(map[string]any{
		"Body": psql.FullText("go", psql.FullTextBoolean),
	}).Render(ctxForEngine(psql.EnginePostgreSQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "posts" WHERE (to_tsvector('simple', coalesce("Body",'')) @@ websearch_to_tsquery('simple', 'go'))`, sql)

	sql, err = psql.B().Select().From("posts").Where(map[string]any{
		"Body": &psql.Not{V: psql.FullText("go")},
	}).Render(ctxForEngine(psql.EngineMySQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "posts" WHERE (NOT (MATCH("Body") AGAINST ('go' IN NATURAL LANGUAGE MODE)))`, sql)
}

func TestFullTextRankRender(t *testing.T) {
	rank := psql.FullTextRank("go generics", "Title", "Body")
	q := psql.B().Select(psql.F("ID"), rank).From("posts").
		Where(psql.FullText("go generics", "Title", "Body")).
		OrderBy(rank.Desc())

	pg := ctxForEngine(psql.EnginePostgreSQL)
	sql, args, err := q.RenderArgs(pg)
	require.NoError(t, err)
	vec := `to_tsvector('simple', coalesce("Title",'') || ' ' || coalesce("Body",''))`
	assert.Equal(t, `SELECT "ID",ts_rank(`+vec+`, plainto_tsquery('simple', $1)) FROM "posts" WHERE (`+vec+` @@ plainto_tsquery('simple', $2)) ORDER BY ts_rank(`+vec+`, plainto_tsquery('simple', $3)) DESC`, sql)
	assert.Equal(t, []any{"go generics", "go generics", "go generics"}, args)

	my := ctxForEngine(psql.EngineMySQL)
	sql, err = q.Render(my)
	require.NoError(t, err)
	match := `MATCH("Title","Body") AGAINST ('go generics' IN NATURAL LANGUAGE MODE)`
	assert.Equal(t, `SELECT "ID",`+match+` FROM "posts" WHERE (`+match+`) ORDER BY `+match+` DESC`, sql)

	// plain (no direction) and ascending sort keys
	sql, err = psql.B().Select().From("posts").OrderBy(rank, rank.Asc()).Render(my)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "posts" ORDER BY `+match+`,`+match+` ASC`, sql)

	_, err = psql.B().Select(rank).From("posts").Render(ctxForEngine(psql.EngineSQLite))
	require.Error(t, err)
	assert.True(t, errors.Is(err, psql.ErrNotSupported), err)

	assert.Equal(t, match, rank.EscapeValue())
	assert.Equal(t, "0", (&psql.FullTextScore{}).EscapeValue())
}

// --- GIN / GIST keys ---

type ginKeyTable struct {
	psql.Name `sql:"gin_key_table"`
	ID        int64    `sql:",key=PRIMARY"`
	Data      string   `sql:",type=JSON"`
	Title     string   `sql:",type=VARCHAR,size=255"`
	DataIdx   psql.Key `sql:",type=GIN,fields='Data'"`
	TextIdx   psql.Key `sql:",type=GIN,expression=\"to_tsvector('simple', {Title})\""`
	RangeIdx  psql.Key `sql:",type=GIST,fields='ID,Title'"`
	Combined  psql.Key `sql:",type=gist,fields='Title',expression=\"lower({Title})\""`
	TextKey   psql.Key `sql:",type=FULLTEXT,fields='Title'"`
}

func TestKeyGINGIST(t *testing.T) {
	keys := map[string]*psql.StructKey{}
	for _, k := range psql.Table[ginKeyTable]().AllKeys() {
		keys[k.Name] = k
	}

	data := keys["DataIdx"]
	require.NotNil(t, data)
	assert.Equal(t, psql.KeyGIN, data.Typ)
	assert.Equal(t, []string{"Data"}, data.Fields)
	assert.Equal(t, "", data.Expression())
	assert.False(t, data.IsUnique())

	text := keys["TextIdx"]
	require.NotNil(t, text)
	assert.Equal(t, psql.KeyGIN, text.Typ)
	assert.Nil(t, text.Fields, "expression keys have no column list")
	assert.Equal(t, `to_tsvector('simple', "Title")`, text.Expression())

	rng := keys["RangeIdx"]
	require.NotNil(t, rng)
	assert.Equal(t, psql.KeyGIST, rng.Typ)
	assert.Equal(t, []string{"ID", "Title"}, rng.Fields)

	// type is case-insensitive; fields and expression may both be given
	comb := keys["Combined"]
	require.NotNil(t, comb)
	assert.Equal(t, psql.KeyGIST, comb.Typ)
	assert.Equal(t, []string{"Title"}, comb.Fields)
	assert.Equal(t, `lower("Title")`, comb.Expression())

	// the generic (inline, MySQL-like) rendering leaves engine-specific
	// index methods to the drivers
	be := psql.NewBackend(psql.EngineMySQL, nil)
	assert.Equal(t, "", data.DefString(be))
	assert.Equal(t, "", text.DefString(be))
	assert.Equal(t, "", rng.DefString(be))
	assert.Equal(t, `FULLTEXT INDEX "TextKey"("Title")`, keys["TextKey"].DefString(be))
	assert.Equal(t, `INDEX "TextIdx"`, text.SqlKeyName())
}

func TestKeyExpressionPlaceholders(t *testing.T) {
	k := &psql.StructKey{Attrs: map[string]string{"expression": `coalesce({a "b"}, {c}) || '{'`}}
	assert.Equal(t, `coalesce("a ""b""", "c") || '{'`, k.Expression())
	k = &psql.StructKey{Attrs: map[string]string{"expression": `lower({x`}}
	assert.Equal(t, `lower({x`, k.Expression())
	k = &psql.StructKey{}
	assert.Equal(t, "", k.Expression())
}
