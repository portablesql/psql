package psql

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"
)

// PreloadChunkSize is the maximum number of keys placed in a single
// "WHERE col IN (...)" list by [Preload]. Larger key sets are split into
// several queries whose results are merged. It defaults to 1000, which is
// below the placeholder limits of all supported engines; tests may lower it.
var PreloadChunkSize = 1000

type assocKind int

const (
	assocBelongsTo assocKind = iota
	assocHasOne
	assocHasMany
	assocManyToMany
)

func (k assocKind) String() string {
	switch k {
	case assocBelongsTo:
		return "belongs_to"
	case assocHasOne:
		return "has_one"
	case assocHasMany:
		return "has_many"
	case assocManyToMany:
		return "many_to_many"
	}
	return "unknown"
}

type assocMeta struct {
	index       int // field index in parent struct
	kind        assocKind
	foreignKey  string          // FK column or Go field name (belongs_to, has_one, has_many)
	targetType  reflect.Type    // element type (e.g., User, not *User or []*User)
	fieldName   string          // Go struct field name
	joinTable   string          // many_to_many: join table name
	joinFK      string          // many_to_many: join table column referencing parent PK
	joinOtherFK string          // many_to_many: join table column referencing target PK
	order       []SortValueable // optional ORDER BY for has_many / many_to_many results
	orderCols   []assocOrderCol // parsed form of order, for Go-side sorting
}

// assocOrderCol is one column of an order= attribute.
type assocOrderCol struct {
	col  string
	desc bool
}

// assocRow is a fetched association target together with the canonical key
// of the column it was fetched by. Rows are returned in database order.
type assocRow struct {
	key any
	val reflect.Value // *T
}

// assocFetcher is an internal interface implemented by TableMeta[T] for association preloading.
type assocFetcher interface {
	// assocFetchByColumn fetches all rows whose column is in keys, chunking the
	// IN list according to PreloadChunkSize. Rows are keyed by the canonical
	// value of column and returned in query order.
	assocFetchByColumn(ctx context.Context, column string, keys []any, opt *FetchOptions) ([]assocRow, error)
	// assocPrimaryKeyField returns the single-column primary key field, or nil.
	assocPrimaryKeyField() *StructField
	// assocResolveField resolves a Go field name or a column name to a field.
	assocResolveField(name string) *StructField
	// assocType returns the Go struct type of the table.
	assocType() reflect.Type
}

// Preload loads associations for the given targets.
// Association fields must be declared with psql struct tags (e.g., `psql:"belongs_to:UserID"`).
// Target types for associations must have been registered via Table[T]().
//
// Association fields may be pointers (`Author *Author`, `Books []*Book`) or
// values (`Author Author`, `Books []Book`). Foreign keys may be pointers; a nil
// foreign key simply leaves the association unset.
//
// Records that reference the same row share a single *T: after preloading a
// belongs_to or many_to_many association, all parents pointing at the same
// target hold the same pointer, so mutating it through one parent is visible
// through the others. has_many and many_to_many results are returned in
// database order unless an order attribute is given in the tag
// (`psql:"has_many:AuthorID;order='Title DESC'"`).
//
// Soft-deleted targets are excluded; use [PreloadOpts] with [IncludeDeleted]
// to include them.
func Preload[T any](ctx context.Context, targets []*T, fields ...string) error {
	return PreloadOpts(ctx, targets, nil, fields...)
}

