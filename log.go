package psql

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
)

// Logger is compatible with go's slog.Logger
type Logger interface {
	DebugContext(ctx context.Context, msg string, args ...any)
}

var logOutput Logger

// SetLogger sets a global logger for debugging psql
// This can be called easily as follows using go's slog package:
//
// psql.SetLogger(slog.Default())
//
// Or a better option:
//
// psql.SetLogger(slog.Default().With("backend", "psql")) etc...
//
// The debug logger receives every query executed together with its
// arguments, and a stack trace for each failed query. Query arguments may
// contain sensitive data: only enable it where that is acceptable.
func SetLogger(l Logger) {
	logOutput = l
}

func debugLog(ctx context.Context, msg string, args ...any) {
	if d := logOutput; d != nil {
		// do not add prefix here as it can be configured by the log package
		d.DebugContext(ctx, fmt.Sprintf(msg, args...), "event", "psql:debug")
	}
}

// logQueryError reports a failed query: one slog.Error line on the default
// logger (query text and error, no stack trace), and the stack trace on the
// debug logger set with SetLogger, if any.
func logQueryError(ctx context.Context, event, table, query string, err error) {
	msg := "psql: " + err.Error()
	if query != "" {
		msg = "psql: " + query + ": " + err.Error()
	}
	slog.ErrorContext(ctx, msg, "event", event, "psql.table", table)
	if logOutput != nil {
		debugLog(ctx, "%s failed: %s\n%s", event, err, debugStack())
	}
}

// debugStack returns a formatted stack trace of the goroutine that calls it.
// It calls runtime.Stack with a large enough buffer to capture the entire trace.
func debugStack() string {
	buf := make([]byte, 1024)
	for {
		n := runtime.Stack(buf, false)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}
