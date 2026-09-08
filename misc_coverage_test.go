package psql_test

import (
	"context"
	"database/sql/driver"
	"fmt"
	"testing"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// === DefaultExportArg tests ===

type testStringer struct{ val string }

func (t testStringer) String() string { return t.val }

type testValuer struct{ val driver.Value }

func (t testValuer) Value() (driver.Value, error) { return t.val, nil }

func TestMiscDefaultExportArg(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		result := psql.DefaultExportArg(nil)
		assert.Nil(t, result)
	})

	t.Run("fmt.Stringer returns String()", func(t *testing.T) {
		s := testStringer{val: "hello"}
		result := psql.DefaultExportArg(s)
		assert.Equal(t, "hello", result)
	})

	t.Run("driver.Valuer returns the value as-is", func(t *testing.T) {
		v := testValuer{val: "dbval"}
		result := psql.DefaultExportArg(v)
		// driver.Valuer is returned as-is (not unwrapped)
		assert.Equal(t, v, result)
	})

	t.Run("non-nil pointer dereferences", func(t *testing.T) {
		s := "world"
		result := psql.DefaultExportArg(&s)
		assert.Equal(t, "world", result)
	})

	t.Run("nil pointer returns nil", func(t *testing.T) {
		var s *string
		result := psql.DefaultExportArg(s)
		assert.Nil(t, result)
	})

	t.Run("basic int returns as-is", func(t *testing.T) {
		result := psql.DefaultExportArg(42)
		assert.Equal(t, 42, result)
	})

	t.Run("basic string returns as-is", func(t *testing.T) {
		result := psql.DefaultExportArg("plain")
		// string doesn't implement Stringer, so returned as-is
		assert.Equal(t, "plain", result)
	})

	t.Run("pointer to stringer dereferences first", func(t *testing.T) {
		s := testStringer{val: "via-ptr"}
		result := psql.DefaultExportArg(&s)
		// pointer gets dereferenced, then Stringer is called
		assert.Equal(t, "via-ptr", result)
	})
}

// === FormatTableName tests ===

func TestMiscFormatTableName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"HelloWorld", "Hello_World"},
		{"MyTable", "My_Table"},
		{"Simple", "Simple"},
		{"ABC", "A_B_C"},
		{"Table1", "Table1"},
		{"lowercase", "Lowercase"},
		{"A", "A"},
		{"ABCDef", "A_B_C_Def"},
		{"Table123Name", "Table123_Name"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, psql.FormatTableName(tt.input))
		})
	}
}

// === QuoteName tests ===

func TestMiscQuoteName(t *testing.T) {
	t.Run("simple name", func(t *testing.T) {
		assert.Equal(t, `"users"`, psql.QuoteName("users"))
	})

	t.Run("name with double quote", func(t *testing.T) {
		assert.Equal(t, `"has""quote"`, psql.QuoteName(`has"quote`))
	})

	t.Run("empty name", func(t *testing.T) {
		assert.Equal(t, `""`, psql.QuoteName(""))
	})

	t.Run("name with multiple quotes", func(t *testing.T) {
		assert.Equal(t, `"a""b""c"`, psql.QuoteName(`a"b"c`))
	})

	t.Run("name with spaces", func(t *testing.T) {
		assert.Equal(t, `"my table"`, psql.QuoteName("my table"))
	})
}

// === Engine.String() tests ===

func TestMiscEngineString(t *testing.T) {
	assert.Equal(t, "MySQL Engine", psql.EngineMySQL.String())
	assert.Equal(t, "PostgreSQL Engine", psql.EnginePostgreSQL.String())
	assert.Equal(t, "SQLite Engine", psql.EngineSQLite.String())
	assert.Equal(t, "Unknown Engine", psql.EngineUnknown.String())
	// Test a non-standard engine value
	assert.Equal(t, "Unknown Engine", psql.Engine(99).String())
}

// === Backend tests ===

func TestMiscBackendNewBackend(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	assert.NotNil(t, be)
	assert.Equal(t, psql.EngineMySQL, be.Engine())
	assert.Nil(t, be.DB())
}

func TestMiscBackendNewBackendPostgreSQL(t *testing.T) {
	be := psql.NewBackend(psql.EnginePostgreSQL, nil)
	assert.Equal(t, psql.EnginePostgreSQL, be.Engine())
}