// PreloadOpts is like [Preload] but honours a [FetchOptions]. Currently only
// WithDeleted (see [IncludeDeleted]) affects preloading: soft-deleted targets
// are then included, matching a parent fetch that used IncludeDeleted. When
// fields is empty, opt.Preload is used as the list of associations to load.
func PreloadOpts[T any](ctx context.Context, targets []*T, opt *FetchOptions, fields ...string) error {
	if len(targets) == 0 {
		return nil
	}
	if opt == nil {
		opt = &FetchOptions{}
	}
	if len(fields) == 0 {
		fields = opt.Preload
	}
	t := Table[T]()
	if t == nil {
		return ErrNotReady
	}
	for _, fieldName := range fields {
		assoc, ok := t.assocs[fieldName]
		if !ok {
			return fmt.Errorf("unknown association %q on type %s", fieldName, t.typ.Name())
		}
		vals := make([]reflect.Value, 0, len(targets))
		for _, target := range targets {
			if target == nil {
				continue
			}
			vals = append(vals, reflect.ValueOf(target).Elem())
		}
		if err := assoc.preload(ctx, t.fldcol, t.mainKey, vals, opt); err != nil {
			return err
		}
	}
	return nil
}

// WithPreload returns a FetchOptions that automatically preloads the given associations after fetching.
func WithPreload(fields ...string) *FetchOptions {
	return &FetchOptions{Preload: fields}
}

// parseAssocTag parses a `psql:"kind:spec[;attr=value,...]"` tag. spec is the
// foreign key (belongs_to, has_one, has_many) or "JoinTable,FK,OtherFK"
// (many_to_many). Supported attributes: order='Col [ASC|DESC][,Col2 ...]'.
func parseAssocTag(tag string, finfo reflect.StructField, index int) *assocMeta {
	parts := strings.SplitN(tag, ":", 2)
	if len(parts) != 2 || parts[1] == "" {
		slog.Warn("[psql] invalid psql tag format, expected kind:ForeignKey", "event", "psql:assoc:bad_tag", "field", finfo.Name, "tag", tag)
		return nil
	}

	spec := parts[1]
	var attrs map[string]string
	if i := strings.IndexByte(spec, ';'); i >= 0 {
		attrs = parseAttrs(spec[i+1:])
		spec = spec[:i]
	}
	if spec == "" {
		slog.Warn("[psql] invalid psql tag format, expected kind:ForeignKey", "event", "psql:assoc:bad_tag", "field", finfo.Name, "tag", tag)
		return nil
	}

	targetType := finfo.Type
	if targetType.Kind() == reflect.Slice {
		targetType = targetType.Elem()
	}
	if targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	if targetType.Kind() != reflect.Struct {
		slog.Warn("[psql] association field must be a struct, a pointer to struct or a slice of those", "event", "psql:assoc:bad_type", "field", finfo.Name, "type", finfo.Type.String())
		return nil
	}

	meta := &assocMeta{
		index:      index,
		targetType: targetType,
		fieldName:  finfo.Name,
	}
	if o, ok := attrs["order"]; ok && o != "" {
		meta.order, meta.orderCols = parseAssocOrder(o)
	}

	switch strings.ToLower(parts[0]) {
	case "belongs_to":
		meta.kind = assocBelongsTo
		meta.foreignKey = spec
	case "has_one":
		meta.kind = assocHasOne
		meta.foreignKey = spec
	case "has_many":
		if finfo.Type.Kind() != reflect.Slice {
			slog.Warn("[psql] has_many association must be a slice type", "event", "psql:assoc:bad_slice", "field", finfo.Name)
			return nil
		}
		meta.kind = assocHasMany
		meta.foreignKey = spec
	case "many_to_many":
		if finfo.Type.Kind() != reflect.Slice {
			slog.Warn("[psql] many_to_many association must be a slice type", "event", "psql:assoc:bad_slice", "field", finfo.Name)
			return nil
		}
		m2mParts := strings.SplitN(spec, ",", 3)
		if len(m2mParts) != 3 || m2mParts[0] == "" || m2mParts[1] == "" || m2mParts[2] == "" {
			slog.Warn("[psql] many_to_many requires format: JoinTable,FK,OtherFK", "event", "psql:assoc:bad_tag", "field", finfo.Name, "tag", tag)
			return nil
		}
		meta.kind = assocManyToMany
		meta.joinTable = strings.TrimSpace(m2mParts[0])
		meta.joinFK = strings.TrimSpace(m2mParts[1])
		meta.joinOtherFK = strings.TrimSpace(m2mParts[2])
	default:
		slog.Warn(fmt.Sprintf("[psql] unknown association type %q", parts[0]), "event", "psql:assoc:bad_kind", "field", finfo.Name)
		return nil
	}
	if meta.kind != assocHasMany && meta.kind != assocManyToMany && meta.order != nil {
		slog.Warn("[psql] order attribute is only supported on has_many and many_to_many associations", "event", "psql:assoc:bad_order", "field", finfo.Name)
		meta.order, meta.orderCols = nil, nil
	}
	return meta
}

