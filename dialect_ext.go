package psql

import (
	"context"
	"time"
)

// Optional dialect interfaces for advanced, engine-specific behaviour. A
// dialect implements the ones it supports; the core falls back to a portable
// rendering or to a descriptive error when an interface is missing.

// RetryableChecker reports transient transaction failures that should be
// retried from the beginning of the transaction: serialization failures
// (SQLSTATE 40001 on PostgreSQL and CockroachDB, "restart transaction" on
// CockroachDB) and deadlocks (MySQL error 1213). [Tx] retries the callback
// when the dialect reports the error as retryable.
type RetryableChecker interface {
	IsRetryable(err error) bool
}

// LockRenderer renders named (advisory) locks: MySQL GET_LOCK/RELEASE_LOCK,
// PostgreSQL pg_advisory_xact_lock. AcquireLockSQL returns a statement that
// blocks for at most timeout (a zero timeout means wait indefinitely, a
// negative one means try without waiting); its result set, when it has one,
// yields a single value that is true or 1 on success. ReleaseLockSQL may
// return "" when the lock is released automatically at transaction end.
type LockRenderer interface {
	AcquireLockSQL(name string, timeout time.Duration) (query string, args []any, err error)
	ReleaseLockSQL(name string) (query string, args []any, err error)
}

// BulkInserter loads many rows with an engine-specific bulk mechanism such as
// PostgreSQL COPY. columns are the SQL column names, rows the exported values
// in the same order. The core falls back to multi-row INSERT statements when
// the dialect does not implement this interface.
type BulkInserter interface {
	BulkInsert(ctx context.Context, be *Backend, table string, columns []string, rows [][]any) (int64, error)
}

// ReturningStatements refines [ReturningRenderer] for products that support
// RETURNING on some statements only (MariaDB: INSERT and DELETE, not UPDATE).
// stmt is "INSERT", "UPDATE" or "DELETE". Dialects that do not implement it
// are assumed to support RETURNING on all three when SupportsReturning is true.
type ReturningStatements interface {
	SupportsReturningFor(stmt string) bool
}

// VariantAware dialects can adapt rendering to the detected server product.
// Core code passes the backend's [Variant] when available.
type VariantAware interface {
	// SupportsFeature reports whether the product supports the named feature.
	// Feature names are the Feature* constants.
	SupportsFeature(v Variant, feature string) bool
}

// Feature names understood by [VariantAware.SupportsFeature] and reported by
// [Backend.Supports].
const (
	FeatureReturning       = "returning"          // INSERT/UPDATE/DELETE ... RETURNING
	FeatureAdvisoryLocks   = "advisory_locks"     // named locks (GET_LOCK, pg_advisory_xact_lock)
	FeatureListenNotify    = "listen_notify"      // PostgreSQL LISTEN/NOTIFY
	FeatureAsOfSystemTime  = "as_of_system_time"  // CockroachDB AS OF SYSTEM TIME
	FeatureRowTTL          = "row_ttl"            // CockroachDB ttl_expire_after
	FeatureDistinctOn      = "distinct_on"        // PostgreSQL DISTINCT ON
	FeatureCTE             = "cte"                // WITH ... AS (...)
	FeatureFullText        = "fulltext"           // full-text search predicates
	FeatureJSON            = "json"               // JSON path/containment operators
	FeatureVectors         = "vectors"            // vector distance operators
	FeatureIdentityColumns = "identity"           // autoinc columns
	FeatureBulkCopy        = "bulk_copy"          // native bulk load (COPY)
)

// Supports reports whether the backend's engine and product support a
// feature (see the Feature* constants). Dialects implementing [VariantAware]
// answer authoritatively; otherwise a conservative engine-level default is
// used.
func (be *Backend) Supports(feature string) bool {
	if be == nil {
		return false
	}
	if va, ok := be.Engine().dialect().(VariantAware); ok {
		return va.SupportsFeature(be.Variant(), feature)
	}
	return defaultSupports(be.Variant(), feature)
}

// defaultSupports is the engine-level fallback used when a dialect does not
// implement VariantAware.
func defaultSupports(v Variant, feature string) bool {
	switch feature {
	case FeatureCTE, FeatureJSON:
		return v != VariantUnknown
	case FeatureReturning:
		return v == VariantPostgreSQL || v == VariantCockroachDB || v == VariantSQLite || v == VariantMariaDB
	case FeatureAdvisoryLocks:
		return v == VariantPostgreSQL || v == VariantMySQL || v == VariantMariaDB
	case FeatureListenNotify:
		return v == VariantPostgreSQL
	case FeatureAsOfSystemTime, FeatureRowTTL:
		return v == VariantCockroachDB
	case FeatureDistinctOn:
		return v == VariantPostgreSQL || v == VariantCockroachDB
	case FeatureFullText:
		return v == VariantPostgreSQL || v == VariantCockroachDB || v == VariantMySQL || v == VariantMariaDB
	case FeatureVectors:
		return v == VariantPostgreSQL || v == VariantCockroachDB
	case FeatureIdentityColumns:
		return v != VariantUnknown
	case FeatureBulkCopy:
		return v == VariantPostgreSQL || v == VariantCockroachDB
	default:
		return false
	}
}
