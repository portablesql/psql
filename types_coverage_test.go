package psql_test

import (
	"context"
	"testing"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCoverageBetweenEscapeValue tests Between via direct EscapeValue calls.
func TestCoverageBetweenEscapeValue(t *testing.T) {
	t.Run("field between ints", func(t *testing.T) {
		b := psql.Between(psql.F("age"), 18, 65)
		assert.Equal(t, `"age" BETWEEN 18 AND 65`, b.EscapeValue())
	})

	t.Run("field between strings", func(t *testing.T) {
		b := psql.Between(psql.F("name"), "A", "M")
		assert.Equal(t, `"name" BETWEEN 'A' AND 'M'`, b.EscapeValue())
	})
}

// TestCoverageBetweenInWhere tests Between used as a WHERE map value.
func TestCoverageBetweenInWhere(t *testing.T) {
	ctx := context.Background()

	t.Run("between in WHERE map", func(t *testing.T) {
		q := psql.B().Select().From("products").
			Where(map[string]any{"price": psql.Between(nil, 10, 100)})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"price" BETWEEN 10 AND 100`)
	})

	t.Run("NOT between in WHERE map", func(t *testing.T) {
		q := psql.B().Select().From("products").
			Where(map[string]any{"price": &psql.Not{V: psql.Between(nil, 10, 100)}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"price" NOT BETWEEN 10 AND 100`)
	})
}

// TestCoverageComparisonEscapeValue tests all Comparison constructors via EscapeValue.
func TestCoverageComparisonEscapeValue(t *testing.T) {
	tests := []struct {
		name     string
		comp     psql.EscapeValueable
		expected string
	}{
		{"Equal fields", psql.Equal(psql.F("a"), psql.F("b")), `"a"="b"`},
		{"Gt with string", psql.Gt(psql.F("name"), "z"), `"name">'z'`},
		{"Gte with float", psql.Gte(psql.F("score"), float64(9.5)), `"score">=9.5`},
		{"Lt with zero", psql.Lt(psql.F("stock"), int64(0)), `"stock"<0`},
		{"Lte with int", psql.Lte(psql.F("count"), int64(100)), `"count"<=100`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.comp.EscapeValue())
		})
	}
}

// TestCoverageComparisonInWhere tests Comparison types used in WHERE map context.
func TestCoverageComparisonInWhere(t *testing.T) {
	ctx := context.Background()

	t.Run("Equal in WHERE map", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"age": psql.Gte(nil, int64(18))})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"age" >= 18`)
	})

	t.Run("Lt in WHERE map", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"age": psql.Lt(nil, int64(65))})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"age" < 65`)
	})

	t.Run("NOT Equal in WHERE map", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"status": &psql.Not{V: psql.Equal(nil, "banned")}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"status" != 'banned'`)
	})

	t.Run("NOT Gt becomes Lte", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"score": &psql.Not{V: psql.Gt(nil, int64(50))}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"score" <= 50`)
	})

	t.Run("NOT Gte becomes Lt", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"score": &psql.Not{V: psql.Gte(nil, int64(50))}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"score" < 50`)
	})

	t.Run("NOT Lt becomes Gte", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"score": &psql.Not{V: psql.Lt(nil, int64(50))}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"score" >= 50`)
	})

	t.Run("NOT Lte becomes Gt", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"score": &psql.Not{V: psql.Lte(nil, int64(50))}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"score" > 50`)
	})
}

// TestCoverageFindInSetEscapeValue tests FindInSet via EscapeValue and String.
func TestCoverageFindInSetEscapeValue(t *testing.T) {
	f := &psql.FindInSet{Field: psql.F("tags"), Value: "golang"}
	ev := f.EscapeValue()
	assert.Equal(t, `FIND_IN_SET('golang',"tags")`, ev)
	assert.Equal(t, ev, f.String())
}

// TestCoverageFindInSetInWhere tests FindInSet in WHERE map context.
func TestCoverageFindInSetInWhere(t *testing.T) {
	ctx := context.Background()

	t.Run("FindInSet in WHERE map", func(t *testing.T) {
		q := psql.B().Select().From("posts").
			Where(map[string]any{"tags": &psql.FindInSet{Value: "go"}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "posts" WHERE (FIND_IN_SET('go',"tags"))`, sql)
	})

	t.Run("NOT FindInSet in WHERE map", func(t *testing.T) {
		q := psql.B().Select().From("posts").
			Where(map[string]any{"tags": &psql.Not{V: &psql.FindInSet{Value: "spam"}}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `NOT FIND_IN_SET('spam',"tags")`)
	})
}