// parseAssocOrder parses "Col DESC,Other" into sort values and column specs.
func parseAssocOrder(s string) ([]SortValueable, []assocOrderCol) {
	var res []SortValueable
	var cols []assocOrderCol
	for _, part := range strings.Split(s, ",") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		oc := assocOrderCol{col: fields[0]}
		if len(fields) > 1 {
			fields[len(fields)-1] = strings.ToUpper(fields[len(fields)-1])
			oc.desc = fields[len(fields)-1] == "DESC"
		}
		res = append(res, S(fields...))
		cols = append(cols, oc)
	}
	return res, cols
}

func (a *assocMeta) preload(ctx context.Context, parentFldcol map[string]*StructField, parentKey *StructKey, targets []reflect.Value, opt *FetchOptions) error {
	tableMapL.RLock()
	targetTable, ok := tableMap[a.targetType]
	tableMapL.RUnlock()
	if !ok {
		return fmt.Errorf("table for type %s not registered, ensure psql.Table[%s]() is called first", a.targetType.Name(), a.targetType.Name())
	}

	loader, ok := targetTable.(assocFetcher)
	if !ok {
		return fmt.Errorf("table for type %s does not support preloading", a.targetType.Name())
	}

	switch a.kind {
	case assocBelongsTo:
		return a.preloadBelongsTo(ctx, parentFldcol, targets, loader, opt)
	case assocHasOne, assocHasMany:
		return a.preloadHas(ctx, parentKey, parentFldcol, targets, loader, opt)
	case assocManyToMany:
		return a.preloadManyToMany(ctx, parentKey, parentFldcol, targets, loader, opt)
	}
	return nil
}

// fetchOpt builds the FetchOptions used for target queries.
func (a *assocMeta) fetchOpt(opt *FetchOptions) *FetchOptions {
	res := &FetchOptions{}
	if opt != nil {
		res.WithDeleted = opt.WithDeleted
	}
	if len(a.order) > 0 {
		res.Sort = a.order
	}
	return res
}

// setOne assigns a fetched *T to dst, which may be *T or T.
func setOne(dst reflect.Value, src reflect.Value) {
	if dst.Kind() == reflect.Ptr {
		dst.Set(src)
		return
	}
	dst.Set(src.Elem())
}

// setSlice assigns fetched *T values to dst, which may be []*T or []T.
func setSlice(dst reflect.Value, srcs []reflect.Value) {
	slice := reflect.MakeSlice(dst.Type(), len(srcs), len(srcs))
	ptrElem := dst.Type().Elem().Kind() == reflect.Ptr
	for i, s := range srcs {
		if ptrElem {
			slice.Index(i).Set(s)
		} else {
			slice.Index(i).Set(s.Elem())
		}
	}
	dst.Set(slice)
}

// collectKeys gathers the unique, non-nil values of the given field across
// targets. It returns the raw values suitable for an IN list (one per
// canonical key) and the canonical key of each target (nil when unset).
func collectKeys(targets []reflect.Value, fieldIndex int) ([]any, []any) {
	seen := make(map[any]struct{})
	var keys []any
	perTarget := make([]any, len(targets))
	for i, target := range targets {
		v := target.Field(fieldIndex)
		k, ok := canonicalKey(v)
		if !ok {
			continue
		}
		perTarget[i] = k
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		keys = append(keys, canonicalRaw(v))
	}
	return keys, perTarget
}

