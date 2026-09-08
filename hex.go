package psql

import (
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"fmt"
)

// Hex is a binary value stored as a hexadecimal string in the database.
// It implements [sql.Scanner] and [driver.Valuer] so it can be used directly
// as a struct field type or as a query value.
type Hex []byte

// Scan implements [sql.Scanner], decoding a hexadecimal string. A NULL
// (nil) source yields an empty value.
func (h *Hex) Scan(src interface{}) error {
	var v []byte
	var err error

	switch s := src.(type) {
	case nil:
		*h = Hex{}
		return nil
	case string:
		v, err = hex.DecodeString(s)
	case []byte:
		v, err = hex.DecodeString(string(s))
	case sql.RawBytes:
		v, err = hex.DecodeString(string(s))
	default:
		return fmt.Errorf("unsupported input format %T", src)
	}
	if err != nil {
		return err
	}
	*h = v
	return nil
}

// Value implements [driver.Valuer], encoding the bytes as a hexadecimal string.
func (h Hex) Value() (driver.Value, error) {
	// encode to hex
	v := hex.EncodeToString(h)

	return v, nil
}
