package psql

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
)

// BulkOption configures [BulkInsert].
type BulkOption func(*bulkOptions)

type bulkOptions struct {
	batchSize int
	ignore    bool
	noHooks   bool
}

// DefaultBulkBatchSize is the number of rows written per statement by
// [BulkInsert] when [BulkBatchSize] is not given. The effective size is also
// capped so that rows × columns stays under the engine's parameter limit.
const DefaultBulkBatchSize = 500

// BulkBatchSize sets the maximum number of rows per INSERT statement. Values
// below 1 keep the default ([DefaultBulkBatchSize]).
func BulkBatchSize(n int) BulkOption {
	return func(o *bulkOptions) {
		if n > 0 {
			o.batchSize = n
		}
	}
}

// BulkIgnore makes [BulkInsert] skip conflicting rows: INSERT IGNORE on
// MySQL, INSERT OR IGNORE on SQLite, ON CONFLICT DO NOTHING on PostgreSQL.
// Because skipped rows cannot be told apart from inserted ones, generated
// keys are not populated in this mode and the native bulk loader is not used.
func BulkIgnore() BulkOption {
	return func(o *bulkOptions) { o.ignore = true }
}

// BulkNoHooks makes [BulkInsert] skip the BeforeSave/BeforeInsert and
// AfterInsert/AfterSave hooks.
func BulkNoHooks() BulkOption {
	return func(o *bulkOptions) { o.noHooks = true }
}

// maxBulkParams returns the maximum number of bind parameters a single
// statement may carry on the engine.
func maxBulkParams(e Engine) int {
	switch e {
	case EngineSQLite:
		return 32766 // SQLITE_MAX_VARIABLE_NUMBER since 3.32
	case EnginePostgreSQL, EngineMySQL:
		return 65535 // 16-bit parameter count in both wire protocols
	default:
		return 32766
	}
}

// BulkInsert inserts many rows of the same table efficiently. It is a
// shortcut for [TableMeta.BulkInsert] on Table[T]().
func BulkInsert[T any](ctx context.Context, rows []*T, opts ...BulkOption) error {
	if len(rows) == 0 {
		return nil
	}
	return Table[T]().BulkInsert(ctx, rows, opts...)
}

