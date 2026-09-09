package psql

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
)

// boundTable is a [TableMeta] resolved against one backend's [Namer]: the
// table name and the column names it holds are the ones used in SQL for that
// backend. Columns given explicitly in a sql tag are never transformed.
//
// When the namer leaves every column unchanged (as [LegacyNamer] does), the
// bound table shares the slices and maps of its TableMeta.
type boundTable struct {
	namer      Namer
	rawName    string // declared table name
	name       string // formatted table name
	fields     []*StructField
	fldcol     map[string]*StructField
	fldStr     string
	keys       []*StructKey
	mainKey    *StructKey
	softDelete *StructField
	autoInc    *StructField // identity column (resolved), or nil
	attrs      map[string]string
}

var _ TableView = (*boundTable)(nil)

// TableName returns the declared table name (implements TableView).
func (b *boundTable) TableName() string { return b.rawName }

// FormattedName returns the resolved table name (implements TableView).
func (b *boundTable) FormattedName(_ *Backend) string { return b.name }

// AllFields returns the fields with their resolved column names (implements TableView).
func (b *boundTable) AllFields() []*StructField { return b.fields }

// AllKeys returns the keys with their resolved column names (implements TableView).
func (b *boundTable) AllKeys() []*StructKey { return b.keys }

// MainKey returns the primary/unique key (implements TableView).
func (b *boundTable) MainKey() *StructKey { return b.mainKey }

// FieldByColumn returns a field by its resolved column name (implements TableView).
func (b *boundTable) FieldByColumn(col string) *StructField { return b.fldcol[col] }

// FieldStr returns the comma-separated quoted column list (implements TableView).
func (b *boundTable) FieldStr() string { return b.fldStr }

// TableAttrs returns the table-level attributes (implements TableView).
func (b *boundTable) TableAttrs() map[string]string { return b.attrs }

// HasSoftDelete reports whether the table has a soft delete column (implements TableView).
func (b *boundTable) HasSoftDelete() bool { return b.softDelete != nil }

// sameNamer reports whether two namers are the same for caching purposes.
// Stateless namers (zero-size types) compare by type only; other comparable
// values compare with ==; uncomparable namers are never considered equal.
func sameNamer(a, b Namer) bool {
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb {
		return false
	}
	if ta == nil || ta.Size() == 0 || (ta.Kind() == reflect.Ptr && ta.Elem().Size() == 0) {
		return true
	}
	if !ta.Comparable() {
		return false
	}
	return a == b
}

// bind returns the view of t resolved against be's Namer. Views are cached
// per backend and rebuilt if the backend's namer changes.
func (t *TableMeta[T]) bind(be *Backend) *boundTable {
	namer := be.Namer()
	if v, ok := t.views.Load(be); ok {
		bt := v.(*boundTable)
		if sameNamer(bt.namer, namer) {
			return bt
		}
	}
	bt := t.buildView(be, namer)
	t.views.Store(be, bt)
	return bt
}

// buildView resolves table and column names through namer.
func (t *TableMeta[T]) buildView(be *Backend, namer Namer) *boundTable {
	bt := &boundTable{
		namer:   namer,
		rawName: t.table,
		name:    t.FormattedName(be),
		attrs:   t.attrs,
	}

	// resolve column names; colmap maps declared → resolved
	colmap := make(map[string]string, len(t.fields))
	fields := make([]*StructField, len(t.fields))
	changed := false
	for i, f := range t.fields {
		col := f.Column
		if !f.explicitCol {
			col = namer.ColumnName(t.table, f.Column)
		}
		colmap[f.Column] = col
		if col == f.Column {
			fields[i] = f
			continue
		}
		changed = true
		nf := f.clone()
		nf.Column = col
		fields[i] = nf
	}

	if !changed {
		bt.fields = t.fields
		bt.fldcol = t.fldcol
		bt.fldStr = t.fldStr
		bt.keys = t.keys
		bt.mainKey = t.mainKey
		bt.softDelete = t.softDelete
		bt.autoInc = t.autoInc
		return bt
	}

	bt.fields = fields
	bt.fldcol = make(map[string]*StructField, len(fields))
	names := make([]string, len(fields))
	for i, f := range fields {
		bt.fldcol[f.Column] = f
		names[i] = QuoteName(f.Column)
		if t.softDelete != nil && f.Index == t.softDelete.Index {
			bt.softDelete = f
		}
		if t.autoInc != nil && f.Index == t.autoInc.Index {
			bt.autoInc = f
		}
	}
	bt.fldStr = strings.Join(names, ",")

	bt.keys = make([]*StructKey, len(t.keys))
	for i, k := range t.keys {
		nk := *k
		nk.Fields = make([]string, len(k.Fields))
		for j, c := range k.Fields {
			if r, ok := colmap[c]; ok {
				nk.Fields[j] = r
			} else {
				nk.Fields[j] = c
			}
		}
		bt.keys[i] = &nk
		if k == t.mainKey {
			bt.mainKey = &nk
		}
	}
	return bt
}

