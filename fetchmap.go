package psql

import (
	"context"
	"fmt"
	"reflect"
)

// FetchMapped fetches records and returns them as a map keyed by the string
// representation of the given field. key may be a Go field name or a column
// name; an unknown key returns [ErrUnknownField]. Each map entry holds a single
// record (last wins if duplicates exist). All [FetchOptions] are honored as in
// [Fetch].
func FetchMapped[T any](ctx context.Context, where any, key string, opts ...*FetchOptions) (map[string]*T, error) {
	return Table[T]().FetchMapped(ctx, where, key, opts...)
}

// FetchMapped fetches records matching where and maps them by key. See [FetchMapped].
func (t *TableMeta[T]) FetchMapped(ctx context.Context, where any, key string, opts ...*FetchOptions) (map[string]*T, error) {
	if t == nil {
		return nil, ErrNotReady
	}
	final := make(map[string]*T)
	err := t.fetchKeyed(ctx, "FetchMapped", where, key, opts, func(k string, v *T) {
		final[k] = v
	})
	if err != nil {
		return nil, err
	}
	return final, nil
}

// FetchGrouped fetches records and returns them grouped by the string
// representation of the given field. key may be a Go field name or a column
// name; an unknown key returns [ErrUnknownField]. Each map entry holds the
// slice of matching records, in query order. All [FetchOptions] are honored as
// in [Fetch].
func FetchGrouped[T any](ctx context.Context, where map[string]any, key string, opts ...*FetchOptions) (map[string][]*T, error) {
	return Table[T]().FetchGrouped(ctx, where, key, opts...)
}

// FetchGrouped fetches records matching where and groups them by key. See [FetchGrouped].
func (t *TableMeta[T]) FetchGrouped(ctx context.Context, where any, key string, opts ...*FetchOptions) (map[string][]*T, error) {
	if t == nil {
		return nil, ErrNotReady
	}
	final := make(map[string][]*T)
	err := t.fetchKeyed(ctx, "FetchGrouped", where, key, opts, func(k string, v *T) {
		final[k] = append(final[k], v)
	})
	if err != nil {
		return nil, err
	}
	return final, nil
}

// fetchKeyed is the shared implementation of FetchMapped and FetchGrouped: it
// validates key, runs the same query as Fetch and hands each record with its
// key string to add.
func (t *TableMeta[T]) fetchKeyed(ctx context.Context, op string, where any, key string, opts []*FetchOptions, add func(string, *T)) error {
	t.check(ctx)
	opt := resolveFetchOpts(opts)
	bt := t.bind(GetBackend(ctx))

	fld := t.fieldByNameOrColumn(bt, key)
	if fld == nil {
		return fmt.Errorf("%s: %w: %q is not a field or column of %s", op, ErrUnknownField, key, t.table)
	}

	req := t.selectQuery(bt, where, opt, true)

	rows, err := req.RunQuery(ctx)
	if err != nil {
		logQueryError(ctx, "psql:"+op+":run_fail", t.table, "", err)
		return err
	}

	final, err := t.spawnAll(ctx, rows) // closes rows
	if err != nil {
		return err
	}

	if len(opt.Preload) > 0 && len(final) > 0 {
		if err := PreloadOpts(ctx, final, opt, opt.Preload...); err != nil {
			return err
		}
	}

	for _, val := range final {
		add(keyString(reflect.ValueOf(val).Elem().Field(fld.Index)), val)
	}
	return nil
}

// keyString renders a field value as a map key, dereferencing pointers.
func keyString(v reflect.Value) string {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return "<nil>"
		}
		v = v.Elem()
	}
	// TODO avoid using fmt.Sprintf to convert value back to string
	return fmt.Sprintf("%v", v.Interface())
}
