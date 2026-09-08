package psql

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"slices"
	"strings"
)

// Set represents a SQL SET column type, stored as a comma-separated string in the
// database. It provides Set/Unset/Has methods for manipulating individual values
// and implements sql.Scanner and driver.Valuer for automatic serialization.
//
// In WHERE conditions a Set is compared as a single value ("col"='a,b');
// use [FindInSet] to test for a single element.
type Set []string

// Set adds k to the set if it is not already present.
func (s *Set) Set(k string) {
	if !s.Has(k) {
		*s = append(*s, k)
	}
}

// Unset removes k from the set if present.
func (s *Set) Unset(k string) {
	for n, v := range *s {
		if v == k {
			*s = slices.Delete(*s, n, n+1)
			return
		}
	}
}

// Has reports whether k is present in the set.
func (s Set) Has(k string) bool {
	for _, v := range s {
		if v == k {
			return true
		}
	}
	return false
}

// Scan implements [sql.Scanner], splitting a comma-separated string. An
// empty string (or NULL) yields an empty, non-nil set.
func (s *Set) Scan(src any) error {
	var str string
	switch v := src.(type) {
	case nil:
		*s = Set{}
		return nil
	case string:
		str = v
	case []byte:
		str = string(v)
	case sql.RawBytes:
		str = string(v)
	default:
		return fmt.Errorf("unsupported input format %T", src)
	}
	if str == "" {
		*s = Set{}
		return nil
	}
	*s = strings.Split(str, ",")
	return nil
}

// Value implements [driver.Valuer], joining the elements with commas.
func (s Set) Value() (driver.Value, error) {
	v := strings.Join(s, ",")

	return v, nil
}
