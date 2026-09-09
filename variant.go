package psql

// Variant identifies the concrete server product behind an [Engine]. An engine
// groups wire-compatible products (PostgreSQL and CockroachDB, MySQL and
// MariaDB); the variant tells them apart where their SQL features differ
// (CockroachDB has AS OF SYSTEM TIME and row TTL but no advisory locks or
// LISTEN/NOTIFY, MariaDB has INSERT ... RETURNING while MySQL does not).
//
// Driver submodules detect the variant when connecting and pass it with
// [WithVariant]; [Backend.Variant] reports it, falling back to the engine's
// primary product when nothing was detected.
type Variant int

const (
	VariantUnknown     Variant = iota // not detected
	VariantMySQL                      // Oracle MySQL
	VariantMariaDB                    // MariaDB
	VariantPostgreSQL                 // PostgreSQL
	VariantCockroachDB                // CockroachDB
	VariantSQLite                     // SQLite
)

// String returns a human readable name for the variant.
func (v Variant) String() string {
	switch v {
	case VariantMySQL:
		return "MySQL"
	case VariantMariaDB:
		return "MariaDB"
	case VariantPostgreSQL:
		return "PostgreSQL"
	case VariantCockroachDB:
		return "CockroachDB"
	case VariantSQLite:
		return "SQLite"
	default:
		return "Unknown"
	}
}

// Engine returns the engine the variant belongs to.
func (v Variant) Engine() Engine {
	switch v {
	case VariantMySQL, VariantMariaDB:
		return EngineMySQL
	case VariantPostgreSQL, VariantCockroachDB:
		return EnginePostgreSQL
	case VariantSQLite:
		return EngineSQLite
	default:
		return EngineUnknown
	}
}

// defaultVariant returns the primary product of an engine, used when no
// variant was detected.
func (e Engine) defaultVariant() Variant {
	switch e {
	case EngineMySQL:
		return VariantMySQL
	case EnginePostgreSQL:
		return VariantPostgreSQL
	case EngineSQLite:
		return VariantSQLite
	default:
		return VariantUnknown
	}
}

// WithVariant records the detected server product (see [Variant]). Driver
// submodules call this after inspecting the server version.
func WithVariant(v Variant) BackendOption {
	return func(b *Backend) {
		b.variant = v
	}
}

// WithServerVersion records the server version string reported by the
// database (for example "16.2" or "CockroachDB CCL v24.1.0"), available
// through [Backend.ServerVersion].
func WithServerVersion(version string) BackendOption {
	return func(b *Backend) {
		b.serverVersion = version
	}
}

// Variant returns the detected server product, or the engine's primary
// product when the driver did not report one.
func (be *Backend) Variant() Variant {
	if be == nil {
		return VariantUnknown
	}
	if be.variant != VariantUnknown {
		return be.variant
	}
	return be.engine.defaultVariant()
}

// ServerVersion returns the version string reported by the server when the
// backend was created, or "" if the driver did not record one.
func (be *Backend) ServerVersion() string {
	if be == nil {
		return ""
	}
	return be.serverVersion
}