// scanPlan maps the columns of one result set to the fields of T, so that
// scanning each row does not repeat the column lookup.
type scanPlan[T any] struct {
	t      *TableMeta[T]
	fields []*StructField // by column position; nil when the column is not a field
	values []sql.RawBytes
	dest   []any
}

// newScanPlan resolves the columns of rows against the table bound to the
// backend found in ctx. Columns that match neither the resolved nor the
// declared column name are ignored.
func (t *TableMeta[T]) newScanPlan(ctx context.Context, rows *sql.Rows) (*scanPlan[T], error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	bt := t.bind(GetBackend(ctx))

	n := len(cols)
	p := &scanPlan[T]{
		t:      t,
		fields: make([]*StructField, n),
		values: make([]sql.RawBytes, n),
		dest:   make([]any, n),
	}
	for i, c := range cols {
		p.dest[i] = &p.values[i]
		if f, ok := bt.fldcol[c]; ok {
			p.fields[i] = f
		} else if f, ok := t.fldcol[c]; ok {
			p.fields[i] = f
		}
	}
	return p, nil
}

// scan reads the current row into target and records the row state. When
// hook is true the [AfterScanHook] is called; RETURNING scans pass false.
func (p *scanPlan[T]) scan(ctx context.Context, rows *sql.Rows, target *T, hook bool) error {
	t := p.t
	if err := rows.Scan(p.dest...); err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("psql: scan error on %s: %s", t.table, err), "event", "psql:table:scan_error", "psql.table", t.table)
		return fmt.Errorf("scan error: %w", err)
	}

	val := reflect.ValueOf(target).Elem()
	st := t.rowstate(target)
	if st != nil {
		st.init = true
		st.val = make(map[int]any, len(p.fields))
	}

	for i, fld := range p.fields {
		if fld == nil {
			continue
		}
		f := val.Field(fld.Index)
		if p.values[i] == nil {
			// NULL: nil pointer, or zero value for non-pointer fields
			f.Set(reflect.Zero(f.Type()))
		} else {
			// make sure "dst" is a settable value (not a ptr), allocate if needed
			dst := f
			for dst.Kind() == reflect.Ptr {
				if dst.IsNil() {
					dst.Set(reflect.New(dst.Type().Elem()))
				}
				dst = dst.Elem()
			}
			if err := fld.setter(dst, p.values[i]); err != nil {
				return fmt.Errorf("on field %s: %w", fld.Name, err)
			}
		}
		if st != nil {
			// keep a copy in the field's declared type so HasChanged can
			// compare it directly against the field
			st.val[fld.Index] = stateValue(fld, f.Interface())
		}
	}

	if hook {
		if h, ok := any(target).(AfterScanHook); ok {
			if err := h.AfterScan(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// scanReturning consumes the single row produced by a RETURNING clause into
// target, then closes rows. A statement that produced no row (e.g. INSERT ...
// ON CONFLICT DO NOTHING) leaves target untouched.
func (t *TableMeta[T]) scanReturning(ctx context.Context, rows *sql.Rows, target *T) error {
	defer rows.Close()
	if !rows.Next() {
		return rows.Err()
	}
	plan, err := t.newScanPlan(ctx, rows)
	if err != nil {
		return err
	}
	if err := plan.scan(ctx, rows, target, false); err != nil {
		return err
	}
	return rows.Err()
}

// fieldByNameOrColumn finds a field of t by its Go name, its resolved column
// name or its declared column name.
func (t *TableMeta[T]) fieldByNameOrColumn(bt *boundTable, name string) *StructField {
	if f, ok := bt.fldcol[name]; ok {
		return f
	}
	if f, ok := t.fldcol[name]; ok {
		return f
	}
	for _, f := range bt.fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}
