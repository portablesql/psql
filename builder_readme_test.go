package psql_test

import (
	"context"
	"testing"
	"time"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReadmeQueryBuilderExamples tests all query builder examples from README.md
func TestReadmeQueryBuilderExamples(t *testing.T) {
	ctx := context.Background()

	t.Run("Basic Usage", func(t *testing.T) {
		// Simple SELECT
		query := psql.B().Select("name", "email").From("users")
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT "name","email" FROM "users"`, sql)

		// SELECT with WHERE clause
		query = psql.B().Select().From("users").Where(map[string]any{"status": "active"})
		sql, err = query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users" WHERE ("status"='active')`, sql)

		// UPDATE
		query = psql.B().Update("users").Set(map[string]any{"status": "inactive"}).Where(map[string]any{"id": 123})
		sql, err = query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `UPDATE "users" SET`)
		assert.Contains(t, sql, `"status"='inactive'`)
		assert.Contains(t, sql, `WHERE ("id"=123)`)

		// DELETE
		query = psql.B().Delete().From("users").Where(map[string]any{"id": 123})
		sql, err = query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `DELETE FROM "users" WHERE ("id"=123)`, sql)

		// Note: INSERT with query builder is typically used with actual objects
		// The simple form shown in README needs correction
	})

	t.Run("WHERE Conditions", func(t *testing.T) {
		// Equality
		query := psql.B().Select().From("users").Where(map[string]any{"id": 123})
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users" WHERE ("id"=123)`, sql)

		// Using comparison operators
		query = psql.B().Select().From("users").Where(psql.Gte(psql.F("age"), 18))
		sql, err = query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users" WHERE ("age">=18)`, sql)

		// LIKE operator
		query = psql.B().Select().From("users").Where(&psql.Like{Field: psql.F("name"), Like: "John%"})
		sql, err = query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users" WHERE ("name" LIKE 'John%' ESCAPE '\')`, sql)

		// Note: IN operator is not directly available, use WhereOR instead
		query = psql.B().Select().From("users").Where(map[string]any{
			"status": psql.WhereOR{"active", "pending"},
		})
		sql, err = query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users" WHERE (("status"='active' OR "status"='pending'))`, sql)

		// IS NULL
		query = psql.B().Select().From("users").Where(map[string]any{"deleted_at": nil})
		sql, err = query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users" WHERE ("deleted_at" IS NULL)`, sql)

		// Complex conditions with OR
		query = psql.B().Select().From("users").Where(map[string]any{
			"status": psql.WhereOR{"active", "pending"},
		})
		sql, err = query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users" WHERE (("status"='active' OR "status"='pending'))`, sql)

		// Multiple conditions (AND)
		query = psql.B().Select().From("users").Where(
			psql.Equal(psql.F("status"), "active"),
			psql.Gte(psql.F("age"), 18),
		)
		sql, err = query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users" WHERE ("status"='active') AND ("age">=18)`, sql)
	})

	t.Run("Comparison Operators", func(t *testing.T) {
		tests := []struct {
			name     string
			query    *psql.QueryBuilder
			expected string
		}{
			{
				name:     "Equal",
				query:    psql.B().Select().From("users").Where(psql.Equal(psql.F("id"), 1)),
				expected: `SELECT * FROM "users" WHERE ("id"=1)`,
			},
			{
				name:     "Lt",
				query:    psql.B().Select().From("users").Where(psql.Lt(psql.F("age"), 18)),
				expected: `SELECT * FROM "users" WHERE ("age"<18)`,
			},
			{
				name:     "Lte",
				query:    psql.B().Select().From("users").Where(psql.Lte(psql.F("age"), 18)),
				expected: `SELECT * FROM "users" WHERE ("age"<=18)`,
			},
			{
				name:     "Gt",
				query:    psql.B().Select().From("users").Where(psql.Gt(psql.F("age"), 18)),
				expected: `SELECT * FROM "users" WHERE ("age">18)`,
			},
			{
				name:     "Gte",
				query:    psql.B().Select().From("users").Where(psql.Gte(psql.F("age"), 18)),
				expected: `SELECT * FROM "users" WHERE ("age">=18)`,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				sql, err := tt.query.Render(ctx)
				require.NoError(t, err)
				assert.Equal(t, tt.expected, sql)
			})
		}
	})

	t.Run("Advanced Features", func(t *testing.T) {
		// ORDER BY and LIMIT
		query := psql.B().Select().From("users").
			OrderBy(psql.S("created_at", "DESC"), psql.S("name", "ASC")).
			Limit(10, 20)
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		// Limit(offset, count) renders LIMIT count OFFSET offset on every engine
		assert.Equal(t, `SELECT * FROM "users" ORDER BY "created_at" DESC,"name" ASC LIMIT 20 OFFSET 10`, sql)

		// Raw SQL in SELECT
		query = psql.B().Select(psql.Raw("COUNT(DISTINCT user_id)")).From("orders")
		sql, err = query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT COUNT(DISTINCT user_id) FROM "orders"`, sql)

		// COUNT aggregate
		query = psql.B().Select(psql.Raw("COUNT(*)")).From("users")
		sql, err = query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT COUNT(*) FROM "users"`, sql)

		// GROUP BY with aggregate and portable timestamp arithmetic
		query = psql.B().Select("status", psql.Raw("COUNT(*) as count")).
			From("users").
			Where(psql.Gt(psql.F("created_at"), psql.DateSub(psql.Now(), 24*time.Hour)))
		sql, err = query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT "status",COUNT(*) as count FROM "users" WHERE ("created_at">NOW() - INTERVAL 1 DAY)`, sql)
	})

	t.Run("Complete Examples", func(t *testing.T) {
		// Find active users older than 18, ordered by name
		users := psql.B().
			Select("id", "name", "email").
			From("users").
			Where(
				psql.Equal(psql.F("status"), "active"),
				psql.Gt(psql.F("age"), 18),
			).
			OrderBy(psql.S("name", "ASC")).
			Limit(50)

		sql, err := users.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `SELECT "id","name","email"`)
		assert.Contains(t, sql, `FROM "users"`)
		assert.Contains(t, sql, `WHERE`)
		assert.Contains(t, sql, `"status"='active'`)
		assert.Contains(t, sql, `"age">18`)
		assert.Contains(t, sql, `ORDER BY "name" ASC`)
		assert.Contains(t, sql, `LIMIT 50`)

		// Update user's last login time
		userID := 42
		update := psql.B().
			Update("users").
			Set(map[string]any{
				"last_login":  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
				"login_count": psql.Raw("login_count + 1"),
			}).
			Where(map[string]any{"id": userID})

		sql, err = update.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `UPDATE "users"`)
		assert.Contains(t, sql, `SET`)
		assert.Contains(t, sql, `"last_login"=`)
		assert.Contains(t, sql, `"login_count"=login_count + 1`)
		assert.Contains(t, sql, `WHERE ("id"=42)`)

		// Complex search with LIKE and OR conditions
		searchTerm := "laptop"
		search := psql.B().
			Select().
			From("products").
			Where(map[string]any{
				"status":      "available",
				"name":        psql.Like{Like: "%" + searchTerm + "%"},
				"category_id": psql.WhereOR{1, 2, 3},
			}).
			OrderBy(psql.S("price", "ASC"))

		sql, err = search.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `SELECT * FROM "products"`)
		assert.Contains(t, sql, `"status"='available'`)
		assert.Contains(t, sql, `"name" LIKE '%laptop%'`)
		assert.Contains(t, sql, `"category_id"=1 OR "category_id"=2 OR "category_id"=3`)
		assert.Contains(t, sql, `ORDER BY "price" ASC`)
	})

	t.Run("Helper Functions", func(t *testing.T) {
		// Test F() - Field reference
		query := psql.B().Select(psql.F("user_id")).From("orders")
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT "user_id" FROM "orders"`, sql)

		// Test V() - Value literal
		query = psql.B().Select(psql.V("constant_value")).From("dummy")
		sql, err = query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT 'constant_value' FROM "dummy"`, sql)

		// Test S() - Sort field
		query = psql.B().Select().From("users").OrderBy(psql.S("created_at", "DESC"))
		sql, err = query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users" ORDER BY "created_at" DESC`, sql)

		// Test Raw() - Raw SQL
		query = psql.B().Select(psql.Raw("DISTINCT category")).From("products")
		sql, err = query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT DISTINCT category FROM "products"`, sql)
	})

	t.Run("Execution Methods", func(t *testing.T) {
		query := psql.B().Select().From("users").Where(map[string]any{"id": 1})

		// Test Render
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.NotEmpty(t, sql)

		// Test RenderArgs
		sqlWithArgs, args, err := query.RenderArgs(ctx)
		require.NoError(t, err)
		assert.NotEmpty(t, sqlWithArgs)
		assert.NotNil(t, args)

		// Note: RunQuery, ExecQuery, and Prepare require a database connection
		// so we're just testing that the methods exist and can be called
	})
}

// TestReadmeQueryBuilderWithArgs tests that queries work with argument placeholders
func TestReadmeQueryBuilderWithArgs(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		query        *psql.QueryBuilder
		expectedSQL  string
		expectedArgs int
	}{
		{
			name:         "Simple WHERE with args",
			query:        psql.B().Select().From("users").Where(map[string]any{"id": 123}),
			expectedSQL:  `SELECT * FROM "users" WHERE ("id"=?)`,
			expectedArgs: 1,
		},
		{
			name:         "Multiple conditions with args",
			query:        psql.B().Select().From("users").Where(map[string]any{"status": "active", "age": 18}),
			expectedSQL:  `SELECT * FROM "users" WHERE`,
			expectedArgs: 2,
		},
		{
			name: "UPDATE with args",
			query: psql.B().Update("users").
				Set(map[string]any{"name": "John", "age": 30}).
				Where(map[string]any{"id": 1}),
			expectedSQL:  `UPDATE "users" SET`,
			expectedArgs: 3,
		},
		{
			name:         "WhereOR with args",
			query:        psql.B().Select().From("users").Where(map[string]any{"status": psql.WhereOR{"active", "pending", "new"}}),
			expectedSQL:  `SELECT * FROM "users" WHERE`,
			expectedArgs: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args, err := tt.query.RenderArgs(ctx)
			require.NoError(t, err)
			assert.Contains(t, sql, tt.expectedSQL)
			assert.Len(t, args, tt.expectedArgs)
		})
	}
}

// TestReadmeQueryBuilderEdgeCases tests edge cases and error conditions
func TestReadmeQueryBuilderEdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("Empty SELECT", func(t *testing.T) {
		query := psql.B().Select().From("users")
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users"`, sql)
	})

	// Note: From() only accepts single argument, multiple tables would need JOINs

	t.Run("LIMIT without offset", func(t *testing.T) {
		query := psql.B().Select().From("users").Limit(10)
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "users" LIMIT 10`, sql)
	})

	t.Run("Complex nested conditions", func(t *testing.T) {
		query := psql.B().Select().From("users").Where(
			psql.Equal(psql.F("active"), true),
			psql.WhereOR{
				psql.Gt(psql.F("age"), 21),
				psql.Equal(psql.F("verified"), true),
			},
		)
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `SELECT * FROM "users"`)
		assert.Contains(t, sql, `WHERE`)
	})

	t.Run("Byte array values", func(t *testing.T) {
		query := psql.B().Select().From("files").Where(
			psql.Equal(psql.F("hash"), []byte{0xff, 0x00, 0xbe, 0xef}),
		)
		sql, err := query.Render(ctx)
		require.NoError(t, err)
		assert.Contains(t, sql, `x'ff00beef'`)
	})
}
