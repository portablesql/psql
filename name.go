package psql

import "reflect"

var nameType = reflect.TypeFor[Name]()

// Name allows specifying the table name when associating a table with a struct.
// The name given in the sql tag is used as-is, without going through the
// backend's [Namer]; further attributes apply to the table (check=0 disables
// the automatic schema check for this table).
//
// For example:
//
//	type X struct {
//		TableName psql.Name `sql:"X"`
//		...
//	}
//
// A Name field also carries the row state used by [HasChanged] and [Update] to
// detect which columns were modified since the object was loaded.
type Name struct {
	st *rowState
}

func (n *Name) state() *rowState {
	if n.st == nil {
		n.st = &rowState{}
	}
	return n.st
}
