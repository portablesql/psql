package psql

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

var (
	tableMap  = make(map[reflect.Type]TableMetaIntf)
	tableMapL sync.RWMutex
)

// TableView is a non-generic interface for accessing table metadata from
// dialect implementations (which cannot use type parameters).
//
// The value handed to [SchemaChecker.CheckStructure] and
// [Backend.CheckStructure] is already resolved against the backend's
// [Namer]: AllFields, AllKeys, FieldByColumn and FieldStr report the column
// names as they exist in that database.
type TableView interface {
	// TableName returns the table name as declared on the struct: the value of
	// the psql.Name tag, or the Go type name.
	TableName() string
	// FormattedName returns the table name as used in SQL for the given backend.
	FormattedName(be *Backend) string
	// AllFields returns every column field of the table, in declaration order.
	AllFields() []*StructField
	// AllKeys returns every key/index declared on the table.
	AllKeys() []*StructKey
	// MainKey returns the primary key (or first unique key), or nil.
	MainKey() *StructKey
	// FieldByColumn returns the field for a column name, or nil.
	FieldByColumn(col string) *StructField
	// FieldStr returns the comma-separated, quoted column list used in SELECT.
	FieldStr() string
	// TableAttrs returns the attributes set on the psql.Name tag (e.g. check=0).
	TableAttrs() map[string]string
	// HasSoftDelete reports whether the table has a soft delete column.
	HasSoftDelete() bool
}

// TableMeta holds the metadata for a registered table type T, including its fields,
// keys, associations, and SQL column mappings. Obtain one via [Table].
//
// A TableMeta stores names as declared in Go. The table and column names
// actually used against a database depend on that backend's [Namer]; see
// [TableMeta.FormattedName].
type TableMeta[T any] struct {
	typ          reflect.Type
	table        string // declared table name: psql.Name value, or Go type name
	explicitName bool   // true if table name was explicitly set via psql.Name
	fields       []*StructField
	fldcol       map[string]*StructField
	keys         []*StructKey
	mainKey      *StructKey
	fldStr       string // string of all fields
	state        int
	attrs        map[string]string
	assocs       map[string]*assocMeta // association metadata by Go field name
	softDelete   *StructField          // non-nil if soft delete is enabled
	autoInc      *StructField          // non-nil if a field carries the autoinc attribute
	views        sync.Map              // *Backend → *boundTable (names resolved by the backend's Namer)
}

// TableMetaIntf is the non-generic interface satisfied by every [TableMeta].
type TableMetaIntf interface {
	// Name returns the declared table name (see [TableMeta.Name]).
	Name() string
}

// Verify TableMeta implements TableView.
var _ TableView = (*TableMeta[struct{}])(nil)

// Table returns the table metadata for the struct type T, registering it on
// first use. Registration is safe for concurrent use: every caller receives
// the same *TableMeta for a given T.
//
// Table panics if T is not a struct, has no column fields, or contains a field
// whose type cannot be scanned from a database row.
func Table[T any]() *TableMeta[T] {
	typ := reflect.TypeFor[T]()

	if typ.Kind() != reflect.Struct {
		panic(fmt.Sprintf("target must be a *struct, got a %s", typ))
	}

	tableMapL.RLock()
	found, ok := tableMap[typ]
	tableMapL.RUnlock()
	if ok {
		return found.(*TableMeta[T])
	}

	info := buildTableMeta[T](typ)

	tableMapL.Lock()
	defer tableMapL.Unlock()
	// another goroutine may have registered the same type while we were
	// building ours: the first registration wins so that every caller shares
	// one instance (and one row state layout).
	if found, ok := tableMap[typ]; ok {
		return found.(*TableMeta[T])
	}
	tableMap[typ] = info
	return info
}

