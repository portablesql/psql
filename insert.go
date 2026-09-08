package psql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
)

// Insert is a short way to insert objects into database
//
// psql.Insert(ctx, obj)
//
// Is equivalent to:
//
// psql.Table(obj).Insert(ctx, obj)
//
// All passed objects must be of the same type
func Insert[T any](ctx context.Context, target ...*T) error {
	if len(target) == 0 {
		return nil
	}

	return Table[T]().Insert(ctx, target...)
}

// Insert inserts the given objects, one statement each, using a single
// prepared statement. Fires [BeforeSaveHook], [BeforeInsertHook],
// [AfterInsertHook] and [AfterSaveHook] if implemented.
//
// On engines supporting RETURNING (PostgreSQL) the objects are refreshed with
// the stored row. Elsewhere, when the table's primary key is a single integer
// field that is still zero, it is populated from the driver's LastInsertId
// (auto-increment / rowid). Query failures are returned as an [*Error].
func (t *TableMeta[T]) Insert(ctx context.Context, targets ...*T) error {
	return t.insertRows(ctx, insertPlain, targets)
}

// InsertIgnore inserts records, silently ignoring conflicts (e.g., duplicate keys).
// On PostgreSQL this uses ON CONFLICT DO NOTHING, on MySQL INSERT IGNORE, on SQLite
// INSERT OR IGNORE. Hooks are called the same as [Insert].
func InsertIgnore[T any](ctx context.Context, target ...*T) error {
	if len(target) == 0 {
		return nil
	}

	return Table[T]().InsertIgnore(ctx, target...)
}

// InsertIgnore inserts the given objects, ignoring conflicting rows. See [InsertIgnore].
func (t *TableMeta[T]) InsertIgnore(ctx context.Context, targets ...*T) error {
	return t.insertRows(ctx, insertIgnore, targets)
}

// insertMode selects the statement built by insertRows.
type insertMode int

const (
	insertPlain   insertMode = iota // INSERT
	insertIgnore                    // INSERT IGNORE / ON CONFLICT DO NOTHING
	insertReplace                   // REPLACE / ON CONFLICT DO UPDATE
)

func (m insertMode) String() string {
	switch m {
	case insertIgnore:
		return "insert_ignore"
	case insertReplace:
		return "replace"
	default:
		return "insert"
	}
}

var valuerType = reflect.TypeFor[driver.Valuer]()