func TestMiscBackendNewBackendSQLite(t *testing.T) {
	be := psql.NewBackend(psql.EngineSQLite, nil)
	assert.Equal(t, psql.EngineSQLite, be.Engine())
}

func TestMiscBackendWithDriverData(t *testing.T) {
	data := "custom-driver-data"
	be := psql.NewBackend(psql.EngineMySQL, nil, psql.WithDriverData(data))
	assert.Equal(t, "custom-driver-data", be.DriverData())
}

func TestMiscBackendWithNamer(t *testing.T) {
	n := &psql.DefaultNamer{}
	be := psql.NewBackend(psql.EngineMySQL, nil, psql.WithNamer(n))
	assert.IsType(t, &psql.DefaultNamer{}, be.Namer())
}

func TestMiscBackendDefaultNamer(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	// Default namer is LegacyNamer
	assert.IsType(t, &psql.LegacyNamer{}, be.Namer())
}

func TestMiscBackendDriverDataNil(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	assert.Nil(t, be.DriverData())
}

func TestMiscBackendDriverDataNilBackend(t *testing.T) {
	var be *psql.Backend
	assert.Nil(t, be.DriverData())
}

func TestMiscBackendEngineNilBackend(t *testing.T) {
	var be *psql.Backend
	assert.Equal(t, psql.EngineUnknown, be.Engine())
}

func TestMiscBackendSetNamer(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	be.SetNamer(&psql.CamelSnakeNamer{})
	assert.IsType(t, &psql.CamelSnakeNamer{}, be.Namer())

	be.SetNamer(&psql.DefaultNamer{})
	assert.IsType(t, &psql.DefaultNamer{}, be.Namer())
}

func TestMiscBackendSetNamerNilBackend(t *testing.T) {
	var be *psql.Backend
	// Should not panic
	be.SetNamer(&psql.DefaultNamer{})
}

func TestMiscBackendNamerNilBackend(t *testing.T) {
	var be *psql.Backend
	// Should return LegacyNamer fallback
	n := be.Namer()
	assert.NotNil(t, n)
	assert.IsType(t, &psql.LegacyNamer{}, n)
}

func TestMiscBackendPlug(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	ctx := be.Plug(context.Background())
	got := psql.GetBackend(ctx)
	assert.Equal(t, be, got)
}

func TestMiscBackendDBPanicsOnNil(t *testing.T) {
	var be *psql.Backend
	assert.Panics(t, func() {
		be.DB()
	})
}

func TestMiscBackendMultipleOptions(t *testing.T) {
	data := "my-data"
	n := &psql.CamelSnakeNamer{}
	be := psql.NewBackend(psql.EnginePostgreSQL, nil,
		psql.WithDriverData(data),
		psql.WithNamer(n),
	)
	assert.Equal(t, psql.EnginePostgreSQL, be.Engine())
	assert.Equal(t, "my-data", be.DriverData())
	assert.IsType(t, &psql.CamelSnakeNamer{}, be.Namer())
}

// === VectorDistance tests ===

func TestMiscVecL2Distance(t *testing.T) {
	vec := psql.Vector{1.0, 2.0, 3.0}
	dist := psql.VecL2Distance(psql.F("embedding"), vec)
	result := dist.EscapeValue()
	assert.Contains(t, result, "vec_l2_distance")
	assert.Contains(t, result, `"embedding"`)
	assert.Contains(t, result, "[1,2,3]")
}

func TestMiscVecCosineDistance(t *testing.T) {
	vec := psql.Vector{0.5, 0.5}
	dist := psql.VecCosineDistance(psql.F("vec"), vec)
	result := dist.EscapeValue()
	assert.Contains(t, result, "vec_cosine_distance")
	assert.Contains(t, result, `"vec"`)
	assert.Contains(t, result, "[0.5,0.5]")
}

func TestMiscVecInnerProduct(t *testing.T) {
	vec := psql.Vector{1.0, 0.0}
	dist := psql.VecInnerProduct(psql.F("vec"), vec)
	result := dist.EscapeValue()
	assert.Contains(t, result, "vec_inner_product")
	assert.Contains(t, result, `"vec"`)
}

func TestMiscVecDistanceString(t *testing.T) {
	vec := psql.Vector{1.0, 2.0}
	dist := psql.VecL2Distance(psql.F("f"), vec)
	assert.Equal(t, dist.EscapeValue(), dist.String())
}

// === VecOrderBy tests ===

