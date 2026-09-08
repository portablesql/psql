package psql

import (
	"database/sql/driver"
	"fmt"
	"reflect"
	"strconv"
	"time"
)

// intStr is a helper to convert int to string.
func intStr(v int) string {
	return strconv.Itoa(v)
}

// DefaultExportArg handles shared export logic for types common to all engines.
// Submodule dialects should call this as a fallback from their ExportArg implementations.
//
// Nil values (including typed nil pointers, slices and maps) export as nil,
// [time.Time] and [driver.Valuer] implementations are passed through for the
// driver to handle, other [fmt.Stringer] implementations export their String()
// and pointers are dereferenced.
func DefaultExportArg(v any) any {
	return defaultExportArg(v)
}

// defaultExportArg handles shared export logic for types common to all engines.
func defaultExportArg(v any) any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Map, reflect.Interface, reflect.Chan, reflect.Func:
		if rv.IsNil() {
			return nil
		}
	}
	switch val := v.(type) {
	case time.Time:
		return val
	case *time.Time:
		return *val
	case driver.Valuer:
		return v
	case fmt.Stringer:
		return val.String()
	}
	if rv.Kind() == reflect.Ptr {
		return defaultExportArg(rv.Elem().Interface())
	}
	return v
}
