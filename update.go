package psql

import (
	"context"
	"errors"
	"reflect"
	"sort"
)

// Update saves changes to existing database records. Only fields that have changed
// since the last load are updated (if the object was previously fetched). Fires
// [BeforeSaveHook], [BeforeUpdateHook], [AfterUpdateHook], and [AfterSaveHook] if
// implemented. All passed objects must be of the same type.
func Update[T any](ctx context.Context, target ...*T) error {
	if len(target) == 0 {
		return nil
	}

	return Table[T]().Update(ctx, target...)
}

type updatedField struct {
	f  *StructField
	v  any
	fv reflect.Value
}

// Update writes the given objects back to the database. Objects that were
// loaded with a fetch operation only have their changed columns written
// (compared against the state recorded at scan time); other objects have every
// column written. Objects without any change are skipped. The table needs a
// primary or unique key. Query failures are returned as an [*Error].
func (t *TableMeta[T]) Update(ctx context.Context, target ...*T) error {
	if t == nil {
		return ErrNotReady
	}
	t.check(ctx)
	if t.mainKey == nil {
		return errors.New("cannot update values without a unique key")
	}

	be := GetBackend(ctx)
	engine := be.Engine()
	bt := t.bind(be)

	for _, obj := range target {
		if h, ok := any(obj).(BeforeSaveHook); ok {
			if err := h.BeforeSave(ctx); err != nil {
				return err
			}
		}
		if h, ok := any(obj).(BeforeUpdateHook); ok {
			if err := h.BeforeUpdate(ctx); err != nil {
				return err
			}
		}

		// check for changed values
		var upd []*updatedField

		val := reflect.ValueOf(obj).Elem()

		st := t.rowstate(obj)
		hasState := st != nil && st.init
		for _, f := range bt.fields {
			newv := val.Field(f.Index).Interface()
			if hasState {
				if stv, ok := st.val[f.Index]; ok && stateEqual(f, newv, stv) {
					continue
				}
			}
			upd = append(upd, &updatedField{f: f, v: newv, fv: val.Field(f.Index)})
		}
		if len(upd) == 0 {
			// no update needed
			continue
		}
		// deterministic column order so prepared statement caches can hit
		sort.Slice(upd, func(i, j int) bool { return upd[i].f.Column < upd[j].f.Column })

		// perform update
		d := engine.dialect()
		req := "UPDATE " + QuoteName(bt.name) + " SET "
		var flds []any
		for n, u := range upd {
			if n > 0 {
				req += ", "
			}
			flds = append(flds, exportField(engine, u.fv, u.f))
			req += QuoteName(u.f.Column) + " = " + d.Placeholder(len(flds))
		}
		req += " WHERE "
		// render key
		for n, col := range bt.mainKey.Fields {
			if n > 0 {
				req += " AND "
			}
			kf := bt.fldcol[col]
			flds = append(flds, exportField(engine, val.Field(kf.Index), kf))
			req += QuoteName(col) + " = " + d.Placeholder(len(flds))
		}

		if _, err := ExecContext(ctx, req, flds...); err != nil {
			logQueryError(ctx, "psql:update:run_fail", t.table, req, err)
			return &Error{Query: req, Err: err}
		}
		if st != nil {
			// update state since update was successful
			if !st.init {
				st.init = true
				st.val = make(map[int]any, len(bt.fields))
			}
			for _, u := range upd {
				st.val[u.f.Index] = stateValue(u.f, u.v)
			}
		}

		if h, ok := any(obj).(AfterUpdateHook); ok {
			if err := h.AfterUpdate(ctx); err != nil {
				return err
			}
		}
		if h, ok := any(obj).(AfterSaveHook); ok {
			if err := h.AfterSave(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}