// TestCoverageIncrementDecrement tests Incr and Decr in UPDATE SET context.
func TestCoverageIncrementDecrement(t *testing.T) {
	ctx := context.Background()

	t.Run("Increment", func(t *testing.T) {
		q := psql.B().Update("counters").
			Set(map[string]any{"views": psql.Incr(int64(1))}).
			Where(map[string]any{"id": int64(42)})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"views"="views"+(1)`)
	})

	t.Run("Decrement", func(t *testing.T) {
		q := psql.B().Update("counters").
			Set(map[string]any{"stock": psql.Decr(int64(5))}).
			Where(map[string]any{"id": int64(1)})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"stock"="stock"-(5)`)
	})

	t.Run("Increment by larger value", func(t *testing.T) {
		q := psql.B().Update("stats").
			Set(map[string]any{"hits": psql.Incr(int64(100))}).
			Where(map[string]any{"page": "home"})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"hits"="hits"+(100)`)
	})
}

// TestCoverageSetRaw tests SetRaw in UPDATE SET context.
func TestCoverageSetRaw(t *testing.T) {
	ctx := context.Background()

	t.Run("SetRaw NOW()", func(t *testing.T) {
		q := psql.B().Update("users").
			Set(map[string]any{"updated_at": &psql.SetRaw{SQL: "NOW()"}}).
			Where(map[string]any{"id": int64(1)})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"updated_at"=NOW()`)
	})

	t.Run("SetRaw with expression", func(t *testing.T) {
		q := psql.B().Update("orders").
			Set(map[string]any{"total": &psql.SetRaw{SQL: `"subtotal" + "tax"`}}).
			Where(map[string]any{"id": int64(5)})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"total"="subtotal" + "tax"`)
	})
}

// TestCoverageWhereOR tests WhereOR via EscapeValue and in WHERE context.
func TestCoverageWhereOR(t *testing.T) {
	t.Run("EscapeValue with maps", func(t *testing.T) {
		w := psql.WhereOR{
			map[string]any{"status": "active"},
			map[string]any{"role": "admin"},
		}
		s := w.EscapeValue()
		assert.Contains(t, s, `"status"='active'`)
		assert.Contains(t, s, ` OR `)
		assert.Contains(t, s, `"role"='admin'`)
		assert.Equal(t, s, w.String())
	})

	t.Run("WhereOR in WHERE map repeats field", func(t *testing.T) {
		ctx := context.Background()
		q := psql.B().Select().From("users").
			Where(map[string]any{"status": psql.WhereOR{"active", "pending"}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"status"='active'`)
		assert.Contains(t, sql, ` OR `)
		assert.Contains(t, sql, `"status"='pending'`)
	})

	t.Run("WhereOR as top-level WHERE", func(t *testing.T) {
		ctx := context.Background()
		q := psql.B().Select().From("users").
			Where(psql.WhereOR{
				map[string]any{"status": "active"},
				map[string]any{"role": "admin"},
			})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"status"='active'`)
		assert.Contains(t, sql, ` OR `)
		assert.Contains(t, sql, `"role"='admin'`)
	})
}

// TestCoverageWhereAND tests WhereAND via EscapeValue and in WHERE context.
func TestCoverageWhereAND(t *testing.T) {
	t.Run("EscapeValue with maps", func(t *testing.T) {
		w := psql.WhereAND{
			map[string]any{"active": true},
			map[string]any{"verified": true},
		}
		s := w.EscapeValue()
		assert.Contains(t, s, `"active"=TRUE`)
		assert.Contains(t, s, ` AND `)
		assert.Contains(t, s, `"verified"=TRUE`)
		assert.Equal(t, s, w.String())
	})

	t.Run("WhereAND in WHERE map repeats field", func(t *testing.T) {
		ctx := context.Background()
		q := psql.B().Select().From("products").
			Where(map[string]any{"price": psql.WhereAND{
				psql.Gte(nil, int64(10)),
				psql.Lte(nil, int64(100)),
			}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"price" >= 10`)
		assert.Contains(t, sql, ` AND `)
		assert.Contains(t, sql, `"price" <= 100`)
	})

	t.Run("WhereAND as top-level WHERE", func(t *testing.T) {
		ctx := context.Background()
		q := psql.B().Select().From("users").
			Where(psql.WhereAND{
				map[string]any{"status": "active"},
				map[string]any{"verified": true},
			})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"status"='active'`)
		assert.Contains(t, sql, ` AND `)
		assert.Contains(t, sql, `"verified"=TRUE`)
	})
}

// TestCoverageCILike tests CILike constructor and rendering.
func TestCoverageCILike(t *testing.T) {
	t.Run("CILike creates case-insensitive Like", func(t *testing.T) {
		l := psql.CILike(psql.F("name"), "john%")
		assert.Contains(t, l.EscapeValue(), `LIKE`)
		assert.Contains(t, l.EscapeValue(), `'john%'`)
		assert.True(t, l.CaseInsensitive)
	})

	t.Run("CILike in WHERE map", func(t *testing.T) {
		ctx := context.Background()
		q := psql.B().Select().From("users").
			Where(map[string]any{"name": psql.CILike(nil, "john%")})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		// Without a specific engine, renders as plain LIKE
		assert.Contains(t, sql, `LIKE 'john%'`)
	})

	t.Run("Like String method", func(t *testing.T) {
		l := &psql.Like{Field: psql.F("x"), Like: "test%"}
		assert.Equal(t, l.EscapeValue(), l.String())
	})
}

// TestCoverageNotVariousTypes tests Not wrapping different types for negation.
func TestCoverageNotVariousTypes(t *testing.T) {
	ctx := context.Background()

	t.Run("Not with nil (IS NOT NULL)", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"email": &psql.Not{V: nil}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"email" IS NOT NULL`)
	})

	t.Run("Not with string value (!=)", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"role": &psql.Not{V: "guest"}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"role"!='guest'`)
	})

	t.Run("Not with Like (NOT LIKE)", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"name": &psql.Not{V: psql.Like{Like: "test%"}}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"name" NOT LIKE 'test%'`)
	})

	t.Run("Not with []any (NOT IN)", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"id": &psql.Not{V: []any{int64(1), int64(2), int64(3)}}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"id" NOT IN(1,2,3)`)
	})

	t.Run("Not with []string (NOT IN)", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"status": &psql.Not{V: []string{"banned", "deleted"}}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"status" NOT IN('banned','deleted')`)
	})

	t.Run("Not EscapeValue wraps in NOT()", func(t *testing.T) {
		n := &psql.Not{V: "something"}
		assert.Equal(t, "NOT ('something')", n.EscapeValue())
	})
}

