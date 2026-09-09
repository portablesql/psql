package psql_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONGetRender(t *testing.T) {
	cases := []struct {
		e    psql.Engine
		want string
	}{
		{psql.EnginePostgreSQL, `SELECT "Data"->'address'->'city' FROM "t"`},
		{psql.EngineMySQL, `SELECT JSON_EXTRACT("Data",'$.address.city') FROM "t"`},
		{psql.EngineSQLite, `SELECT json_extract("Data",'$.address.city') FROM "t"`},
	}
	for _, c := range cases {
		sql, err := psql.B().Select(psql.JSONGet("Data", "address", "city")).From("t").Render(ctxForEngine(c.e))
		require.NoError(t, err, c.e)
		assert.Equal(t, c.want, sql, c.e)
	}
	// engine-neutral display form
	assert.Equal(t, `JSON_EXTRACT("Data",'$.address.city')`, psql.JSONGet("Data", "address", "city").EscapeValue())
	assert.Equal(t, `JSON_EXTRACT("Data",'$.address.city')`, psql.Escape(psql.JSONGet("Data", "address", "city")))
}

func TestJSONGetTextRender(t *testing.T) {
	cases := []struct {
		e    psql.Engine
		want string
	}{
		{psql.EnginePostgreSQL, `SELECT "Data"->'tags'->0->>'name' FROM "t"`},
		{psql.EngineMySQL, `SELECT JSON_UNQUOTE(JSON_EXTRACT("Data",'$.tags[0].name')) FROM "t"`},
		{psql.EngineSQLite, `SELECT json_extract("Data",'$.tags[0].name') FROM "t"`},
	}
	for _, c := range cases {
		sql, err := psql.B().Select(psql.JSONGetText("Data", "tags", "0", "name")).From("t").Render(ctxForEngine(c.e))
		require.NoError(t, err, c.e)
		assert.Equal(t, c.want, sql, c.e)
	}
}

func TestJSONPathEscaping(t *testing.T) {
	// quotes and spaces in keys, table-qualified field, expression field
	pg := ctxForEngine(psql.EnginePostgreSQL)
	sql, err := psql.B().Select(psql.JSONGet(psql.F("t", "Data"), "it's", "a b")).From("t").Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT "t"."Data"->'it''s'->'a b' FROM "t"`, sql)

	my := ctxForEngine(psql.EngineMySQL)
	sql, err = psql.B().Select(psql.JSONGet("Data", "it's", `a "b"`, "007")).From("t").Render(my)
	require.NoError(t, err)
	assert.Equal(t, `SELECT JSON_EXTRACT("Data",'$."it''s"."a \"b\""."007"') FROM "t"`, sql)

	// empty path: whole document
	sql, err = psql.B().Select(psql.JSONGet("Data"), psql.JSONGetText("Data")).From("t").Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT "Data","Data"#>>'{}' FROM "t"`, sql)
	sql, err = psql.B().Select(psql.JSONGet("Data")).From("t").Render(my)
	require.NoError(t, err)
	assert.Equal(t, `SELECT JSON_EXTRACT("Data",'$') FROM "t"`, sql)

	// paths are literals even in RenderArgs mode; compared values are bound
	sql, args, err := psql.B().Select().From("t").
		Where(psql.Equal(psql.JSONGetText("Data", "status"), "active")).RenderArgs(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("Data"->>'status'=$1)`, sql)
	assert.Equal(t, []any{"active"}, args)

	// usable as ORDER BY key
	sql, err = psql.B().Select().From("t").OrderBy(psql.JSONGetText("Data", "rank")).Render(my)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" ORDER BY JSON_UNQUOTE(JSON_EXTRACT("Data",'$.rank'))`, sql)

	// missing field
	_, err = psql.B().Select(psql.JSONGet(nil, "a")).From("t").Render(my)
	require.Error(t, err)
}