// parentPKField resolves the parent's single-column primary key field.
func (a *assocMeta) parentPKField(parentKey *StructKey, parentFldcol map[string]*StructField) (*StructField, error) {
	if parentKey == nil || len(parentKey.Fields) != 1 || parentKey.Fields[0] == "" {
		return nil, fmt.Errorf("association %s: parent must have a single-column primary key for %s (declare it with sql:\",key=PRIMARY\" on the column, or add fields=Column to the psql.Key attributes)", a.fieldName, a.kind)
	}
	col := parentKey.Fields[0]
	f := findFieldByNameOrCol(parentFldcol, col)
	if f == nil {
		return nil, fmt.Errorf("association %s: parent primary key column %q does not match any struct field (check the fields= attribute of the psql.Key)", a.fieldName, col)
	}
	return f, nil
}

func (a *assocMeta) preloadBelongsTo(ctx context.Context, parentFldcol map[string]*StructField, targets []reflect.Value, loader assocFetcher, opt *FetchOptions) error {
	fkField := findFieldByNameOrCol(parentFldcol, a.foreignKey)
	if fkField == nil {
		return fmt.Errorf("association %s: foreign key %q not found (expected a Go field name or column name)", a.fieldName, a.foreignKey)
	}
	pkField := loader.assocPrimaryKeyField()
	if pkField == nil {
		return fmt.Errorf("association %s: target type %s has no single-column primary key", a.fieldName, a.targetType.Name())
	}

	keys, perTarget := collectKeys(targets, fkField.Index)
	if len(keys) == 0 {
		return nil
	}

	rows, err := loader.assocFetchByColumn(ctx, pkField.Column, keys, a.fetchOpt(opt))
	if err != nil {
		return err
	}
	byKey := make(map[any]reflect.Value, len(rows))
	for _, r := range rows {
		if _, dup := byKey[r.key]; !dup {
			byKey[r.key] = r.val
		}
	}

	for i, target := range targets {
		if perTarget[i] == nil {
			continue
		}
		if v, ok := byKey[perTarget[i]]; ok {
			setOne(target.Field(a.index), v)
		}
	}
	return nil
}

// preloadHas handles has_one and has_many: children are fetched by their
// foreign key column matching the parent primary key.
func (a *assocMeta) preloadHas(ctx context.Context, parentKey *StructKey, parentFldcol map[string]*StructField, targets []reflect.Value, loader assocFetcher, opt *FetchOptions) error {
	pkField, err := a.parentPKField(parentKey, parentFldcol)
	if err != nil {
		return err
	}
	fkField := loader.assocResolveField(a.foreignKey)
	if fkField == nil {
		return fmt.Errorf("association %s: foreign key %q not found on %s (expected a Go field name or column name)", a.fieldName, a.foreignKey, a.targetType.Name())
	}

	keys, perTarget := collectKeys(targets, pkField.Index)
	if len(keys) == 0 {
		return nil
	}

	rows, err := loader.assocFetchByColumn(ctx, fkField.Column, keys, a.fetchOpt(opt))
	if err != nil {
		return err
	}
	byKey := make(map[any][]reflect.Value)
	for _, r := range rows {
		byKey[r.key] = append(byKey[r.key], r.val)
	}

	for i, target := range targets {
		if perTarget[i] == nil {
			continue
		}
		results, ok := byKey[perTarget[i]]
		if !ok || len(results) == 0 {
			continue
		}
		if a.kind == assocHasOne {
			setOne(target.Field(a.index), results[0])
		} else {
			setSlice(target.Field(a.index), results)
		}
	}
	return nil
}

