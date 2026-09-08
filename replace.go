package psql

import (
	"context"
)

// Replace performs an upsert operation: inserts the record if it doesn't exist, or
// replaces it if a conflicting key exists. On MySQL this uses REPLACE INTO, on
// PostgreSQL it uses INSERT ... ON CONFLICT DO UPDATE, on SQLite INSERT OR REPLACE.
// Fires [BeforeSaveHook] and [AfterSaveHook] if implemented.
func Replace[T any](ctx context.Context, target ...*T) error {
	if len(target) == 0 {
		return nil
	}

	return Table[T]().Replace(ctx, target...)
}

// Replace inserts or replaces the given objects. See [Replace]. Query
// failures are returned as an [*Error].
func (t *TableMeta[T]) Replace(ctx context.Context, targets ...*T) error {
	return t.insertRows(ctx, insertReplace, targets)
}
