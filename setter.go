package psql

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"
)

// findSetter returns the scan function for values of type t, panicking if the
// type is not supported. See lookupSetter for the supported types.
func findSetter(t reflect.Type) func(v reflect.Value, from sql.RawBytes) error {
	s, err := lookupSetter(t)
	if err != nil {
		panic("psql: " + err.Error())
	}
	return s
}

// lookupSetter returns the scan function for values of type t (pointers are
// dereferenced). Supported are types implementing sql.Scanner, time.Time,
// bool, string, all int and uint kinds, float32/64 and []byte.
func lookupSetter(t reflect.Type) (func(v reflect.Value, from sql.RawBytes) error, error) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if reflect.PointerTo(t).Implements(reflect.TypeFor[sql.Scanner]()) {
		return scanSetter, nil
	}

	// most specific types
	switch t {
	case reflect.TypeFor[time.Time]():
		return timeSetter, nil
	}

	// fallbacks
	switch t.Kind() {
	case reflect.Bool:
		return boolSetter, nil
	case reflect.String:
		return stringSetter, nil
	case reflect.Int8, reflect.Int16, reflect.Int32:
		return int32Setter, nil
	case reflect.Int64, reflect.Int:
		return int64Setter, nil
	case reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return uint32Setter, nil
	case reflect.Uint64, reflect.Uint:
		return uint64Setter, nil
	case reflect.Float32:
		return float32Setter, nil
	case reflect.Float64:
		return float64Setter, nil
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return bytesSetter, nil
		}
	}
	return nil, fmt.Errorf("unsupported type %s (supported: sql.Scanner implementations, time.Time, bool, string, int/uint kinds, float32/64, []byte, and pointers to these)", t)
}

func scanSetter(v reflect.Value, from sql.RawBytes) error {
	// v implements Scanner, so we should call v.Scan(from)
	v = v.Addr() // use the pointer version

	intf := v.Interface().(sql.Scanner)

	return intf.Scan([]byte(from))
}

func boolSetter(v reflect.Value, from sql.RawBytes) error {
	// expect "from" to be 1 or 0
	switch string(from) {
	case "1", "true", "TRUE", "True":
		v.SetBool(true)
		return nil
	case "0", "false", "FALSE", "False":
		v.SetBool(false)
		return nil
	default:
		return errors.New("invalid bool value")
	}
}

func stringSetter(v reflect.Value, from sql.RawBytes) error {
	v.SetString(string(from))
	return nil
}

func int32Setter(v reflect.Value, from sql.RawBytes) error {
	n, err := strconv.ParseInt(string(from), 10, v.Type().Bits())
	if err != nil {
		return err
	}
	v.SetInt(n)
	return nil
}

func uint32Setter(v reflect.Value, from sql.RawBytes) error {
	n, err := strconv.ParseUint(string(from), 10, v.Type().Bits())
	if err != nil {
		return err
	}
	v.SetUint(n)
	return nil
}

func int64Setter(v reflect.Value, from sql.RawBytes) error {
	n, err := strconv.ParseInt(string(from), 10, 64)
	if err != nil {
		return err
	}
	v.SetInt(n)
	return nil
}

func uint64Setter(v reflect.Value, from sql.RawBytes) error {
	n, err := strconv.ParseUint(string(from), 10, 64)
	if err != nil {
		return err
	}
	v.SetUint(n)
	return nil
}

func float32Setter(v reflect.Value, from sql.RawBytes) error {
	n, err := strconv.ParseFloat(string(from), 32)
	if err != nil {
		return err
	}
	v.SetFloat(n)
	return nil
}

func float64Setter(v reflect.Value, from sql.RawBytes) error {
	n, err := strconv.ParseFloat(string(from), 64)
	if err != nil {
		return err
	}
	v.SetFloat(n)
	return nil
}

func bytesSetter(v reflect.Value, from sql.RawBytes) error {
	if from == nil {
		v.SetBytes(nil)
		return nil
	}
	cp := make([]byte, len(from))
	copy(cp, from)
	v.SetBytes(cp)
	return nil
}

// makeJSONSetter returns a setter that deserializes JSON from the database into
// a Go value of the given type. Used for fields with format=json.
func makeJSONSetter(t reflect.Type) func(v reflect.Value, from sql.RawBytes) error {
	return func(v reflect.Value, from sql.RawBytes) error {
		if len(from) == 0 {
			v.Set(reflect.Zero(t))
			return nil
		}
		ptr := reflect.New(t)
		if err := json.Unmarshal(from, ptr.Interface()); err != nil {
			return fmt.Errorf("json unmarshal: %w", err)
		}
		v.Set(ptr.Elem())
		return nil
	}
}

func timeSetter(v reflect.Value, from sql.RawBytes) error {
	// parse date
	if len(from) == 0 {
		v.Set(reflect.ValueOf(time.Time{}))
		return nil
	}
	// RFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
	if bytes.IndexByte(from, 'T') != -1 {
		// this is a RFC3339 date
		t, err := time.Parse(time.RFC3339Nano, string(from))
		if err != nil {
			return err
		}
		v.Set(reflect.ValueOf(t))
		return nil
	}

	const base = "2006-01-02 15:04:05.999999"
	const zero = "0000-00-00 00:00:00.000000"
	switch len(from) {
	case 10, 19, 21, 22, 23, 24, 25, 26: // up to "YYYY-MM-DD HH:MM:SS.MMMMMM"
		if string(from) == zero[:len(from)] {
			// zero time
			v.Set(reflect.ValueOf(time.Time{}))
			return nil
		}

		// In the absence of a time zone indicator, Parse returns a time in UTC.
		t, err := time.Parse(base[:len(from)], string(from))
		if err != nil {
			return err
		}
		v.Set(reflect.ValueOf(t))
		return nil
	}
	return fmt.Errorf("failed to parse time: %s", from)
}
