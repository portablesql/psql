package psql

import (
	"errors"
	"fmt"
	"os"
)

// Error wraps a SQL error with the query that caused it. It implements the
// error and Unwrap interfaces for use with errors.Is and errors.As.
type Error struct {
	Query string
	Err   error
}

// Unwrap returns the underlying database error, for use with errors.Is and errors.As.
func (e *Error) Unwrap() error {
	return e.Err
}

// Error returns the error message, including the query that failed.
func (e *Error) Error() string {
	return fmt.Sprintf("While running %s: %s", e.Query, e.Err)
}

// IsNotExist returns true if the error is relative to a table not existing.
//
// See: https://mariadb.com/kb/en/mariadb-error-codes/
//
// Example:
// Error 1146: Table 'test.Test_Table1' doesn't exist
func IsNotExist(err error) bool {
	// First check registered ErrorClassifiers
	for _, d := range dialects {
		if ec, ok := d.(ErrorClassifier); ok {
			if ec.IsNotExist(err) {
				return true
			}
		}
	}

	// Fallback to error number check
	switch ErrorNumber(err) {
	case 1008: // Can't drop database '%s'; database doesn't exist
	case 1029: // View '%s' doesn't exist for '%s'
	case 1049: // Unknown database '%s'
	case 1051: // Unknown table '%s'
	case 1054: // Unknown column '%s' in '%s'
	case 1072: // Key column '%s' doesn't exist in table
	case 1091: // Can't DROP '%s'; check that column/key exists
	case 1109: // Unknown table '%s' in %s
	case 1141: // There is no such grant defined for user '%s' on host '%s'
	case 1146: // Table '%s.%s' doesn't exist
	case 1147: // There is no such grant defined for user '%s' on host '%s' on table '%s'
	case 1176: // Key '%s' doesn't exist in table '%s'
	case 1305: // %s %s does not exist
	case 1360: // Trigger does not exist
	case 1431: // The foreign data source you are trying to reference does not exist. Data source error: %s
	case 1449: // The user specified as a definer ('%s'@'%s') does not exist
	case 1477: // The foreign server name you are trying to reference does not exist. Data source error: %s
	case 1539: // Unknown event '%s'
	case 1630: // FUNCTION %s does not exist. Check the 'Function Name Parsing and Resolution' section in the Reference Manual
	case 1749: // partition '%s' doesn't exist
	case 1974: // Can't drop user '%-.64s'@'%-.64s'; it doesn't exist
	case 1976: // Can't drop role '%-.64s'; it doesn't exist
	case 4031: // Referenced trigger '%s' for the given action time and event type does not exist
	case 4162: // Operator does not exists: '%-.128s'
	default:
		// in some cases we replace error with fs.ErrNotExist, check for that too
		return os.IsNotExist(err)
	}
	return true
}

// ErrorNumber extracts a database error number from the error chain.
// It delegates to registered ErrorClassifier implementations.
// Returns 0 for nil errors, 0xffff for unrecognized errors.
func ErrorNumber(err error) uint16 {
	if err == nil {
		return 0
	}

	// Try registered ErrorClassifiers
	for _, d := range dialects {
		if ec, ok := d.(ErrorClassifier); ok {
			if n := ec.ErrorNumber(err); n != 0 && n != 0xffff {
				return n
			}
		}
	}

	// Unwrap and try again
	var wrapped interface{ Unwrap() error }
	if errors.As(err, &wrapped) {
		inner := wrapped.Unwrap()
		if inner != nil && inner != err {
			return ErrorNumber(inner)
		}
	}

	return 0xffff
}

// DuplicateChecker is implemented by dialects that can detect duplicate key errors.
type DuplicateChecker interface {
	IsDuplicate(err error) bool
}

// IsDuplicate returns true if the error indicates a unique constraint violation
// (duplicate key). Works across MySQL (error 1062), PostgreSQL (SQLSTATE 23505),
// and SQLite (UNIQUE constraint failed) via registered DuplicateChecker dialects.
func IsDuplicate(err error) bool {
	if err == nil {
		return false
	}

	// Check registered DuplicateCheckers
	for _, d := range dialects {
		if dc, ok := d.(DuplicateChecker); ok {
			if dc.IsDuplicate(err) {
				return true
			}
		}
	}

	// Fallback: MySQL error 1062
	if ErrorNumber(err) == 1062 {
		return true
	}

	return false
}

// Sentinel errors returned by psql operations.
var (
	// ErrNotReady is returned when an operation is attempted on a nil table or
	// backend, or on a feature the table does not have (e.g. [Restore] on a
	// table without soft delete).
	ErrNotReady = errors.New("database is not ready (no connection is available)")
	// ErrNotNillable is returned when a nil value is given for a column that
	// cannot be NULL.
	ErrNotNillable = errors.New("field is nil but cannot be nil")
	// ErrTxAlreadyProcessed is returned by [TxProxy.Commit] and
	// [TxProxy.Rollback] once the transaction was already committed or rolled back.
	ErrTxAlreadyProcessed = errors.New("transaction has already been committed or rollbacked")
	// ErrDeleteBadAssert is returned by [DeleteOne] when the number of deleted
	// rows is not exactly one.
	ErrDeleteBadAssert = errors.New("delete operation failed assertion")
	// ErrBreakLoop can be returned from an Each callback to stop the iteration
	// without reporting an error.
	ErrBreakLoop = errors.New("exiting loop (not an actual error, used to break out of loop callbacks)")
	// ErrUnknownField is returned when a Go field or column name given to
	// [FetchMapped] or [FetchGrouped] does not exist on the table.
	ErrUnknownField = errors.New("unknown field or column")
)