// TestCoverageSubIn tests SubIn with a subquery in WHERE context.
func TestCoverageSubIn(t *testing.T) {
	ctx := context.Background()

	t.Run("SubIn basic", func(t *testing.T) {
		sub := psql.B().Select("user_id").From("orders")
		q := psql.B().Select().From("users").
			Where(map[string]any{"id": &psql.SubIn{Sub: sub}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"id" IN (SELECT "user_id" FROM "orders")`)
	})

	t.Run("NOT SubIn", func(t *testing.T) {
		sub := psql.B().Select("banned_id").From("bans")
		q := psql.B().Select().From("users").
			Where(map[string]any{"id": &psql.Not{V: &psql.SubIn{Sub: sub}}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"id" NOT IN (SELECT "banned_id" FROM "bans")`)
	})

	t.Run("SubIn with WHERE in subquery", func(t *testing.T) {
		sub := psql.B().Select("user_id").From("orders").
			Where(map[string]any{"status": "paid"})
		q := psql.B().Select().From("users").
			Where(map[string]any{"id": &psql.SubIn{Sub: sub}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"id" IN (SELECT "user_id" FROM "orders" WHERE ("status"='paid'))`)
	})
}

// TestCoverageScope tests WithScope creating FetchOptions with scopes.
func TestCoverageScope(t *testing.T) {
	t.Run("WithScope creates FetchOptions", func(t *testing.T) {
		scope := psql.Scope(func(q *psql.QueryBuilder) *psql.QueryBuilder {
			return q.Where(map[string]any{"active": true})
		})
		opts := psql.WithScope(scope)
		assert.NotNil(t, opts)
		assert.Len(t, opts.Scopes, 1)
	})

	t.Run("WithScope multiple scopes", func(t *testing.T) {
		s1 := psql.Scope(func(q *psql.QueryBuilder) *psql.QueryBuilder {
			return q.Where(map[string]any{"active": true})
		})
		s2 := psql.Scope(func(q *psql.QueryBuilder) *psql.QueryBuilder {
			return q.Limit(10)
		})
		opts := psql.WithScope(s1, s2)
		assert.Len(t, opts.Scopes, 2)
	})

	t.Run("Scope Apply on QueryBuilder", func(t *testing.T) {
		ctx := context.Background()
		scope := psql.Scope(func(q *psql.QueryBuilder) *psql.QueryBuilder {
			return q.Where(map[string]any{"status": "active"})
		})
		q := psql.B().Select().From("users").Apply(scope)
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"status"='active'`)
	})
}

// TestCoverageAnyType tests the Any type in WHERE context.
func TestCoverageAnyType(t *testing.T) {
	ctx := context.Background()

	t.Run("Any with slice renders IN", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"id": &psql.Any{Values: []int64{1, 2, 3}}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"id" IN(1,2,3)`)
	})

	t.Run("Any with empty slice renders FALSE", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"id": &psql.Any{Values: []int64{}}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "FALSE")
	})

	t.Run("Not Any renders NOT IN", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"id": &psql.Not{V: &psql.Any{Values: []string{"a", "b"}}}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"id" NOT IN('a','b')`)
	})

	t.Run("Any with non-slice falls back to equality", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"id": &psql.Any{Values: "single"}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"id"='single'`)
	})
}

// TestCoverageTypedSlicesInWhere tests typed slices ([]int, []int64) in WHERE map context.
func TestCoverageTypedSlicesInWhere(t *testing.T) {
	ctx := context.Background()

	t.Run("[]int64 renders IN", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"id": []int64{10, 20, 30}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"id" IN(10,20,30)`)
	})

	t.Run("NOT []int64 renders NOT IN", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"id": &psql.Not{V: []int64{10, 20, 30}}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"id" NOT IN(10,20,30)`)
	})

	t.Run("empty typed slice renders FALSE", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"id": []int64{}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "FALSE")
	})
}

// TestCoverageWhereORWithFindInSet tests WhereOR containing FindInSet values in WHERE map.
func TestCoverageWhereORWithFindInSet(t *testing.T) {
	ctx := context.Background()

	q := psql.B().Select().From("posts").
		Where(map[string]any{"tags": psql.WhereOR{
			&psql.FindInSet{Value: "go"},
			&psql.FindInSet{Value: "rust"},
		}})
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `FIND_IN_SET('go',"tags")`)
	assert.Contains(t, sql, ` OR `)
	assert.Contains(t, sql, `FIND_IN_SET('rust',"tags")`)
}

// TestCoverageWhereORWithNilValues tests WhereOR containing nil values.
func TestCoverageWhereORWithNilValues(t *testing.T) {
	ctx := context.Background()

	q := psql.B().Select().From("users").
		Where(map[string]any{"Field": psql.WhereOR{nil, psql.Lte(nil, int64(42))}})
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"Field" IS NULL`)
	assert.Contains(t, sql, ` OR `)
	assert.Contains(t, sql, `"Field" <= 42`)
}