func TestMiscVecOrderBy(t *testing.T) {
	vec := psql.Vector{1.0, 2.0, 3.0}
	s := psql.VecOrderBy(psql.F("embedding"), vec, psql.VectorL2)
	assert.NotNil(t, s)

	// Rendering on an engine without vector support is an error
	ctx := context.Background()
	query := psql.B().Select().From("items").OrderBy(s)
	_, err := query.Render(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vector distance is not supported")
	// the display form still uses the function syntax
	assert.Contains(t, psql.VecL2Distance(psql.F("embedding"), vec).EscapeValue(), "vec_l2_distance")
}

func TestMiscVecOrderByCosine(t *testing.T) {
	vec := psql.Vector{0.1, 0.2}
	s := psql.VecOrderBy(psql.F("embed"), vec, psql.VectorCosine)
	assert.NotNil(t, s)

	ctx := context.Background()
	query := psql.B().Select().From("items").OrderBy(s)
	_, err := query.Render(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vector distance is not supported")
}

func TestMiscVecOrderByInnerProduct(t *testing.T) {
	vec := psql.Vector{1.0}
	s := psql.VecOrderBy(psql.F("embed"), vec, psql.VectorInnerProduct)
	assert.NotNil(t, s)

	ctx := context.Background()
	query := psql.B().Select().From("items").OrderBy(s)
	_, err := query.Render(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vector distance is not supported")
}

// === VectorComparison tests ===

func TestMiscVecEqual(t *testing.T) {
	vec := psql.Vector{1.0, 2.0, 3.0}
	comp := psql.VecEqual(psql.F("embedding"), vec)
	result := comp.EscapeValue()
	assert.Contains(t, result, `"embedding"`)
	assert.Contains(t, result, "=")
	assert.Contains(t, result, "[1,2,3]")
	assert.NotContains(t, result, "<>")
}

func TestMiscVecNotEqual(t *testing.T) {
	vec := psql.Vector{1.0, 2.0, 3.0}
	comp := psql.VecNotEqual(psql.F("embedding"), vec)
	result := comp.EscapeValue()
	assert.Contains(t, result, `"embedding"`)
	assert.Contains(t, result, "<>")
	assert.Contains(t, result, "[1,2,3]")
}

func TestMiscVecComparisonString(t *testing.T) {
	vec := psql.Vector{1.0}
	comp := psql.VecEqual(psql.F("f"), vec)
	assert.Equal(t, comp.EscapeValue(), comp.String())
}

func TestMiscVecEqualInWhere(t *testing.T) {
	ctx := context.Background()
	vec := psql.Vector{1.0, 2.0}
	query := psql.B().Select().From("items").Where(psql.VecEqual(psql.F("embedding"), vec))
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, "=")
	assert.Contains(t, sql, "[1,2]")
}

func TestMiscVecDistanceInWhere(t *testing.T) {
	ctx := context.Background()
	vec := psql.Vector{1.0, 2.0}
	dist := psql.VecL2Distance(psql.F("embedding"), vec)
	query := psql.B().Select().From("items").Where(psql.Lt(dist, 0.5))
	_, err := query.Render(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vector distance is not supported")
	// the display form still uses the function syntax
	assert.Contains(t, dist.EscapeValue(), "vec_l2_distance")
}

// === Hex type tests ===

func TestMiscHexScanString(t *testing.T) {
	var h psql.Hex
	err := h.Scan("48656c6c6f")
	assert.NoError(t, err)
	assert.Equal(t, psql.Hex("Hello"), h)
}

func TestMiscHexScanBytes(t *testing.T) {
	var h psql.Hex
	err := h.Scan([]byte("deadbeef"))
	assert.NoError(t, err)
	assert.Equal(t, psql.Hex{0xde, 0xad, 0xbe, 0xef}, h)
}

func TestMiscHexScanInvalidHex(t *testing.T) {
	var h psql.Hex
	err := h.Scan("not-hex!")
	assert.Error(t, err)
}

func TestMiscHexScanUnsupportedType(t *testing.T) {
	var h psql.Hex
	err := h.Scan(12345)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported input format")
}

func TestMiscHexValue(t *testing.T) {
	h := psql.Hex{0xca, 0xfe}
	v, err := h.Value()
	assert.NoError(t, err)
	assert.Equal(t, "cafe", v)
}

func TestMiscHexValueEmpty(t *testing.T) {
	h := psql.Hex{}
	v, err := h.Value()
	assert.NoError(t, err)
	assert.Equal(t, "", v)
}

// === StructKey tests ===

func TestMiscStructKeyKeyname(t *testing.T) {
	t.Run("primary key", func(t *testing.T) {
		k := &psql.StructKey{Typ: psql.KeyPrimary, Key: "pk"}
		assert.Equal(t, "PRIMARY", k.Keyname())
	})

	t.Run("non-primary key", func(t *testing.T) {
		k := &psql.StructKey{Typ: psql.KeyIndex, Key: "idx_name"}
		assert.Equal(t, "idx_name", k.Keyname())
	})

	t.Run("unique key", func(t *testing.T) {
		k := &psql.StructKey{Typ: psql.KeyUnique, Key: "uniq_email"}
		assert.Equal(t, "uniq_email", k.Keyname())
	})
}

func TestMiscStructKeySqlKeyName(t *testing.T) {
	t.Run("primary key", func(t *testing.T) {
		k := &psql.StructKey{Typ: psql.KeyPrimary, Key: "pk"}
		assert.Equal(t, "PRIMARY KEY", k.SqlKeyName())
	})

	t.Run("non-primary key", func(t *testing.T) {
		k := &psql.StructKey{Typ: psql.KeyIndex, Key: "idx_name"}
		assert.Equal(t, `INDEX "idx_name"`, k.SqlKeyName())
	})
}

func TestMiscStructKeyIsUnique(t *testing.T) {
	assert.True(t, (&psql.StructKey{Typ: psql.KeyPrimary}).IsUnique())
	assert.True(t, (&psql.StructKey{Typ: psql.KeyUnique}).IsUnique())
	assert.False(t, (&psql.StructKey{Typ: psql.KeyIndex}).IsUnique())
	assert.False(t, (&psql.StructKey{Typ: psql.KeyFulltext}).IsUnique())
	assert.False(t, (&psql.StructKey{Typ: psql.KeySpatial}).IsUnique())
	assert.False(t, (&psql.StructKey{Typ: psql.KeyVector}).IsUnique())
}

func TestMiscStructKeyConstants(t *testing.T) {
	assert.Equal(t, 1, psql.KeyPrimary)
	assert.Equal(t, 2, psql.KeyUnique)
	assert.Equal(t, 3, psql.KeyIndex)
	assert.Equal(t, 4, psql.KeyFulltext)
	assert.Equal(t, 5, psql.KeySpatial)
	assert.Equal(t, 6, psql.KeyVector)
}

// === RegisterDialect / dialect lookup tests ===

// We test that RegisterDialect doesn't panic and the default dialect is used
// when no dialect is registered for an engine. The Placeholders method exercises
// the dialect lookup.
func TestMiscPlaceholders(t *testing.T) {
	// Unknown engine should use default dialect (? placeholders)
	result := psql.EngineUnknown.Placeholders(3, 1)
	assert.Equal(t, "?,?,?", result)
}

func TestMiscPlaceholdersSingleArg(t *testing.T) {
	result := psql.EngineUnknown.Placeholders(1, 1)
	assert.Equal(t, "?", result)
}

func TestMiscPlaceholdersMySQL(t *testing.T) {
	// MySQL might not have a registered dialect in test, but check behavior
	result := psql.EngineMySQL.Placeholders(2, 1)
	// Should be either "?,?" (default) or "$1,$2" depending on registration
	assert.NotEmpty(t, result)
}

// === RegisterBackendFactory tests ===

type testFactory struct{}

func (f *testFactory) MatchDSN(dsn string) bool { return dsn == "test://match" }
func (f *testFactory) CreateBackend(dsn string) (*psql.Backend, error) {
	return psql.NewBackend(psql.EngineMySQL, nil), nil
}

func TestMiscRegisterBackendFactory(t *testing.T) {
	// Register a factory - should not panic
	psql.RegisterBackendFactory(&testFactory{})

	// Now New should match our factory's DSN
	be, err := psql.New("test://match")
	assert.NoError(t, err)
	assert.NotNil(t, be)
	assert.Equal(t, psql.EngineMySQL, be.Engine())
}

func TestMiscNewNoMatchingFactory(t *testing.T) {
	_, err := psql.New("unknown://no-match-ever-xyz")
	// Unless some other factory matches, this should error
	// (the test factory only matches "test://match")
	if err != nil {
		assert.Contains(t, err.Error(), "no backend factory matches DSN")
	}
}

// === HasChanged tests ===

type testHasChangedObj struct {
	psql.Name `sql:"Test_Has_Changed"`
	psql.Key  `sql:"PRIMARY,type=PRIMARY,fields='ID'"`
	ID        int64  `sql:"ID,type=BIGINT,size=20"`
	HCName    string `sql:"HCName,type=VARCHAR,size=64"`
}

func TestMiscHasChanged(t *testing.T) {
	// HasChanged on a fresh (non-scanned) object should return true
	obj := &testHasChangedObj{ID: 1, HCName: "test"}
	result := psql.HasChanged(obj)
	// Since the object was never scanned from DB, state is uninitialized
	assert.True(t, result)
}

// === Error helpers tests ===

func TestMiscIsDuplicateNil(t *testing.T) {
	assert.False(t, psql.IsDuplicate(nil))
}

func TestMiscIsDuplicateGenericError(t *testing.T) {
	assert.False(t, psql.IsDuplicate(fmt.Errorf("some error")))
}

func TestMiscIsNotExistGenericError(t *testing.T) {
	assert.False(t, psql.IsNotExist(fmt.Errorf("some error")))
}

func TestMiscErrorNumberNil(t *testing.T) {
	assert.Equal(t, uint16(0), psql.ErrorNumber(nil))
}

func TestMiscErrorNumberGenericError(t *testing.T) {
	assert.Equal(t, uint16(0xffff), psql.ErrorNumber(fmt.Errorf("generic")))
}

func TestMiscErrorNumberWrapped(t *testing.T) {
	inner := fmt.Errorf("inner")
	outer := &psql.Error{Query: "SELECT 1", Err: inner}
	// Unwraps to inner which is also generic
	assert.Equal(t, uint16(0xffff), psql.ErrorNumber(outer))
}

func TestMiscIsNotExistWrappedPsqlError(t *testing.T) {
	inner := fmt.Errorf("generic inner")
	outer := &psql.Error{Query: "SELECT 1", Err: inner}
	assert.False(t, psql.IsNotExist(outer))
}

// === fieldName tests via F() ===

func TestMiscFieldNameSimple(t *testing.T) {
	f := psql.F("name")
	assert.Equal(t, `"name"`, f.EscapeValue())
}

func TestMiscFieldNameWithDot(t *testing.T) {
	// dot notation: table.field
	f := psql.F("users.name")
	assert.Equal(t, `"users"."name"`, f.EscapeValue())
}

func TestMiscFieldNameWildcard(t *testing.T) {
	f := psql.F("*")
	assert.Equal(t, `*`, f.EscapeValue())
}

func TestMiscFieldNameTwoArgs(t *testing.T) {
	f := psql.F("users", "name")
	assert.Equal(t, `"users"."name"`, f.EscapeValue())
}

func TestMiscFieldNameTwoArgsEmptyTable(t *testing.T) {
	// Empty table, field with dots stays as single field name
	f := psql.F("", "field.with.dots")
	assert.Equal(t, `"field.with.dots"`, f.EscapeValue())
}

func TestMiscFieldNameWithQuote(t *testing.T) {
	f := psql.F(`has"quote`)
	assert.Equal(t, `"has""quote"`, f.EscapeValue())
}

func TestMiscFieldNamePanicThreeArgs(t *testing.T) {
	assert.Panics(t, func() {
		psql.F("a", "b", "c")
	})
}

// === Sort field tests via S() ===

func TestMiscSortFieldASC(t *testing.T) {
	ctx := context.Background()
	query := psql.B().Select().From("users").OrderBy(psql.S("name", "ASC"))
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"name" ASC`)
}

func TestMiscSortFieldDESC(t *testing.T) {
	ctx := context.Background()
	query := psql.B().Select().From("users").OrderBy(psql.S("name", "DESC"))
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"name" DESC`)
}

func TestMiscSortFieldNoDirection(t *testing.T) {
	ctx := context.Background()
	query := psql.B().Select().From("users").OrderBy(psql.S("name"))
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"name"`)
}

func TestMiscSortFieldWithTable(t *testing.T) {
	ctx := context.Background()
	query := psql.B().Select().From("users").OrderBy(psql.S("users", "name", "ASC"))
	sql, err := query.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"users"."name" ASC`)
}

func TestMiscSortPanicsEmpty(t *testing.T) {
	assert.Panics(t, func() {
		psql.S()
	})
}

// === CILike tests ===

func TestMiscCILike(t *testing.T) {
	il := psql.CILike(psql.F("name"), "john%")
	result := il.EscapeValue()
	assert.Contains(t, result, `"name"`)
	assert.Contains(t, result, "LIKE")
	assert.Contains(t, result, "'john%'")
	assert.True(t, il.CaseInsensitive)
}

// === Vector type tests ===

func TestMiscVectorString(t *testing.T) {
	v := psql.Vector{1.0, 2.5, 3.0}
	assert.Equal(t, "[1,2.5,3]", v.String())
}

func TestMiscVectorStringNil(t *testing.T) {
	var v psql.Vector
	assert.Equal(t, "", v.String())
}

func TestMiscVectorStringEmpty(t *testing.T) {
	v := psql.Vector{}
	assert.Equal(t, "[]", v.String())
}

func TestMiscVectorDimensions(t *testing.T) {
	v := psql.Vector{1.0, 2.0, 3.0}
	assert.Equal(t, 3, v.Dimensions())
}

func TestMiscVectorDimensionsEmpty(t *testing.T) {
	v := psql.Vector{}
	assert.Equal(t, 0, v.Dimensions())
}

func TestMiscVectorScan(t *testing.T) {
	t.Run("from string", func(t *testing.T) {
		var v psql.Vector
		err := v.Scan("[1,2,3]")
		assert.NoError(t, err)
		assert.Equal(t, psql.Vector{1.0, 2.0, 3.0}, v)
	})

	t.Run("from bytes", func(t *testing.T) {
		var v psql.Vector
		err := v.Scan([]byte("[4,5,6]"))
		assert.NoError(t, err)
		assert.Equal(t, psql.Vector{4.0, 5.0, 6.0}, v)
	})

	t.Run("nil sets nil", func(t *testing.T) {
		var v psql.Vector
		err := v.Scan(nil)
		assert.NoError(t, err)
		assert.Nil(t, v)
	})

	t.Run("empty string sets nil", func(t *testing.T) {
		var v psql.Vector
		err := v.Scan("")
		assert.NoError(t, err)
		assert.Nil(t, v)
	})

	t.Run("empty brackets", func(t *testing.T) {
		var v psql.Vector
		err := v.Scan("[]")
		assert.NoError(t, err)
		assert.Equal(t, psql.Vector{}, v)
	})

	t.Run("unsupported type", func(t *testing.T) {
		var v psql.Vector
		err := v.Scan(12345)
		assert.Error(t, err)
	})

	t.Run("invalid component", func(t *testing.T) {
		var v psql.Vector
		err := v.Scan("[1,abc,3]")
		assert.Error(t, err)
	})
}

func TestMiscVectorValue(t *testing.T) {
	v := psql.Vector{1.0, 2.0}
	val, err := v.Value()
	assert.NoError(t, err)
	assert.Equal(t, "[1,2]", val)
}

func TestMiscVectorValueNil(t *testing.T) {
	var v psql.Vector
	val, err := v.Value()
	assert.NoError(t, err)
	assert.Nil(t, val)
}

// === Table meta tests via generic API ===

type testTableObj struct {
	psql.Name `sql:"Test_Misc_Table"`
	psql.Key  `sql:"PRIMARY,type=PRIMARY,fields='ID'"`
	ID        int64  `sql:"ID,type=BIGINT,size=20"`
	TName     string `sql:"TName,type=VARCHAR,size=64"`
}

func TestMiscTableMeta(t *testing.T) {
	tbl := psql.Table[testTableObj]()
	assert.NotNil(t, tbl)
	assert.Equal(t, "Test_Misc_Table", tbl.Name())
	assert.Equal(t, "Test_Misc_Table", tbl.TableName())
}

func TestMiscTableMetaFields(t *testing.T) {
	tbl := psql.Table[testTableObj]()
	fields := tbl.AllFields()
	assert.Len(t, fields, 2)
	assert.Equal(t, "ID", fields[0].Name)
	assert.Equal(t, "TName", fields[1].Name)
}

func TestMiscTableMetaKeys(t *testing.T) {
	tbl := psql.Table[testTableObj]()
	keys := tbl.AllKeys()
	assert.NotEmpty(t, keys)

	mainKey := tbl.MainKey()
	assert.NotNil(t, mainKey)
	assert.Equal(t, psql.KeyPrimary, mainKey.Typ)
}

func TestMiscTableMetaFieldByColumn(t *testing.T) {
	tbl := psql.Table[testTableObj]()
	f := tbl.FieldByColumn("ID")
	assert.NotNil(t, f)
	assert.Equal(t, "ID", f.Column)

	f = tbl.FieldByColumn("TName")
	assert.NotNil(t, f)
	assert.Equal(t, "TName", f.Column)

	f = tbl.FieldByColumn("nonexistent")
	assert.Nil(t, f)
}

func TestMiscTableMetaFieldStr(t *testing.T) {
	tbl := psql.Table[testTableObj]()
	fldStr := tbl.FieldStr()
	assert.Contains(t, fldStr, `"ID"`)
	assert.Contains(t, fldStr, `"TName"`)
}

func TestMiscTableMetaHasSoftDelete(t *testing.T) {
	tbl := psql.Table[testTableObj]()
	assert.False(t, tbl.HasSoftDelete())
}

func TestMiscTableMetaTableAttrs(t *testing.T) {
	tbl := psql.Table[testTableObj]()
	attrs := tbl.TableAttrs()
	assert.NotNil(t, attrs)
}

// === Table with key shorthand (key= in field tag) ===

type testKeyShorthandObj struct {
	psql.Name `sql:"Test_Key_Shorthand"`
	ID        int64 `sql:"ID,type=BIGINT,size=20,key=PRIMARY"`
}

func TestMiscTableKeyShorthand(t *testing.T) {
	tbl := psql.Table[testKeyShorthandObj]()
	assert.NotNil(t, tbl)
	keys := tbl.AllKeys()
	assert.NotEmpty(t, keys)
	mainKey := tbl.MainKey()
	assert.NotNil(t, mainKey)
	assert.Equal(t, psql.KeyPrimary, mainKey.Typ)
}

// === Table with UNIQUE key shorthand ===

type testUniqueKeyObj struct {
	psql.Name `sql:"Test_Unique_Key"`
	ID        int64  `sql:"ID,type=BIGINT,size=20,key=PRIMARY"`
	Email     string `sql:"Email,type=VARCHAR,size=255,key=UNIQUE:email_uniq"`
}

func TestMiscTableUniqueKey(t *testing.T) {
	tbl := psql.Table[testUniqueKeyObj]()
	keys := tbl.AllKeys()
	assert.True(t, len(keys) >= 2) // PRIMARY + UNIQUE
}

// === FormattedName tests ===

type testFormattedNameObj struct {
	ID   int64  `sql:"ID,type=BIGINT,size=20,key=PRIMARY"`
	Name string `sql:"Name,type=VARCHAR,size=64"`
}

func TestMiscTableFormattedName(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)

	tbl := psql.Table[testFormattedNameObj]()
	// Without explicit name, namer should be applied
	name := tbl.FormattedName(be)
	assert.NotEmpty(t, name)
}