// buildTableMeta inspects typ and builds its metadata. It does not touch the
// registry.
func buildTableMeta[T any](typ reflect.Type) *TableMeta[T] {
	info := &TableMeta[T]{
		typ:    typ,
		table:  typ.Name(),
		fldcol: make(map[string]*StructField),
		attrs:  make(map[string]string),
		assocs: make(map[string]*assocMeta),
		state:  -1,
	}

	cnt := typ.NumField()
	var names []string
	extraKeys := make(map[string]*StructKey)

	for i := 0; i < cnt; i += 1 {
		if !typ.Field(i).IsExported() {
			continue
		}
		finfo := typ.Field(i)

		// Check for psql association tag
		if psqlTag := finfo.Tag.Get("psql"); psqlTag != "" {
			assoc := parseAssocTag(psqlTag, finfo, i)
			if assoc != nil {
				info.assocs[finfo.Name] = assoc
			}
			continue
		}

		col := finfo.Name
		explicitCol := false
		attrs := make(map[string]string)

		tag := finfo.Tag.Get("sql")
		if tag != "" {
			if tag == "-" {
				// skip
				continue
			}
			// handle properties, etc
			tagCol, tagAttrs := parseTagData(tag)
			if tagCol != "" {
				// could be sql:",type=..." so only set col if not empty
				// Explicit tag name overrides the namer
				col = tagCol
				explicitCol = true
			}
			attrs = tagAttrs
		}

		switch finfo.Type {
		case nameType:
			// this is actually the name of the table
			info.table = col
			info.explicitName = true // Mark that name was explicitly provided
			info.attrs = attrs
			if info.state == -1 {
				info.state = i
			}
			continue
		case keyType:
			if info.state == -1 {
				info.state = i
			}
			key := &StructKey{
				Index: i,
				Name:  finfo.Name,
				Key:   col,
			}
			key.loadAttrs(attrs)
			info.keys = append(info.keys, key)

			if (info.mainKey == nil && key.IsUnique()) || key.Typ == KeyPrimary {
				info.mainKey = key
			}
			continue
		}

		if keyName, ok := attrs["key"]; ok {
			delete(attrs, "key")
			if k, found := extraKeys[keyName]; found {
				k.Fields = append(k.Fields, col)
			} else {
				k = &StructKey{
					Index:  -1,
					Fields: []string{col},
					Attrs:  map[string]string{},
				}
				k.loadKeyName(keyName)
				extraKeys[keyName] = k
				info.keys = append(info.keys, k)

				if (info.mainKey == nil && k.IsUnique()) || k.Typ == KeyPrimary {
					info.mainKey = k
				}
			}
		}

		// "softdelete" only marks the field, it is not a column attribute and
		// must not prevent type inference below.
		_, softDelete := attrs["softdelete"]
		delete(attrs, "softdelete")

		// "autoinc" marks an identity column. Like key/softdelete it must not
		// prevent type inference, but it is put back afterwards (as
		// autoinc=1) so dialects see it in the resolved attributes.
		autoInc := parseAutoInc(attrs)
		delete(attrs, "autoinc")

		if len(attrs) == 0 {
			// import based on type
			attrs["import"] = finfo.Type.String()
		}
		if autoInc {
			if !isIntegerType(finfo.Type) {
				panic(fmt.Sprintf("psql: field %s.%s: autoinc requires an integer field, got %s", typ.Name(), finfo.Name, finfo.Type))
			}
			if info.autoInc != nil {
				panic(fmt.Sprintf("psql: table %s: only one autoinc field is allowed (%s and %s)", typ.Name(), info.autoInc.Name, finfo.Name))
			}
			attrs["autoinc"] = "1"
		}

		var setter func(reflect.Value, sql.RawBytes) error
		if isJSONFormat(attrs) {
			// format=json fields (directly or through an import such as
			// import=JSON) are (un)marshaled with encoding/json
			jt := finfo.Type
			for jt.Kind() == reflect.Ptr {
				jt = jt.Elem()
			}
			setter = makeJSONSetter(jt)
		} else {
			var err error
			setter, err = lookupSetter(finfo.Type)
			if err != nil {
				panic(fmt.Sprintf("psql: cannot use field %s.%s as a column: %s", typ.Name(), finfo.Name, err))
			}
		}

		fld := &StructField{
			Index:       i,
			Name:        finfo.Name,
			Column:      col,
			setter:      setter,
			Attrs:       attrs,
			Rattrs:      make(map[Engine]map[string]string),
			explicitCol: explicitCol,
			json:        isJSONFormat(attrs),
			autoinc:     autoInc,
		}
		names = append(names, QuoteName(col))
		if autoInc {
			info.autoInc = fld
		}

		// TODO handle other kind of nullables, such as sql.NullString etc
		if finfo.Type.Kind() == reflect.Ptr {
			fld.Nullable = true
		}

		info.fields = append(info.fields, fld)
		info.fldcol[fld.Column] = fld

		// Detect soft delete: *time.Time field named "DeletedAt" or with softdelete attr
		if softDelete || (finfo.Name == "DeletedAt" && finfo.Type == ptrTimeType) {
			info.softDelete = fld
		}
	}

	if len(info.fields) == 0 {
		panic("no fields for table")
	}

	// primary key members are NOT NULL on every engine
	for _, k := range info.keys {
		if k.Typ != KeyPrimary {
			continue
		}
		for _, col := range k.Fields {
			if fld, ok := info.fldcol[col]; ok {
				fld.primary = true
			}
		}
	}

	info.fldStr = strings.Join(names, ",")
	return info
}

