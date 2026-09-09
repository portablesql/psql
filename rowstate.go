package psql

import (
	"encoding/json"
	"reflect"
	"unsafe"

	"github.com/KarpelesLab/typutil"
)

// rowState remembers the values an object had when it was last loaded from,
// or saved to, the database. Values are stored in the field's declared type
// (a nil pointer for NULL) and keyed by the field's struct index, so they can
// be compared directly against the current field with reflect.DeepEqual.
type rowState struct {
	init bool
	val  map[int]any
}

type stateIntf interface {
	state() *rowState
}

// rowstate returns the state carried by the psql.Name or psql.Key field of v,
// or nil if the struct has neither.
func (t *TableMeta[T]) rowstate(v *T) *rowState {
	if t.state == -1 {
		return nil
	}

	val := reflect.ValueOf(v).Elem().Field(t.state)
	// grab value for pointer
	rf := reflect.NewAt(val.Type(), unsafe.Pointer(val.UnsafeAddr()))

	return rf.Interface().(stateIntf).state()
}

// stateValue returns the copy of v that should be remembered in the row state
// for fld. Fields with format=json are remembered as their JSON encoding
// (their Go values may hold maps that cannot be deep-cloned or compared
// reliably); everything else is deep-cloned in its declared type.
func stateValue(fld *StructField, v any) any {
	if fld.IsJSON() {
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		return string(b)
	}
	return typutil.DeepClone(v)
}

// stateEqual reports whether the current field value matches the value
// remembered by stateValue.
func stateEqual(fld *StructField, current, stored any) bool {
	if fld.IsJSON() {
		b, err := json.Marshal(current)
		if err != nil {
			return false
		}
		s, ok := stored.(string)
		return ok && s == string(b)
	}
	return reflect.DeepEqual(current, stored)
}