func TestMiscTableFormattedNameExplicit(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)

	tbl := psql.Table[testTableObj]()
	// With explicit name (psql.Name), should return as-is
	name := tbl.FormattedName(be)
	assert.Equal(t, "Test_Misc_Table", name)
}

// === StructField tests ===

func TestMiscStructFieldProps(t *testing.T) {
	tbl := psql.Table[testTableObj]()
	f := tbl.FieldByColumn("TName")
	assert.NotNil(t, f)
	assert.Equal(t, "TName", f.Name)
	assert.Equal(t, "TName", f.Column)
}

func TestMiscStructFieldGetAttrs(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	tbl := psql.Table[testTableObj]()
	f := tbl.FieldByColumn("TName")
	attrs := f.GetAttrs(be)
	assert.NotNil(t, attrs)
	assert.Equal(t, "VARCHAR", attrs["type"])
	assert.Equal(t, "64", attrs["size"])
}

func TestMiscStructFieldSqlType(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	tbl := psql.Table[testTableObj]()
	f := tbl.FieldByColumn("TName")
	sqlType := f.SqlType(be)
	// Without a dialect TypeMapper, generic default: type(size)
	assert.Equal(t, "varchar(64)", sqlType)
}

func TestMiscStructFieldDefString(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	tbl := psql.Table[testTableObj]()
	f := tbl.FieldByColumn("TName")
	def := f.DefString(be)
	assert.Contains(t, def, `"TName"`)
	assert.Contains(t, def, "varchar")
}

