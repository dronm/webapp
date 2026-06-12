package webapp

import (
	"context"
	"log/slog"
	"sync/atomic"
)

var sqlDebugEnabled atomic.Bool

// SetSQLDebug enables or disables SQL debug logging for CRUD helpers.
// It is intended for development and troubleshooting only because query
// parameters may contain sensitive application data.
func SetSQLDebug(enabled bool) {
	sqlDebugEnabled.Store(enabled)
}

// SQLDebugEnabled reports whether SQL debug logging is enabled.
func SQLDebugEnabled() bool {
	return sqlDebugEnabled.Load()
}

func logSQL(ctx context.Context, operation string, query string, args []any) {
	if !SQLDebugEnabled() {
		return
	}

	slog.Debug(
		"sql query",
		"operation", operation,
		"query", query,
		"args", args,
		"query_id", QueryIDFromContext(ctx),
	)
}
