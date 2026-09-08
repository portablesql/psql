package psql

import (
	"fmt"
	"strings"
)

const (
	// NameQuoteChar is the identifier quote character (" for ANSI SQL; MySQL
	// is run in ANSI_QUOTES mode so it accepts it too).
	NameQuoteChar = `"`
	// NameQuoteRune is NameQuoteChar as a rune.
	NameQuoteRune = '"'
)

// F allows passing a field name to the query builder. It can be used in multiple ways:
//
// psql.F("field")
// psql.F("table.field")
// psql.F("table.*")
// psql.F("", "field.with.dots")
// psql.F("table", "field")
// psql.F("table.with.dots", "field.with.dots")
// and more...
func F(field ...string) EscapeValueable {
	switch len(field) {
	case 1:
		return fieldName(field[0])
	case 2:
		return &fullField{tableName(field[0]), fieldName(field[1])}
	default:
		panic(fmt.Sprintf("psql.F() expects only one or two args, got %d", len(field)))
	}
}

// S creates a sort expression for ORDER BY clauses. The last argument may be
// "ASC" or "DESC" to set the sort direction. If omitted, the database default
// (typically ASC) is used:
//
//	psql.S("name", "ASC")         // "name" ASC
//	psql.S("table", "field", "DESC") // "table"."field" DESC
func S(field ...string) SortValueable {
	// same as F but last value must be "ASC" or "DESC"
	if len(field) == 0 {
		panic("psql.S() expects at least one arg, got none")
	}
	last := field[len(field)-1]
	switch last {
	case "ASC", "DESC":
		return &ordField{ord: last, fld: F(field[:len(field)-1]...)}
	default:
		return &ordField{ord: "", fld: F(field...)}
	}
}

type fieldName string

type ordField struct {
	ord string // "ASC" or "DESC"
	fld EscapeValueable
}

func (f *ordField) sortEscapeValue() string {
	if f.ord == "" {
		return f.fld.EscapeValue()
	}
	return f.fld.EscapeValue() + " " + f.ord
}

type fullField struct {
	tableName
	fieldName
}

func (f fieldName) String() string {
	return string(f)
}

func (f fieldName) EscapeValue() string {
	if f == "*" {
		// special case
		return "*"
	}
	if tbl, ok := strings.CutSuffix(string(f), ".*"); ok && tbl != "" {
		// "table.*" → "table".*
		return QuoteName(tbl) + ".*"
	}
	// we consider table names won't contain dots, if it do use fullField instead of fieldName
	return NameQuoteChar + strings.Replace(strings.ReplaceAll(string(f), NameQuoteChar, NameQuoteChar+NameQuoteChar), ".", NameQuoteChar+"."+NameQuoteChar, 1) + NameQuoteChar
}

func (f fieldName) sortEscapeValue() string {
	return f.EscapeValue()
}

func (f *fullField) String() string {
	return f.EscapeValue()
}

func (f *fullField) EscapeValue() string {
	if f.tableName == "" {
		return QuoteName(string(f.fieldName))
	}
	if f.fieldName == "*" {
		return QuoteName(string(f.tableName)) + ".*"
	}
	return QuoteName(string(f.tableName)) + "." + QuoteName(string(f.fieldName))
}

func (f *fullField) sortEscapeValue() string {
	return f.EscapeValue()
}

type tableName string

func (t tableName) String() string {
	return string(t)
}

func (t tableName) EscapeTable() string {
	return QuoteName(string(t))
}

// aliasedTable is a table reference with an alias, rendered as "name" AS "alias".
type aliasedTable struct {
	name  tableName
	alias string
}

func (a *aliasedTable) String() string {
	return string(a.name) + " AS " + a.alias
}

func (a *aliasedTable) EscapeTable() string {
	return a.name.EscapeTable() + " AS " + QuoteName(a.alias)
}

// parseTableRef parses a table reference given as a string. "name",
// "name alias" and "name AS alias" are supported; anything else is quoted
// as a single identifier.
func parseTableRef(s string) EscapeTableable {
	parts := strings.Fields(s)
	switch len(parts) {
	case 2:
		return &aliasedTable{name: tableName(parts[0]), alias: parts[1]}
	case 3:
		if strings.EqualFold(parts[1], "AS") {
			return &aliasedTable{name: tableName(parts[0]), alias: parts[2]}
		}
	}
	return tableName(s)
}

// QuoteName quotes a SQL identifier (table name, column name, etc.) with double quotes,
// escaping any embedded double quotes. This does not apply naming strategy transformations.
func QuoteName(v string) string {
	pos := strings.IndexByte(v, NameQuoteRune)
	if pos == -1 {
		return NameQuoteChar + v + NameQuoteChar
	} else {
		return NameQuoteChar + strings.ReplaceAll(v, NameQuoteChar, NameQuoteChar+NameQuoteChar) + NameQuoteChar
	}
}