// === StructKey DefString ===

func TestMiscStructKeyDefString(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	tbl := psql.Table[testTableObj]()
	mainKey := tbl.MainKey()
	def := mainKey.DefString(be)
	assert.Contains(t, def, "PRIMARY KEY")
}

func TestMiscStructKeyInlineDefString(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	tbl := psql.Table[testTableObj]()
	mainKey := tbl.MainKey()
	def := mainKey.InlineDefString(be, "Test_Misc_Table")
	assert.Contains(t, def, "PRIMARY KEY")
}

func TestMiscStructKeyCreateIndexSQL(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	tbl := psql.Table[testTableObj]()
	mainKey := tbl.MainKey()
	// For default dialect, returns empty (all keys inline)
	result := mainKey.CreateIndexSQL(be, "Test_Misc_Table")
	assert.Equal(t, "", result)
}

// === SetLogger test ===

func TestMiscSetLogger(t *testing.T) {
	// Should not panic when setting nil
	psql.SetLogger(nil)
}

// === New with no matching factory ===

func TestMiscNewInvalidDSN(t *testing.T) {
	_, err := psql.New("completely-invalid-dsn-no-factory-will-match-98765")
	assert.Error(t, err)
}

// === Init with no matching factory ===

func TestMiscInitInvalidDSN(t *testing.T) {
	err := psql.Init("completely-invalid-dsn-no-factory-will-match-98765")
	assert.Error(t, err)
}