// insertRows is the shared implementation of Insert, InsertIgnore and Replace.
func (t *TableMeta[T]) insertRows(ctx context.Context, mode insertMode, targets []*T) error {
	if t == nil {
		return ErrNotReady
	}
	t.check(ctx)

	be := GetBackend(ctx)
	engine := be.Engine()
	bt := t.bind(be)
	tableName := bt.name

	ph := engine.Placeholders(len(bt.fields), 1)
	d := engine.dialect()
	ur, hasUpsert := d.(UpsertRenderer)

	var req string
	switch mode {
	case insertIgnore:
		if hasUpsert {
			req = ur.InsertIgnoreSQL(tableName, bt.fldStr, ph)
		} else {
			// Generic fallback: MySQL-like INSERT IGNORE
			req = "INSERT IGNORE INTO " + QuoteName(tableName) + " (" + bt.fldStr + ") VALUES (" + ph + ")"
		}
	case insertReplace:
		if hasUpsert {
			req = ur.ReplaceSQL(tableName, bt.fldStr, ph, bt.mainKey, bt.fields)
		} else {
			// Generic fallback: MySQL-like REPLACE INTO
			if bt.mainKey == nil {
				return errors.New("cannot use Replace without a primary key")
			}
			req = "REPLACE INTO " + QuoteName(tableName) + " (" + bt.fldStr + ") VALUES (" + ph + ")"
		}
	default:
		req = "INSERT INTO " + QuoteName(tableName) + " (" + bt.fldStr + ") VALUES (" + ph + ")"
	}

	useReturning := false
	if rr, ok := d.(ReturningRenderer); ok {
		useReturning = rr.SupportsReturning()
	}
	if useReturning {
		req += " RETURNING " + bt.fldStr
	}

	event := "psql:" + mode.String()

	stmt, err := doPrepareContext(ctx, req)
	if err != nil {
		logQueryError(ctx, event+":prep_fail", tableName, req, err)
		return &Error{Query: req, Err: err}
	}
	defer stmt.Close()

	var autoKey *StructField
	if !useReturning {
		autoKey = t.autoIncrementField(bt)
	}

	for _, target := range targets {
		if h, ok := any(target).(BeforeSaveHook); ok {
			if err := h.BeforeSave(ctx); err != nil {
				return err
			}
		}
		if mode != insertReplace {
			if h, ok := any(target).(BeforeInsertHook); ok {
				if err := h.BeforeInsert(ctx); err != nil {
					return err
				}
			}
		}

		val := reflect.ValueOf(target).Elem()
		params := make([]any, len(bt.fields))
		for n, f := range bt.fields {
			params[n] = exportField(engine, val.Field(f.Index), f)
		}

		if useReturning {
			rows, err := stmt.QueryContext(ctx, params...)
			if err != nil {
				logQueryError(ctx, event+":run_fail", tableName, req, err)
				return &Error{Query: req, Err: err}
			}
			if err := t.scanReturning(ctx, rows, target); err != nil {
				return err
			}
		} else {
			res, err := stmt.ExecContext(ctx, params...)
			if err != nil {
				logQueryError(ctx, event+":run_fail", tableName, req, err)
				return &Error{Query: req, Err: err}
			}
			if autoKey != nil {
				t.applyLastInsertId(res, autoKey, target)
			}
		}

		if mode != insertReplace {
			if h, ok := any(target).(AfterInsertHook); ok {
				if err := h.AfterInsert(ctx); err != nil {
					return err
				}
			}
		}
		if h, ok := any(target).(AfterSaveHook); ok {
			if err := h.AfterSave(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// exportField converts a struct field value into a query parameter. Nil
// pointers, slices and maps become NULL, unless the (non-pointer) type
// implements [driver.Valuer], such as a zero [Set]: its Value() is used, so a
// NOT NULL column keeps receiving the Valuer's representation.
func exportField(engine Engine, fval reflect.Value, f *StructField) any {
	switch fval.Kind() {
	case reflect.Ptr, reflect.Interface:
		if fval.IsNil() {
			return nil
		}
	case reflect.Slice, reflect.Map:
		if fval.IsNil() {
			if !fval.Type().Implements(valuerType) {
				return nil
			}
			v, err := fval.Interface().(driver.Valuer).Value()
			if err != nil {
				return nil
			}
			return v
		}
	}
	return engine.export(fval.Interface(), f)
}

// autoIncrementField returns the primary key field if it is a single integer
// column that can be populated from LastInsertId, or nil.
func (t *TableMeta[T]) autoIncrementField(bt *boundTable) *StructField {
	if bt.mainKey == nil || bt.mainKey.Typ != KeyPrimary || len(bt.mainKey.Fields) != 1 {
		return nil
	}
	f, ok := bt.fldcol[bt.mainKey.Fields[0]]
	if !ok {
		return nil
	}
	typ := t.typ.Field(f.Index).Type
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return f
	}
	return nil
}

// applyLastInsertId sets the primary key of target from res if the key is
// still zero (or a nil pointer) and the driver reports a generated id.
func (t *TableMeta[T]) applyLastInsertId(res sql.Result, f *StructField, target *T) {
	fv := reflect.ValueOf(target).Elem().Field(f.Index)
	if !fv.IsZero() {
		return
	}
	id, err := res.LastInsertId()
	if err != nil || id == 0 {
		return
	}
	if fv.Kind() == reflect.Ptr {
		fv.Set(reflect.New(fv.Type().Elem()))
		fv = fv.Elem()
	}
	switch fv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		fv.SetInt(id)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		fv.SetUint(uint64(id))
	}
	if st := t.rowstate(target); st != nil && st.init {
		st.val[f.Index] = reflect.ValueOf(target).Elem().Field(f.Index).Interface()
	}
}