func (a *assocMeta) preloadManyToMany(ctx context.Context, parentKey *StructKey, parentFldcol map[string]*StructField, targets []reflect.Value, loader assocFetcher, opt *FetchOptions) error {
	pkField, err := a.parentPKField(parentKey, parentFldcol)
	if err != nil {
		return err
	}
	targetPK := loader.assocPrimaryKeyField()
	if targetPK == nil {
		return fmt.Errorf("association %s: target type %s has no single-column primary key", a.fieldName, a.targetType.Name())
	}
	if len(targets) == 0 {
		return nil
	}
	parentType := targets[0].Type()
	targetType := loader.assocType()

	// Collect parent PKs
	keys, perTarget := collectKeys(targets, pkField.Index)
	if len(keys) == 0 {
		return nil
	}

	// Query join table in chunks: SELECT joinFK, joinOtherFK FROM joinTable WHERE joinFK IN (...)
	type joinPair struct {
		parentKey any
		targetKey any
	}
	var pairs []joinPair
	targetSeen := make(map[any]struct{})
	var targetKeys []any
	for _, chunk := range chunkKeys(keys, PreloadChunkSize) {
		rows, err := B().
			Select(a.joinFK, a.joinOtherFK).
			From(a.joinTable).
			Where(map[string]any{a.joinFK: chunk}).
			RunQuery(ctx)
		if err != nil {
			return fmt.Errorf("many_to_many join query on %s: %w", a.joinTable, err)
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var pFK, tFK sql.RawBytes
				if err := rows.Scan(&pFK, &tFK); err != nil {
					return fmt.Errorf("many_to_many join scan: %w", err)
				}
				if pFK == nil || tFK == nil {
					continue
				}
				pv, err := scanKeyValue(pkField, parentType, pFK)
				if err != nil {
					return fmt.Errorf("many_to_many join column %s: %w", a.joinFK, err)
				}
				tv, err := scanKeyValue(targetPK, targetType, tFK)
				if err != nil {
					return fmt.Errorf("many_to_many join column %s: %w", a.joinOtherFK, err)
				}
				pk, ok1 := canonicalKey(pv)
				tk, ok2 := canonicalKey(tv)
				if !ok1 || !ok2 {
					continue
				}
				pairs = append(pairs, joinPair{parentKey: pk, targetKey: tk})
				if _, dup := targetSeen[tk]; !dup {
					targetSeen[tk] = struct{}{}
					targetKeys = append(targetKeys, canonicalRaw(tv))
				}
			}
			return rows.Err()
		}()
		if err != nil {
			return err
		}
	}
	if len(pairs) == 0 {
		return nil
	}

	// Fetch target records by PK. Within a single chunk the database order
	// is authoritative; when the targets span several chunks, ordered
	// results are re-sorted per parent in Go using the order columns.
	rows, err := loader.assocFetchByColumn(ctx, targetPK.Column, targetKeys, a.fetchOpt(opt))
	if err != nil {
		return err
	}
	var goSort func(vals []reflect.Value)
	if len(a.orderCols) > 0 && len(chunkKeys(targetKeys, PreloadChunkSize)) > 1 {
		goSort, err = a.goSorter(loader)
		if err != nil {
			return err
		}
	}
	type indexed struct {
		val reflect.Value
		pos int
	}
	byKey := make(map[any]indexed, len(rows))
	for i, r := range rows {
		if _, dup := byKey[r.key]; !dup {
			byKey[r.key] = indexed{val: r.val, pos: i}
		}
	}

	// Group targets by parent PK, in join-row order
	grouped := make(map[any][]indexed)
	for _, pair := range pairs {
		if r, ok := byKey[pair.targetKey]; ok {
			grouped[pair.parentKey] = append(grouped[pair.parentKey], r)
		}
	}

	// Assign to parent targets
	for i, target := range targets {
		if perTarget[i] == nil {
			continue
		}
		results, ok := grouped[perTarget[i]]
		if !ok || len(results) == 0 {
			continue
		}
		if len(a.order) > 0 {
			sort.SliceStable(results, func(x, y int) bool { return results[x].pos < results[y].pos })
		}
		vals := make([]reflect.Value, len(results))
		for j, r := range results {
			vals[j] = r.val
		}
		if goSort != nil {
			goSort(vals)
		}
		setSlice(target.Field(a.index), vals)
	}
	return nil
}