// TestCoverageComplexWhereConditions tests combining multiple condition types in one query.
func TestCoverageComplexWhereConditions(t *testing.T) {
	ctx := context.Background()

	t.Run("multiple conditions in one WHERE map", func(t *testing.T) {
		q := psql.B().Select().From("products").
			Where(map[string]any{
				"active": true,
				"price":  psql.Gte(nil, int64(10)),
				"name":   &psql.Like{Like: "Widget%"},
			})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"active"=TRUE`)
		assert.Contains(t, sql, `"price" >= 10`)
		assert.Contains(t, sql, `"name" LIKE 'Widget%'`)
	})

	t.Run("chained WHERE calls with different types", func(t *testing.T) {
		q := psql.B().Select().From("orders").
			Where(map[string]any{"status": "pending"}).
			Where(psql.Gt(psql.F("total"), int64(100)))
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"status"='pending'`)
		assert.Contains(t, sql, `"total">100`)
		assert.Contains(t, sql, `AND`)
	})
}

// TestCoverageUpdateWithMultipleSetTypes tests an UPDATE query with Increment, Decrement, and SetRaw.
func TestCoverageUpdateWithMultipleSetTypes(t *testing.T) {
	ctx := context.Background()

	q := psql.B().Update("stats").
		Set(map[string]any{
			"hits":       psql.Incr(int64(1)),
			"misses":     psql.Decr(int64(1)),
			"updated_at": &psql.SetRaw{SQL: "NOW()"},
		}).
		Where(map[string]any{"page": "index"})
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"hits"="hits"+(1)`)
	assert.Contains(t, sql, `"misses"="misses"-(1)`)
	assert.Contains(t, sql, `"updated_at"=NOW()`)
	assert.Contains(t, sql, `WHERE`)
}

// TestCoverageSubqueryRendering tests that a subquery renders correctly when used via SubIn.
func TestCoverageSubqueryRendering(t *testing.T) {
	ctx := context.Background()
	sub := psql.B().Select("id").From("users").
		Where(map[string]any{"active": true})
	q := psql.B().Select().From("orders").
		Where(map[string]any{"user_id": &psql.SubIn{Sub: sub}})
	sql, err := q.Render(ctx)
	require.NoError(t, err)
	assert.Contains(t, sql, `"user_id" IN (SELECT "id" FROM "users" WHERE ("active"=TRUE))`)
}

// TestCoverageEmptyWhereOR tests empty/single-element WhereOR/WhereAND.
func TestCoverageEmptyWhereOR(t *testing.T) {
	t.Run("single element WhereOR", func(t *testing.T) {
		w := psql.WhereOR{map[string]any{"a": int64(1)}}
		s := w.EscapeValue()
		assert.Contains(t, s, `"a"=1`)
	})

	t.Run("single element WhereAND", func(t *testing.T) {
		w := psql.WhereAND{map[string]any{"a": int64(1)}}
		s := w.EscapeValue()
		assert.Contains(t, s, `"a"=1`)
	})
}

// TestCoverageMongoStyleWithNot tests $gt/$lt mongo-style operators in WHERE.
func TestCoverageMongoStyleWithNot(t *testing.T) {
	ctx := context.Background()

	t.Run("$lt", func(t *testing.T) {
		q := psql.B().Select().From("items").
			Where(map[string]any{"qty": map[string]any{"$lt": int64(5)}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"qty"<5`)
	})

	t.Run("$gte", func(t *testing.T) {
		q := psql.B().Select().From("items").
			Where(map[string]any{"qty": map[string]any{"$gte": int64(100)}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"qty">=100`)
	})

	t.Run("$lte", func(t *testing.T) {
		q := psql.B().Select().From("items").
			Where(map[string]any{"qty": map[string]any{"$lte": int64(0)}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"qty"<=0`)
	})

	t.Run("empty map condition is FALSE", func(t *testing.T) {
		q := psql.B().Select().From("items").
			Where(map[string]any{"qty": map[string]any{}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, "FALSE")
	})
}

// TestCoverageNullConditions tests IS NULL and IS NOT NULL in WHERE.
func TestCoverageNullConditions(t *testing.T) {
	ctx := context.Background()

	t.Run("IS NULL", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"deleted_at": nil})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"deleted_at" IS NULL`)
	})

	t.Run("IS NOT NULL", func(t *testing.T) {
		q := psql.B().Select().From("users").
			Where(map[string]any{"deleted_at": &psql.Not{V: nil}})
		sql, err := q.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `"deleted_at" IS NOT NULL`)
	})
}