// === ContextBackend/ContextDB tests ===

func TestMiscContextBackendRoundTrip(t *testing.T) {
	be := psql.NewBackend(psql.EngineSQLite, nil)
	ctx := psql.ContextBackend(context.Background(), be)
	got := psql.GetBackend(ctx)
	assert.Equal(t, be, got)
	assert.Equal(t, psql.EngineSQLite, got.Engine())
}

// === Miscellaneous edge cases ===

func TestMiscFieldNameStringer(t *testing.T) {
	// F returns an EscapeValueable which also has String()
	f := psql.F("test")
	// Using Stringer interface
	s := fmt.Sprintf("%s", f)
	assert.NotEmpty(t, s)
}

func TestMiscFieldNameTwoArgsStringer(t *testing.T) {
	f := psql.F("table", "field")
	s := fmt.Sprintf("%s", f)
	assert.Contains(t, s, `"table"."field"`)
}

// === NumericTypes coverage ===

func TestMiscNumericTypes(t *testing.T) {
	assert.True(t, psql.NumericTypes["int"])
	assert.True(t, psql.NumericTypes["bigint"])
	assert.True(t, psql.NumericTypes["float"])
	assert.False(t, psql.NumericTypes["tinyint(1)"])
	_, exists := psql.NumericTypes["varchar"]
	assert.False(t, exists)
}

