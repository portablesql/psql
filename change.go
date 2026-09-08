package psql

import (
	"reflect"
)

// HasChanged returns true if the object has been modified since it was last loaded
// from or saved to the database. It compares current field values against the stored
// state from the last scan.
//
// Objects that carry no state (no psql.Name or psql.Key field), or that were
// never loaded from nor saved to the database, are always reported as changed.
func HasChanged[T any](obj *T) bool {
	return Table[T]().HasChanged(obj)
}

// HasChanged reports whether obj differs from the values it had when it was
// last scanned from, or written to, the database. See [HasChanged].
func (t *TableMeta[T]) HasChanged(obj *T) bool {
	st := t.rowstate(obj)
	if st == nil || !st.init {
		// no state, or never loaded → always report changed
		return true
	}

	val := reflect.ValueOf(obj).Elem()

	for _, col := range t.fields {
		// grab state value
		stv, ok := st.val[col.Index]
		if !ok {
			// can't check because that column wasn't fetched → bad
			return true
		}
		if !stateEqual(col, val.Field(col.Index).Interface(), stv) {
			return true
		}
	}

	return false
}
