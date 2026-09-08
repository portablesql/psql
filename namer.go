package psql

// Namer maps the names declared in Go to the names used in SQL. It is
// configured per [Backend] with [WithNamer] or [Backend.SetNamer]; the default
// is [LegacyNamer]. This is based on gorm.
//
// TableName is applied once to the Go type name of tables that have no
// explicit psql.Name; ColumnName is applied once to the Go field name of
// columns that have no explicit name in their sql tag. Explicit names are
// always used as-is.
type Namer interface {
	TableName(table string) string
	SchemaName(table string) string
	ColumnName(table, column string) string
	JoinTableName(joinTable string) string
	CheckerName(table, column string) string
	IndexName(table, column string) string
	UniqueName(table, column string) string
	EnumTypeName(table, column string) string // For PostgreSQL ENUM types
}

// DefaultNamer is a namer that returns names as they are provided
type DefaultNamer struct{}

// TableName returns the table name as is
func (DefaultNamer) TableName(table string) string {
	return table
}

// SchemaName returns the schema name as is
func (DefaultNamer) SchemaName(table string) string {
	return table
}

// ColumnName returns the column name as is
func (DefaultNamer) ColumnName(table, column string) string {
	return column
}

// JoinTableName returns the join table name as is
func (DefaultNamer) JoinTableName(joinTable string) string {
	return joinTable
}

// CheckerName returns the checker name as is
func (DefaultNamer) CheckerName(table, column string) string {
	return "chk_" + table + "_" + column
}

// IndexName returns the index name as is
func (DefaultNamer) IndexName(table, column string) string {
	return "idx_" + table + "_" + column
}

// UniqueName returns the unique constraint name as is
func (DefaultNamer) UniqueName(table, column string) string {
	return "uniq_" + table + "_" + column
}

// EnumTypeName returns the enum type name using original table and column names
func (DefaultNamer) EnumTypeName(table, column string) string {
	return "enum_" + table + "_" + column
}

// CamelSnakeNamer is a namer that converts names to Camel_Snake_Case
type CamelSnakeNamer struct{}

// TableName returns the table name in Camel_Snake_Case format
func (CamelSnakeNamer) TableName(table string) string {
	return formatCamelSnakeCase(table)
}

// SchemaName returns the schema name in Camel_Snake_Case format
func (CamelSnakeNamer) SchemaName(table string) string {
	return formatCamelSnakeCase(table)
}

// ColumnName returns the column name in Camel_Snake_Case format
func (CamelSnakeNamer) ColumnName(table, column string) string {
	return formatCamelSnakeCase(column)
}

// JoinTableName returns the join table name in Camel_Snake_Case format
func (CamelSnakeNamer) JoinTableName(joinTable string) string {
	return formatCamelSnakeCase(joinTable)
}

// CheckerName returns the checker name with table and column in Camel_Snake_Case format
func (CamelSnakeNamer) CheckerName(table, column string) string {
	return "chk_" + formatCamelSnakeCase(table) + "_" + formatCamelSnakeCase(column)
}

// IndexName returns the index name with table and column in Camel_Snake_Case format
func (CamelSnakeNamer) IndexName(table, column string) string {
	return "idx_" + formatCamelSnakeCase(table) + "_" + formatCamelSnakeCase(column)
}

// UniqueName returns the unique constraint name with table and column in Camel_Snake_Case format
func (CamelSnakeNamer) UniqueName(table, column string) string {
	return "uniq_" + formatCamelSnakeCase(table) + "_" + formatCamelSnakeCase(column)
}

// EnumTypeName returns the enum type name with table and column in Camel_Snake_Case format
func (CamelSnakeNamer) EnumTypeName(table, column string) string {
	return "enum_" + formatCamelSnakeCase(table) + "_" + formatCamelSnakeCase(column)
}

// LegacyNamer reproduces the behavior of the original implementation:
// - Table names use CamelSnakeCase (through [FormatTableName])
// - Column names are kept as is (no transformation)
// - Other names use standard prefixes with the original names
//
// It is the default namer, so existing databases keep working unchanged.
type LegacyNamer struct{}

// TableName returns the table name in Camel_Snake_Case format (original
// behavior): "UserProfile" becomes "User_Profile". It uses the [FormatTableName]
// variable so that overriding it keeps taking effect.
func (LegacyNamer) TableName(table string) string {
	return FormatTableName(table)
}

// SchemaName returns the schema name in Camel_Snake_Case format
func (LegacyNamer) SchemaName(table string) string {
	return formatCamelSnakeCase(table)
}

// ColumnName returns the column name as is (no transformation)
func (LegacyNamer) ColumnName(table, column string) string {
	return column
}

// JoinTableName returns the join table name in Camel_Snake_Case format
func (LegacyNamer) JoinTableName(joinTable string) string {
	return formatCamelSnakeCase(joinTable)
}

// CheckerName returns the checker name with table in Camel_Snake_Case format and original column name
func (LegacyNamer) CheckerName(table, column string) string {
	return "chk_" + formatCamelSnakeCase(table) + "_" + column
}

// IndexName returns the index name with table in Camel_Snake_Case format and original column name
func (LegacyNamer) IndexName(table, column string) string {
	return "idx_" + formatCamelSnakeCase(table) + "_" + column
}

// UniqueName returns the unique constraint name with table in Camel_Snake_Case format and original column name
func (LegacyNamer) UniqueName(table, column string) string {
	return "uniq_" + formatCamelSnakeCase(table) + "_" + column
}

// EnumTypeName returns the enum type name with table in Camel_Snake_Case format and original column name
func (LegacyNamer) EnumTypeName(table, column string) string {
	return "enum_" + formatCamelSnakeCase(table) + "_" + column
}