// parseAutoInc reports whether attrs declare an identity column: a bare
// "autoinc" or autoinc=1/true. autoinc=0/false is accepted and means no.
func parseAutoInc(attrs map[string]string) bool {
	v, ok := attrs["autoinc"]
	if !ok {
		return false
	}
	switch v {
	case "0", "false", "no":
		return false
	default:
		return true
	}
}

// isIntegerType reports whether typ (or the type it points to) is a Go
// integer kind.
func isIntegerType(typ reflect.Type) bool {
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

// AutoIncField returns the field declared with the autoinc attribute, or nil
// when the table has none. See [StructField.IsAutoInc].
func (t *TableMeta[T]) AutoIncField() *StructField {
	if t == nil {
		return nil
	}
	return t.autoInc
}

// Name returns the table name as declared: the value of the psql.Name tag, or
// the Go type name when no explicit name was given. The name used in SQL for
// a given backend is returned by [TableMeta.FormattedName].
func (t *TableMeta[T]) Name() string {
	if t == nil {
		return ""
	}
	return t.table
}

// TableName returns the declared table name (implements TableView). It is the
// same value as [TableMeta.Name].
func (t *TableMeta[T]) TableName() string {
	return t.table
}

// FormattedName returns the table name used in SQL for the given backend.
// Explicit names (psql.Name) are returned as-is; otherwise the Go type name is
// passed once through the backend's [Namer].TableName. With the default
// [LegacyNamer] "UserProfile" becomes "User_Profile"; with [DefaultNamer] it
// stays "UserProfile".
func (t *TableMeta[T]) FormattedName(be *Backend) string {
	if t == nil {
		return ""
	}
	if t.explicitName {
		// Table name was explicitly set via psql.Name, use as-is
		return t.table
	}
	// Apply namer transformation
	return be.Namer().TableName(t.table)
}

// AllFields returns all fields with their declared column names (implements TableView).
func (t *TableMeta[T]) AllFields() []*StructField {
	return t.fields
}

// AllKeys returns all keys (implements TableView).
func (t *TableMeta[T]) AllKeys() []*StructKey {
	return t.keys
}

// MainKey returns the primary/unique key (implements TableView).
func (t *TableMeta[T]) MainKey() *StructKey {
	return t.mainKey
}

// FieldByColumn returns a field by its declared column name (implements TableView).
func (t *TableMeta[T]) FieldByColumn(col string) *StructField {
	return t.fldcol[col]
}

// FieldStr returns the comma-separated quoted field list (implements TableView).
func (t *TableMeta[T]) FieldStr() string {
	return t.fldStr
}

// TableAttrs returns the table-level attributes (implements TableView).
func (t *TableMeta[T]) TableAttrs() map[string]string {
	return t.attrs
}

// HasSoftDelete returns true if the table has a soft delete field (implements TableView).
func (t *TableMeta[T]) HasSoftDelete() bool {
	return t.softDelete != nil
}

// tableType returns the Go type this metadata describes.
func (t *TableMeta[T]) tableType() reflect.Type {
	return t.typ
}

func (t *TableMeta[T]) newobj() *T {
	return reflect.New(t.typ).Interface().(*T)
}

// spawnAll scans every remaining row of rows into new objects and closes rows.
func (t *TableMeta[T]) spawnAll(ctx context.Context, rows *sql.Rows) ([]*T, error) {
	defer rows.Close()

	plan, err := t.newScanPlan(ctx, rows)
	if err != nil {
		return nil, err
	}

	var res []*T
	for rows.Next() {
		obj := t.newobj()
		if err := plan.scan(ctx, rows, obj, true); err != nil {
			return res, err
		}
		res = append(res, obj)
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	return res, nil
}

// spawn scans the current row of rows into a new object.
func (t *TableMeta[T]) spawn(ctx context.Context, rows *sql.Rows) (*T, error) {
	res := t.newobj()
	err := t.ScanTo(ctx, rows, res)
	return res, err
}

// ScanTo scans the current row of rows (rows.Next must already have been
// called) into v, matching result columns to fields by column name, and
// records the row state used by [TableMeta.HasChanged] and [TableMeta.Update].
// The [AfterScanHook] is called if T implements it.
//
// When scanning many rows, prefer [Fetch], [Iter] or [SQLQueryT.Each]: they
// resolve the column mapping once per result set instead of once per row.
func (t *TableMeta[T]) ScanTo(ctx context.Context, rows *sql.Rows, v *T) error {
	plan, err := t.newScanPlan(ctx, rows)
	if err != nil {
		return err
	}
	return plan.scan(ctx, rows, v, true)
}