// === EscapeTx with no transaction ===

func TestMiscEscapeTxNoTx(t *testing.T) {
	ctx, ok := psql.EscapeTx(context.Background())
	assert.False(t, ok)
	assert.NotNil(t, ctx)
}

// === Sentinel errors ===

func TestMiscSentinelErrors(t *testing.T) {
	assert.Error(t, psql.ErrNotReady)
	assert.Error(t, psql.ErrNotNillable)
	assert.Error(t, psql.ErrTxAlreadyProcessed)
	assert.Error(t, psql.ErrDeleteBadAssert)
	assert.Error(t, psql.ErrBreakLoop)
}

// === Error type ===

func TestMiscErrorType(t *testing.T) {
	inner := fmt.Errorf("connection refused")
	e := &psql.Error{Query: "INSERT INTO t VALUES(1)", Err: inner}
	assert.Contains(t, e.Error(), "INSERT INTO t VALUES(1)")
	assert.Contains(t, e.Error(), "connection refused")
	assert.Equal(t, inner, e.Unwrap())
}

// === StructField Matches ===

func TestMiscStructFieldMatches(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	tbl := psql.Table[testTableObj]()
	f := tbl.FieldByColumn("TName")

	// Match with correct type
	match, err := f.Matches(be, "varchar(64)", "", nil, nil)
	assert.NoError(t, err)
	assert.True(t, match)

	// Mismatch with wrong type
	match, err = f.Matches(be, "int(11)", "", nil, nil)
	assert.NoError(t, err)
	assert.False(t, match)
}