func TestJSONContainsRender(t *testing.T) {
	value := map[string]any{"role": "admin"}
	pg := ctxForEngine(psql.EnginePostgreSQL)
	sql, args, err := psql.B().Select().From("t").Where(psql.JSONContains("Data", value)).RenderArgs(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("Data" @> $1::jsonb)`, sql)
	assert.Equal(t, []any{`{"role":"admin"}`}, args)

	sql, err = psql.B().Select().From("t").Where(psql.JSONContains("Data", value)).Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("Data" @> '{"role":"admin"}'::jsonb)`, sql)

	my := ctxForEngine(psql.EngineMySQL)
	sql, args, err = psql.B().Select().From("t").Where(psql.JSONContains("Data", value)).RenderArgs(my)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (JSON_CONTAINS("Data",?))`, sql)
	assert.Equal(t, []any{`{"role":"admin"}`}, args)

	// pre-encoded JSON is passed through
	for _, raw := range []any{`[1,2]`, []byte(`[1,2]`), json.RawMessage(`[1,2]`)} {
		_, args, err := psql.B().Select().From("t").Where(psql.JSONContains("Data", raw)).RenderArgs(my)
		require.NoError(t, err)
		assert.Equal(t, []any{`[1,2]`}, args)
	}

	// SQLite has no containment operator
	_, err = psql.B().Select().From("t").Where(psql.JSONContains("Data", value)).Render(ctxForEngine(psql.EngineSQLite))
	require.Error(t, err)
	assert.True(t, errors.Is(err, psql.ErrNotSupported), err)

	// unmarshalable value
	_, err = psql.B().Select().From("t").Where(psql.JSONContains("Data", make(chan int))).Render(my)
	require.Error(t, err)

	// map value form, with Not
	sql, err = psql.B().Select().From("t").Where(map[string]any{
		"Data": psql.JSONContains(nil, value),
	}).Render(pg)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("Data" @> '{"role":"admin"}'::jsonb)`, sql)
	sql, err = psql.B().Select().From("t").Where(map[string]any{
		"Data": &psql.Not{V: psql.JSONContains(nil, value)},
	}).Render(my)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (NOT (JSON_CONTAINS("Data",'{"role":"admin"}')))`, sql)
}

func TestJSONHasKeyRender(t *testing.T) {
	cases := []struct {
		e    psql.Engine
		sql  string
		args []any
	}{
		{psql.EnginePostgreSQL, `SELECT * FROM "t" WHERE ("Data" ? $1)`, []any{"email"}},
		{psql.EngineMySQL, `SELECT * FROM "t" WHERE (JSON_CONTAINS_PATH("Data",'one','$.email'))`, nil},
		{psql.EngineSQLite, `SELECT * FROM "t" WHERE (json_type("Data",'$.email') IS NOT NULL)`, nil},
	}
	for _, c := range cases {
		sql, args, err := psql.B().Select().From("t").Where(psql.JSONHasKey("Data", "email")).RenderArgs(ctxForEngine(c.e))
		require.NoError(t, err, c.e)
		assert.Equal(t, c.sql, sql, c.e)
		assert.Equal(t, c.args, args, c.e)
	}
	sql, err := psql.B().Select().From("t").Where(psql.JSONHasKey("Data", "e'mail")).Render(ctxForEngine(psql.EnginePostgreSQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE ("Data" ? 'e''mail')`, sql)

	// map value form, negated
	sql, err = psql.B().Select().From("t").Where(map[string]any{
		"Data": &psql.Not{V: psql.JSONHasKey(nil, "email")},
	}).Render(ctxForEngine(psql.EngineSQLite))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (json_type("Data",'$.email') IS NULL)`, sql)
	sql, err = psql.B().Select().From("t").Where(map[string]any{
		"Data": &psql.Not{V: psql.JSONHasKey(nil, "email")},
	}).Render(ctxForEngine(psql.EngineMySQL))
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "t" WHERE (NOT JSON_CONTAINS_PATH("Data",'one','$.email'))`, sql)
}

func TestJSONSetRender(t *testing.T) {
	where := map[string]any{"ID": 1}
	set := map[string]any{"Data": psql.JSONSet("Data", []string{"prefs", "theme"}, `"dark"`)}

	cases := []struct {
		e    psql.Engine
		sql  string
		args []any
	}{
		{psql.EnginePostgreSQL, `UPDATE "t" SET "Data"=jsonb_set("Data",'{prefs,theme}',$1::jsonb,true) WHERE ("ID"=$2)`, []any{`"dark"`, 1}},
		{psql.EngineMySQL, `UPDATE "t" SET "Data"=JSON_SET("Data",'$.prefs.theme',CAST(? AS JSON)) WHERE ("ID"=?)`, []any{`"dark"`, 1}},
		{psql.EngineSQLite, `UPDATE "t" SET "Data"=json_set("Data",'$.prefs.theme',json(?)) WHERE ("ID"=?)`, []any{`"dark"`, 1}},
	}
	for _, c := range cases {
		sql, args, err := psql.B().Update("t").Set(set).Where(where).RenderArgs(ctxForEngine(c.e))
		require.NoError(t, err, c.e)
		assert.Equal(t, c.sql, sql, c.e)
		assert.Equal(t, c.args, args, c.e)
	}

	// non-parameterized rendering, marshaled value, quoted path element and array index
	sql, err := psql.B().Update("t").Set(map[string]any{
		"Data": psql.JSONSet("Data", []string{"a b", "0"}, map[string]int{"n": 1}),
	}).Where(where).Render(ctxForEngine(psql.EnginePostgreSQL))
	require.NoError(t, err)
	assert.Equal(t, `UPDATE "t" SET "Data"=jsonb_set("Data",'{"a b",0}','{"n":1}'::jsonb,true) WHERE ("ID"=1)`, sql)

	sql, err = psql.B().Update("t").Set(map[string]any{
		"Data": psql.JSONSet("Data", []string{"a b", "0"}, map[string]int{"n": 1}),
	}).Where(where).Render(ctxForEngine(psql.EngineMySQL))
	require.NoError(t, err)
	assert.Equal(t, `UPDATE "t" SET "Data"=JSON_SET("Data",'$."a b"[0]',CAST('{"n":1}' AS JSON)) WHERE ("ID"=1)`, sql)

	assert.Equal(t, `JSON_SET("Data",'$.a',CAST('1' AS JSON))`, psql.JSONSet("Data", []string{"a"}, 1).EscapeValue())
}