// goSorter returns a function sorting fetched *T values by the association's
// order columns, used when results come from several chunked queries.
func (a *assocMeta) goSorter(loader assocFetcher) (func(vals []reflect.Value), error) {
	type key struct {
		idx  int
		desc bool
	}
	keys := make([]key, len(a.orderCols))
	for i, oc := range a.orderCols {
		f := loader.assocResolveField(oc.col)
		if f == nil {
			return nil, fmt.Errorf("association %s: order column %q not found on %s", a.fieldName, oc.col, a.targetType.Name())
		}
		keys[i] = key{idx: f.Index, desc: oc.desc}
	}
	return func(vals []reflect.Value) {
		sort.SliceStable(vals, func(x, y int) bool {
			for _, k := range keys {
				c := assocCompare(vals[x].Elem().Field(k.idx), vals[y].Elem().Field(k.idx))
				if c == 0 {
					continue
				}
				if k.desc {
					return c > 0
				}
				return c < 0
			}
			return false
		})
	}, nil
}

// assocCompare orders two column values the way a database would for common
// types: NULL (nil pointer) first, then numeric, string, bytes, bool or time
// comparison. It returns -1, 0 or 1.
func assocCompare(x, y reflect.Value) int {
	for x.Kind() == reflect.Ptr || x.Kind() == reflect.Interface {
		if x.IsNil() {
			if (y.Kind() == reflect.Ptr || y.Kind() == reflect.Interface) && y.IsNil() {
				return 0
			}
			return -1
		}
		x = x.Elem()
	}
	for y.Kind() == reflect.Ptr || y.Kind() == reflect.Interface {
		if y.IsNil() {
			return 1
		}
		y = y.Elem()
	}
	switch x.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return cmpOrdered(x.Int(), y.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return cmpOrdered(x.Uint(), y.Uint())
	case reflect.Float32, reflect.Float64:
		return cmpOrdered(x.Float(), y.Float())
	case reflect.String:
		return strings.Compare(x.String(), y.String())
	case reflect.Bool:
		if x.Bool() == y.Bool() {
			return 0
		}
		if !x.Bool() {
			return -1
		}
		return 1
	case reflect.Slice:
		if x.Type().Elem().Kind() == reflect.Uint8 {
			return bytes.Compare(x.Bytes(), y.Bytes())
		}
	case reflect.Struct:
		if x.Type() == assocTimeType {
			return x.Interface().(time.Time).Compare(y.Interface().(time.Time))
		}
	}
	return strings.Compare(fmt.Sprint(x.Interface()), fmt.Sprint(y.Interface()))
}

