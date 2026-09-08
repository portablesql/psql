package psql

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// EnumConstraint represents a CHECK constraint for enum columns
type EnumConstraint struct {
	Name    string              // Constraint name (chk_enum_XXXXXXXX)
	Values  []string            // Allowed enum values
	Columns map[string][]string // Map of table -> columns using this constraint
}

// GetEnumConstraintName generates a deduplicated constraint name based on the hash of the values
// It is exported for testing purposes but should generally not be used directly
func GetEnumConstraintName(values string) string {
	// Convert comma-separated values to pipe-separated for hashing (matching PGSQL.md spec)
	valuesList := strings.Split(values, ",")
	valuesStr := strings.Join(valuesList, "|")

	// Generate hash of values for deduplication
	hasher := sha256.New()
	hasher.Write([]byte(valuesStr))
	hash := hex.EncodeToString(hasher.Sum(nil))
	// Use first 8 characters of hash for constraint name
	constraintName := "chk_enum_" + hash[:8]

	slog.Debug(fmt.Sprintf("[psql] Generating enum constraint name for values: %s -> %s -> %s", values, valuesStr, constraintName),
		"event", "psql:enum:constraint_name",
		"values", values,
		"pipe_values", valuesStr,
		"constraint_name", constraintName)

	return constraintName
}

// GetEnumTypeName is kept for backward compatibility but returns the constraint name
func GetEnumTypeName(values string) string {
	return GetEnumConstraintName(values)
}

// CollectEnumConstraints collects all enum columns in a table and groups them by their values.
// This is exported so that database submodules can use it for schema checking.
func CollectEnumConstraints(tv TableView, be *Backend) map[string]*EnumConstraint {
	constraints := make(map[string]*EnumConstraint)

	for _, f := range tv.AllFields() {
		attrs := f.GetAttrs(be)
		if attrs == nil {
			continue
		}

		mytyp, ok := attrs["type"]
		if !ok || strings.ToLower(mytyp) != "enum" {
			continue
		}

		myvals, ok := attrs["values"]
		if !ok {
			continue
		}

		// Get constraint name for these values
		constraintName := GetEnumConstraintName(myvals)

		// Get or create constraint
		constraint, exists := constraints[constraintName]
		if !exists {
			valuesList := strings.Split(myvals, ",")
			constraint = &EnumConstraint{
				Name:    constraintName,
				Values:  valuesList,
				Columns: make(map[string][]string),
			}
			constraints[constraintName] = constraint
		}

		// Add this column to the constraint
		tableName := tv.FormattedName(be)
		constraint.Columns[tableName] = append(constraint.Columns[tableName], f.Column)
	}

	return constraints
}

// GenerateEnumCheckSQL generates the CHECK constraint SQL for enum columns
// According to PGSQL.md, it should create a single constraint that validates all columns with the same enum values
// This is exported for testing purposes
func GenerateEnumCheckSQL(constraint *EnumConstraint, tableName string) string {
	columns := constraint.Columns[tableName]
	if len(columns) == 0 {
		return ""
	}

	// Build the CHECK expression for each column
	var checks []string
	for _, col := range columns {
		// Each column check: (column IS NULL OR column IN ('value1','value2',...))
		quotedValues := make([]string, len(constraint.Values))
		for i, val := range constraint.Values {
			quotedValues[i] = Escape(val)
		}

		check := fmt.Sprintf("(%s IS NULL OR %s IN (%s))",
			QuoteName(col),
			QuoteName(col),
			strings.Join(quotedValues, ","))
		checks = append(checks, check)
	}

	// Combine all checks with AND
	return fmt.Sprintf("CONSTRAINT %s CHECK (%s)",
		QuoteName(constraint.Name),
		strings.Join(checks, " AND "))
}

// ValidateEnum checks that value is one of the values allowed by an enum
// field (a field declared with type=enum,values='a,b,c'). It returns nil for
// fields that are not enums. The error wraps [ErrInvalidEnumValue] so callers
// can test for it with errors.Is. It is not called automatically yet; insert
// and update paths may use it to reject invalid values before reaching the
// database, where only PostgreSQL CHECK constraints would catch them.
func ValidateEnum(field *StructField, value string) error {
	if field == nil {
		return nil
	}
	typ, ok := field.Attrs["type"]
	if !ok || !strings.EqualFold(typ, "enum") {
		return nil
	}
	values, ok := field.Attrs["values"]
	if !ok {
		return nil
	}
	for _, v := range strings.Split(values, ",") {
		if v == value {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not one of %s for column %s", ErrInvalidEnumValue, value, values, field.Column)
}

// ErrInvalidEnumValue is returned (wrapped) by [ValidateEnum] when a value is
// not part of the allowed enum values.
var ErrInvalidEnumValue = errors.New("invalid enum value")
