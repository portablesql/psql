package psql_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Value-typed association fields must be accepted at registration time.
type afValueAuthor struct {
	psql.Name `sql:"af_value_author"`
	ID        int64         `sql:",key=PRIMARY"`
	Books     []afValueBook `psql:"has_many:AuthorID;order='Title DESC'"`
}

type afValueBook struct {
	psql.Name `sql:"af_value_book"`
	ID        int64         `sql:",key=PRIMARY"`
	AuthorID  *int64        `sql:",type=BIGINT"`
	Title     string        `sql:",type=VARCHAR,size=64"`
	Author    afValueAuthor `psql:"belongs_to:AuthorID"`
}

// Parent whose primary key is declared with psql.Key but without fields=.
type afNoFieldsParent struct {
	psql.Name `sql:"af_nofields_parent"`
	psql.Key  `sql:",type=PRIMARY"`
	ID        int64            `sql:",type=BIGINT"`
	Children  []*afNoFieldsKid `psql:"has_many:ParentID"`
}

type afNoFieldsKid struct {
	psql.Name `sql:"af_nofields_kid"`
	ID        int64 `sql:",key=PRIMARY"`
	ParentID  int64 `sql:",type=BIGINT"`
}

// Parent whose psql.Key fields= names a column that does not exist.
type afTypoParent struct {
	psql.Name `sql:"af_typo_parent"`
	psql.Key  `sql:",type=PRIMARY,fields=Identifier"`
	ID        int64            `sql:",type=BIGINT"`
	Children  []*afNoFieldsKid `psql:"has_many:ParentID"`
	Bad       []*afNoFieldsKid `psql:"has_many:NoSuchColumn"`
}

func TestAssocFixesValueFieldsRegister(t *testing.T) {
	// Registration must not panic and must keep both associations.
	tbl := psql.Table[afValueBook]()
	require.NotNil(t, tbl)
	require.NotNil(t, psql.Table[afValueAuthor]())

	// No targets is a no-op even for value fields.
	require.NoError(t, psql.Preload[afValueBook](context.Background(), nil, "Author"))
	// All-nil targets are skipped without touching the database.
	require.NoError(t, psql.Preload(context.Background(), []*afValueBook{nil, nil}, "Author"))
	// Books with a nil pointer FK need no query either.
	require.NoError(t, psql.Preload(context.Background(), []*afValueBook{{ID: 1}}, "Author"))
}

func TestAssocFixesMissingKeyFieldsError(t *testing.T) {
	_ = psql.Table[afNoFieldsKid]()
	parents := []*afNoFieldsParent{{ID: 1}}
	err := psql.Preload(context.Background(), parents, "Children")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fields=")
	assert.Contains(t, err.Error(), "Children")

	typo := []*afTypoParent{{ID: 1}}
	err = psql.Preload(context.Background(), typo, "Children")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"Identifier"`)
}

func TestAssocFixesUnknownForeignKeyError(t *testing.T) {
	_ = psql.Table[afNoFieldsKid]()
	typo := []*afTypoParent{{ID: 1}}
	err := psql.Preload(context.Background(), typo, "Bad")
	require.Error(t, err)
	// The parent key error comes first for this type, so use a parent with a valid key.
	assert.True(t, strings.Contains(err.Error(), "Identifier") || strings.Contains(err.Error(), "NoSuchColumn"))

	type okParent struct {
		psql.Name `sql:"af_ok_parent"`
		ID        int64            `sql:",key=PRIMARY"`
		Bad       []*afNoFieldsKid `psql:"has_many:NoSuchColumn"`
	}
	err = psql.Preload(context.Background(), []*okParent{{ID: 1}}, "Bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"NoSuchColumn"`)
	assert.Contains(t, err.Error(), "afNoFieldsKid")
}

func TestAssocFixesPreloadOptsUsesOptPreload(t *testing.T) {
	// With no field list, PreloadOpts uses opt.Preload; an unknown field errors.
	err := psql.PreloadOpts(context.Background(), []*afValueBook{{ID: 1}}, psql.WithPreload("Nope"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown association")
	// A nil option and no fields is a no-op.
	require.NoError(t, psql.PreloadOpts(context.Background(), []*afValueBook{{ID: 1}}, nil))
}

func TestAssocFixesInListQueryShape(t *testing.T) {
	// Preload issues WHERE col IN (...) with typed, parameterized values; make
	// sure the builder renders integer and binary keys as placeholders.
	be := psql.NewBackend(psql.EngineSQLite, nil)
	ctx := be.Plug(context.Background())

	q, args, err := psql.B().Select(psql.Raw("*")).From("af_value_book").
		Where(map[string]any{"AuthorID": []any{int64(1), int64(2)}}).RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, q, `"AuthorID" IN(?,?)`)
	assert.Equal(t, []any{int64(1), int64(2)}, args)

	q, args, err = psql.B().Select("TagID", "PostID").From("post_tag").
		Where(map[string]any{"TagID": []any{[]byte{1, 2}, []byte{3}}}).RenderArgs(ctx)
	require.NoError(t, err)
	assert.Contains(t, q, `"TagID" IN(?,?)`)
	assert.Equal(t, []any{[]byte{1, 2}, []byte{3}}, args)
}

func TestAssocFixesChunkSizeDefault(t *testing.T) {
	assert.Equal(t, 1000, psql.PreloadChunkSize)
}

func TestEnumValidate(t *testing.T) {
	type enumTable struct {
		psql.Name `sql:"af_enum"`
		ID        int64  `sql:",key=PRIMARY"`
		Status    string `sql:",type=enum,values='a,b,it''s'"`
		Label     string `sql:",type=VARCHAR,size=16"`
	}
	tbl := psql.Table[enumTable]()
	status := tbl.FieldByColumn("Status")
	require.NotNil(t, status)

	require.NoError(t, psql.ValidateEnum(status, "a"))
	require.NoError(t, psql.ValidateEnum(status, "b"))
	err := psql.ValidateEnum(status, "c")
	require.Error(t, err)
	assert.True(t, errors.Is(err, psql.ErrInvalidEnumValue))
	assert.Contains(t, err.Error(), "Status")

	// Non-enum fields and nil fields always validate.
	require.NoError(t, psql.ValidateEnum(tbl.FieldByColumn("Label"), "anything"))
	require.NoError(t, psql.ValidateEnum(nil, "anything"))
}

func TestEnumCheckSQLQuotesValues(t *testing.T) {
	c := &psql.EnumConstraint{
		Name:    "chk_enum_test",
		Values:  []string{"it's", "plain"},
		Columns: map[string][]string{"tbl": {"col"}},
	}
	sqlStr := psql.GenerateEnumCheckSQL(c, "tbl")
	assert.Contains(t, sqlStr, `'it''s'`)
	assert.Contains(t, sqlStr, `'plain'`)
	assert.Equal(t, "", psql.GenerateEnumCheckSQL(c, "other"))
}
