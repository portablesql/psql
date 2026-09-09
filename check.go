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
	if be.Engine() != EngineSQLite {
		// Run DDL outside any transaction carried by ctx: MySQL commits
		// implicitly on DDL (destroying savepoints) and PostgreSQL would roll a
		// freshly created table back with the transaction while it stays marked
		// as checked. SQLite keeps the check inside the transaction because an
		// in-memory database has a single connection.
		if outer, ok := EscapeTx(ctx); ok {
			ctx = outer
		}
	}
	if err := be.checkTable(ctx, t.typ, t.bind(be)); err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("psql: failed to check table %s: %s", t.table, err), "event", "psql:table:check_error", "psql.table", t.table)
	}
}