func cmpOrdered[T int64 | uint64 | float64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// scanKeyValue converts a raw database value into a fresh value of the field's
// (pointer-stripped) Go type using the field's setter.
func scanKeyValue(fld *StructField, typ reflect.Type, raw sql.RawBytes) (reflect.Value, error) {
	ft := typ.Field(fld.Index).Type
	for ft.Kind() == reflect.Ptr {
		ft = ft.Elem()
	}
	v := reflect.New(ft).Elem()
	if fld.setter == nil {
		return v, fmt.Errorf("no setter for field %s", fld.Name)
	}
	if err := fld.setter(v, raw); err != nil {
		return v, err
	}
	return v, nil
}

// chunkKeys splits keys into slices of at most n elements.
func chunkKeys(keys []any, n int) [][]any {
	if n <= 0 {
		n = 1000
	}
	if len(keys) <= n {
		return [][]any{keys}
	}
	var res [][]any
	for len(keys) > n {
		res = append(res, keys[:n:n])
		keys = keys[n:]
	}
	if len(keys) > 0 {
		res = append(res, keys)
	}
	return res
}

func findFieldByNameOrCol(fldcol map[string]*StructField, name string) *StructField {
	if f, ok := fldcol[name]; ok {
		return f
	}
	for _, f := range fldcol {
		if f.Name == name {
			return f
		}
	}
	return nil
}

var (
	assocTimeType   = reflect.TypeFor[time.Time]()
	assocValuerType = reflect.TypeFor[driver.Valuer]()
)

// canonicalKey converts a value into a comparable key such that equal database
// values produce equal keys regardless of the Go type used to hold them:
// pointers are dereferenced (nil yields ok=false), all integer kinds map to
// int64 (uint64 values above MaxInt64 stay uint64), integral floats map to
// int64, strings and byte slices/arrays map to string, and driver.Valuer
// implementations are unwrapped. time.Time maps to its UnixNano value.
func canonicalKey(v reflect.Value) (any, bool) {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, false
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return nil, false
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := v.Uint()
		if u <= math.MaxInt64 {
			return int64(u), true
		}
		return u, true
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if f == math.Trunc(f) && math.Abs(f) < 1<<53 {
			return int64(f), true
		}
		return f, true
	case reflect.String:
		return v.String(), true
	case reflect.Bool:
		return v.Bool(), true
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if v.IsNil() {
				return nil, false
			}
			return string(v.Bytes()), true
		}
	case reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			b := make([]byte, v.Len())
			for i := range b {
				b[i] = byte(v.Index(i).Uint())
			}
			return string(b), true
		}
	case reflect.Struct:
		if v.Type() == assocTimeType {
			return v.Interface().(time.Time).UnixNano(), true
		}
	}
	if v.Type().Implements(assocValuerType) {
		if dv, err := v.Interface().(driver.Valuer).Value(); err == nil {
			if dv == nil {
				return nil, false
			}
			return canonicalKey(reflect.ValueOf(dv))
		}
	}
	if v.CanAddr() && v.Addr().Type().Implements(assocValuerType) {
		if dv, err := v.Addr().Interface().(driver.Valuer).Value(); err == nil {
			if dv == nil {
				return nil, false
			}
			return canonicalKey(reflect.ValueOf(dv))
		}
	}
	if v.Type().Comparable() {
		return v.Interface(), true
	}
	return fmt.Sprintf("%v", v.Interface()), true
}

// canonicalRaw returns the dereferenced Go value of v, suitable for use in a
// query argument. Callers must have checked canonicalKey(v) returned ok.
func canonicalRaw(v reflect.Value) any {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	return v.Interface()
}

// assocFetcher implementation on TableMeta

func (t *TableMeta[T]) assocFetchByColumn(ctx context.Context, column string, keys []any, opt *FetchOptions) ([]assocRow, error) {
	fld, ok := t.fldcol[column]
	if !ok {
		return nil, fmt.Errorf("column %q not found in table %s", column, t.table)
	}
	var rows []assocRow
	for _, chunk := range chunkKeys(keys, PreloadChunkSize) {
		results, err := t.Fetch(ctx, map[string]any{column: chunk}, opt)
		if err != nil {
			return nil, err
		}
		for _, r := range results {
			val := reflect.ValueOf(r).Elem()
			key, ok := canonicalKey(val.Field(fld.Index))
			if !ok {
				continue
			}
			rows = append(rows, assocRow{key: key, val: reflect.ValueOf(r)})
		}
	}
	return rows, nil
}

func (t *TableMeta[T]) assocPrimaryKeyField() *StructField {
	if t.mainKey == nil || len(t.mainKey.Fields) != 1 || t.mainKey.Fields[0] == "" {
		return nil
	}
	return findFieldByNameOrCol(t.fldcol, t.mainKey.Fields[0])
}

func (t *TableMeta[T]) assocResolveField(name string) *StructField {
	return findFieldByNameOrCol(t.fldcol, name)
}

func (t *TableMeta[T]) assocType() reflect.Type {
	return t.typ
}