// BulkInsert inserts rows with as few statements as possible. An empty slice
// is a no-op.
//
// The BeforeSave and BeforeInsert hooks run for every row first, then the
// rows are written, then AfterInsert and AfterSave run per row (unless
// [BulkNoHooks] is given). AfterInsert/AfterSave run after each batch, so a
// failure in a later batch leaves the hooks of the earlier, already written
// rows fired; wrap the call in [Tx] for all-or-nothing behaviour.
//
// When the dialect implements [BulkInserter] (PostgreSQL COPY), no autoinc
// key needs to be populated (see [StructField.IsAutoInc]) and [BulkIgnore] is
// not set, every row is exported and handed to it. Otherwise multi-row
// INSERT statements are issued in batches of at most [BulkBatchSize] rows,
// further limited so that rows × columns stays under the engine's parameter
// limit (65535 on PostgreSQL and MySQL, 32766 on SQLite). Rows whose autoinc
// field is zero are inserted without that column so the database generates
// it; a batch never mixes rows with and without the column, so mixing them
// costs extra statements but keeps the insertion order.
//
// Generated keys are populated as follows: on engines with RETURNING
// (PostgreSQL, CockroachDB) the whole row is returned and scanned back into
// the objects in order (the objects are refreshed like [Insert] does); on
// MySQL the autoinc field is set to LastInsertId + row index, which is
// correct for a single multi-row INSERT because InnoDB allocates the values
// of one statement consecutively (innodb_autoinc_lock_mode 0, 1 and 2 alike
// for "simple inserts", as long as no row in the batch supplies its own
// value, which BulkInsert guarantees); on SQLite rowids of one multi-row
// statement are not guaranteed to be consecutive, so the field is left
// unset (use [Insert] when the ids matter). Query failures are returned as
// an [*Error].
func (t *TableMeta[T]) BulkInsert(ctx context.Context, rows []*T, opts ...BulkOption) error {
	if t == nil {
		return ErrNotReady
	}
	if len(rows) == 0 {
		return nil
	}
	o := &bulkOptions{batchSize: DefaultBulkBatchSize}
	for _, opt := range opts {
		opt(o)
	}
	t.check(ctx)

	be := GetBackend(ctx)
	engine := be.Engine()
	bt := t.bind(be)

	if !o.noHooks {
		for _, r := range rows {
			if h, ok := any(r).(BeforeSaveHook); ok {
				if err := h.BeforeSave(ctx); err != nil {
					return err
				}
			}
			if h, ok := any(r).(BeforeInsertHook); ok {
				if err := h.BeforeInsert(ctx); err != nil {
					return err
				}
			}
		}
	}

	// which rows leave the autoinc column out (the database generates it)
	omit := make([]bool, len(rows))
	needsKey := false
	for i, r := range rows {
		omit[i] = omitAutoInc(bt, reflect.ValueOf(r).Elem())
		needsKey = needsKey || omit[i]
	}
	if !needsKey && bt.autoInc == nil {
		// legacy heuristic: a zero single-integer primary key is populated
		// from the generated value by Insert, keep that behaviour here
		if k := t.autoIncrementField(bt); k != nil {
			for _, r := range rows {
				if reflect.ValueOf(r).Elem().Field(k.Index).IsZero() {
					needsKey = true
					break
				}
			}
		}
	}

	if bi, ok := engine.dialect().(BulkInserter); ok && !o.ignore && !needsKey {
		err := t.bulkNative(ctx, be, bt, bi, rows)
		if err == nil {
			return t.bulkAfterHooks(ctx, o, rows)
		}
		if !errors.Is(err, ErrNotSupported) {
			return err
		}
		// the dialect declined (e.g. a type COPY cannot carry): fall back
	}

	shapes := insertShapes(bt)
	// batch size: rows × columns must fit the parameter limit
	batch := o.batchSize
	if limit := maxBulkParams(engine) / len(bt.fields); limit < 1 {
		batch = 1
	} else if limit < batch {
		batch = limit
	}

	start := 0
	for start < len(rows) {
		end := start + 1
		for end < len(rows) && end-start < batch && omit[end] == omit[start] {
			end++
		}
		shape := shapes[0]
		if omit[start] {
			shape = shapes[1]
		}
		if err := t.bulkBatch(ctx, be, bt, o, shape, rows[start:end]); err != nil {
			return err
		}
		if err := t.bulkAfterHooks(ctx, o, rows[start:end]); err != nil {
			return err
		}
		start = end
	}
	return nil
}

