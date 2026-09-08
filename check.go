package psql

import (
	"context"
	"fmt"
	"log/slog"
)

// check runs the backend's schema check for this table if it has not yet
// succeeded on this backend. A failure is logged and the operation proceeds
// (it will typically fail with the underlying error); the check is attempted
// again by the next operation.
func (t *TableMeta[T]) check(ctx context.Context) {
	be := GetBackend(ctx)
	if be == nil {
		// no backend: the operation will fail on DB()
		return
	}
	if err := be.checkTable(ctx, t.typ, t.bind(be)); err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("psql: failed to check table %s: %s", t.table, err), "event", "psql:table:check_error", "psql.table", t.table)
	}
}
