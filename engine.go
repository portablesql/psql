package psql

// Engine identifies the SQL database engine in use.
type Engine int

const (
	EngineUnknown    Engine = iota // Unknown or unset engine
	EngineMySQL                    // MySQL / MariaDB
	EnginePostgreSQL               // PostgreSQL / CockroachDB
	EngineSQLite                   // SQLite (via modernc.org/sqlite)
)

// String returns a human readable name for the engine.
func (e Engine) String() string {
	switch e {
	case EngineMySQL:
		return "MySQL Engine"
	case EnginePostgreSQL:
		return "PostgreSQL Engine"
	case EngineSQLite:
		return "SQLite Engine"
	default:
		return "Unknown Engine"
	}
}