// bulkAfterHooks fires AfterInsert/AfterSave for rows unless disabled.
func (t *TableMeta[T]) bulkAfterHooks(ctx context.Context, o *bulkOptions, rows []*T) error {
	if o.noHooks {
		return nil
	}
	for _, r := range rows {
		if h, ok := any(r).(AfterInsertHook); ok {
			if err := h.AfterInsert(ctx); err != nil {
				return err
			}
		}
		if h, ok := any(r).(AfterSaveHook); ok {
			if err := h.AfterSave(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// bulkNative exports every row and hands them to the dialect's BulkInserter.
func (t *TableMeta[T]) bulkNative(ctx context.Context, be *Backend, bt *boundTable, bi BulkInserter, rows []*T) error {
	engine := be.Engine()
	cols := make([]string, len(bt.fields))
	for i, f := range bt.fields {
		cols[i] = f.Column
	}
	data := make([][]any, len(rows))
	for i, r := range rows {
		val := reflect.ValueOf(r).Elem()
		params := make([]any, len(bt.fields))
		for n, f := range bt.fields {
			params[n] = exportField(engine, val.Field(f.Index), f)
		}
		data[i] = params
	}
	if _, err := bi.BulkInsert(ctx, be, bt.name, cols, data); err != nil {
		if errors.Is(err, ErrNotSupported) {
			return err
		}
		logQueryError(ctx, "psql:bulk_insert:run_fail", bt.name, "COPY "+bt.name, err)
		return &Error{Query: "COPY " + QuoteName(bt.name) + " (" + bt.fldStr + ")", Err: err}
	}
	return nil
}

// bulkSQL renders a multi-row INSERT for n rows of shape, with RETURNING
// when requested.
func (t *TableMeta[T]) bulkSQL(engine Engine, bt *boundTable, o *bulkOptions, shape insertShape, n int, useReturning bool) string {
	ncols := len(shape.fields)
	var values strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			values.WriteString("),(")
		}
		values.WriteString(engine.Placeholders(ncols, i*ncols+1))
	}
	// the UpsertRenderer wraps the placeholder list in "VALUES (" ... ")":
	// the "),(" separators above turn that into a multi-row VALUES list.
	ph := values.String()

	var req string
	if o.ignore {
		if ur, ok := engine.dialect().(UpsertRenderer); ok {
			req = ur.InsertIgnoreSQL(bt.name, shape.fldStr, ph)
		} else {
			req = "INSERT IGNORE INTO " + QuoteName(bt.name) + " (" + shape.fldStr + ") VALUES (" + ph + ")"
		}
	} else {
		req = "INSERT INTO " + QuoteName(bt.name) + " (" + shape.fldStr + ") VALUES (" + ph + ")"
	}
	if useReturning {
		req += " RETURNING " + bt.fldStr
	}
	return req
}

// bulkBatch writes one batch of rows sharing shape with a single statement
// and populates generated keys.
func (t *TableMeta[T]) bulkBatch(ctx context.Context, be *Backend, bt *boundTable, o *bulkOptions, shape insertShape, rows []*T) error {
	engine := be.Engine()
	useReturning := false
	if rr, ok := engine.dialect().(ReturningRenderer); ok && !o.ignore {
		useReturning = rr.SupportsReturning()
	}
	req := t.bulkSQL(engine, bt, o, shape, len(rows), useReturning)

	params := make([]any, 0, len(rows)*len(shape.fields))
	for _, r := range rows {
		val := reflect.ValueOf(r).Elem()
		for _, f := range shape.fields {
			params = append(params, exportField(engine, val.Field(f.Index), f))
		}
	}

	if useReturning {
		res, err := doQueryContext(ctx, req, params...)
		if err != nil {
			logQueryError(ctx, "psql:bulk_insert:run_fail", bt.name, req, err)
			return &Error{Query: req, Err: err}
		}
		return t.scanReturningRows(ctx, res, rows)
	}

	res, err := ExecContext(ctx, req, params...)
	if err != nil {
		logQueryError(ctx, "psql:bulk_insert:run_fail", bt.name, req, err)
		return &Error{Query: req, Err: err}
	}
	if engine == EngineMySQL && !o.ignore {
		// InnoDB allocates the ids of one multi-row statement consecutively
		// and LastInsertId reports the first one
		switch {
		case shape.omitAutoInc:
			t.applyBulkInsertIds(res, bt.autoInc, rows)
		case bt.autoInc == nil:
			// legacy heuristic: a zero single-integer primary key was sent as
			// 0, which an AUTO_INCREMENT column replaces with generated ids
			if k := t.autoIncrementField(bt); k != nil && allZeroField(k, rows) {
				t.applyBulkInsertIds(res, k, rows)
			}
		}
	}
	return nil
}

// scanReturningRows scans the rows returned by a multi-row INSERT ...
// RETURNING back into targets, in order, then closes res.
func (t *TableMeta[T]) scanReturningRows(ctx context.Context, res *sql.Rows, targets []*T) error {
	defer res.Close()
	plan, err := t.newScanPlan(ctx, res)
	if err != nil {
		return err
	}
	i := 0
	for res.Next() {
		if i >= len(targets) {
			break
		}
		if err := plan.scan(ctx, res, targets[i], false); err != nil {
			return err
		}
		i++
	}
	return res.Err()
}

// applyBulkInsertIds sets f on every target to LastInsertId + index.
func (t *TableMeta[T]) applyBulkInsertIds(res sql.Result, f *StructField, targets []*T) {
	if f == nil {
		return
	}
	first, err := res.LastInsertId()
	if err != nil || first == 0 {
		return
	}
	for i, target := range targets {
		fv := reflect.ValueOf(target).Elem().Field(f.Index)
		if fv.Kind() == reflect.Ptr {
			fv.Set(reflect.New(fv.Type().Elem()))
			fv = fv.Elem()
		}
		id := first + int64(i)
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
}

// allZeroField reports whether field f is zero on every target.
func allZeroField[T any](f *StructField, targets []*T) bool {
	for _, target := range targets {
		if !reflect.ValueOf(target).Elem().Field(f.Index).IsZero() {
			return false
		}
	}
	return true
}
