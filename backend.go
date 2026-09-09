package psql

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

// Backend represents a database connection with engine-specific behavior.
// Create one with [New], or use a submodule constructor (e.g., mysql.New, pgsql.New,
// sqlite.New), then attach it to a context with [Backend.Plug] or [ContextBackend].
type Backend struct {
	db            *sql.DB
	driverData    any // engine-specific data (e.g., *pgxpool.Pool)
	engine        Engine
	checked       map[reflect.Type]*tableCheck
	checkedLk     sync.RWMutex
	namer         Namer // custom namer for table/column names
	noSchemaCheck bool  // automatic CREATE/ALTER TABLE disabled (WithSchemaCheck(false))
	variant       Variant
	serverVersion string
}

// tableCheck tracks the schema check of one table type on a backend. lk
// serializes concurrent checks of the same table; done is only set once a
// check succeeded, so a failed check is retried on the next operation.
type tableCheck struct {
	lk   sync.Mutex
	done atomic.Bool
}

// New returns a [Backend] that connects to the database identified by dsn.
// The engine is auto-detected by trying registered [BackendFactory] implementations.
// Import a database submodule (e.g., _ "github.com/portablesql/psql-sqlite") to
// register its factory.
func New(dsn string) (*Backend, error) {
	for _, f := range backendFactories {
		if f.MatchDSN(dsn) {
			return f.CreateBackend(dsn)
		}
	}
	return nil, fmt.Errorf("no backend factory matches DSN: %s (did you import a database driver submodule?)", dsn)
}

// NewBackend creates a Backend with the given engine and *sql.DB. This is called
// by submodule factories to construct backends.
func NewBackend(engine Engine, db *sql.DB, opts ...BackendOption) *Backend {
	b := &Backend{
		db:      db,
		engine:  engine,
		checked: make(map[reflect.Type]*tableCheck),
		namer:   &LegacyNamer{},
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// BackendOption is a functional option for [NewBackend].
type BackendOption func(*Backend)

// WithDriverData sets engine-specific driver data (e.g., *pgxpool.Pool for PostgreSQL).
func WithDriverData(data any) BackendOption {
	return func(b *Backend) {
		b.driverData = data
	}
}

// WithNamer sets the naming strategy for table/column names.
func WithNamer(n Namer) BackendOption {
	return func(b *Backend) {
		b.namer = n
	}
}

// WithSchemaCheck enables or disables the automatic schema check (CREATE
// TABLE / ALTER TABLE) performed the first time each table is used on the
// backend. It is enabled by default. When disabled, no DDL is ever issued
// implicitly; call [Backend.CheckStructure] explicitly (e.g. at startup) for
// the tables that should be created or migrated.
func WithSchemaCheck(enabled bool) BackendOption {
	return func(b *Backend) {
		b.noSchemaCheck = !enabled
	}
}

// WithPoolDefaults configures standard connection pool settings (128 max open,
// 32 max idle, 3 min lifetime).
func WithPoolDefaults(b *Backend) {
	b.db.SetConnMaxLifetime(time.Minute * 3)
	b.db.SetMaxOpenConns(128)
	b.db.SetMaxIdleConns(32)
}

// Close releases the backend's connections: it closes the underlying *sql.DB
// and, when the driver data (see [Backend.DriverData]) has a Close method
// (for example a *pgxpool.Pool), closes it too. The backend must not be used
// afterwards.
func (be *Backend) Close() error {
	if be == nil || be.db == nil {
		return nil
	}
	err := be.db.Close()
	switch c := be.driverData.(type) {
	case interface{ Close() error }:
		if cerr := c.Close(); err == nil {
			err = cerr
		}
	case interface{ Close() }:
		c.Close()
	}
	return err
}

// Plug attaches this backend to the given context. All psql operations using
// the returned context will use this backend. Equivalent to [ContextBackend].
func (be *Backend) Plug(ctx context.Context) context.Context {
	return ContextBackend(ctx, be)
}

// DB returns the underlying *sql.DB connection. Panics if the backend is nil.
func (be *Backend) DB() *sql.DB {
	if be == nil {
		panic("attempting to perform DB operations without backend")
	}
	return be.db
}

// Engine returns the database engine type ([EngineMySQL], [EnginePostgreSQL], or [EngineSQLite]).
func (be *Backend) Engine() Engine {
	if be == nil {
		return EngineUnknown
	}
	return be.engine
}

// DriverData returns the engine-specific driver data (e.g., *pgxpool.Pool for PostgreSQL).
func (be *Backend) DriverData() any {
	if be == nil {
		return nil
	}
	return be.driverData
}

// Namer returns the configured naming strategy
func (be *Backend) Namer() Namer {
	if be == nil || be.namer == nil {
		// If backend or namer is nil, return LegacyNamer for backward compatibility
		return &LegacyNamer{}
	}
	return be.namer
}

// SetNamer allows changing the naming strategy
// Use DefaultNamer to keep names exactly as defined in structs (e.g., "HelloWorld" stays "HelloWorld")
// Use LegacyNamer (default) for backward compatibility (e.g., "HelloWorld" becomes "Hello_World")
// Use CamelSnakeNamer to convert all names to Camel_Snake_Case
func (be *Backend) SetNamer(n Namer) {
	if be == nil {
		return
	}
	be.namer = n
}

// SchemaCheckEnabled reports whether the automatic schema check is enabled
// (see [WithSchemaCheck]).
func (be *Backend) SchemaCheckEnabled() bool {
	return be != nil && !be.noSchemaCheck
}

// CheckStructure verifies that the table described by tv exists in the
// database with the expected columns and keys, creating or altering it as
// needed through the engine's [SchemaChecker]. It runs even when the
// automatic check is disabled with [WithSchemaCheck], and is meant to be
// called at startup in that mode:
//
//	if err := be.CheckStructure(ctx, psql.Table[User]()); err != nil { ... }
//
// A table whose psql.Name tag carries check=0 is never modified. On success
// the table is remembered as checked, so later operations skip the automatic
// check; on failure it is retried by the next operation (or call).
func (be *Backend) CheckStructure(ctx context.Context, tv TableView) error {
	if be == nil {
		return ErrNotReady
	}
	if tv == nil {
		return fmt.Errorf("psql: CheckStructure requires a table")
	}
	var typ reflect.Type
	if tt, ok := tv.(interface{ tableType() reflect.Type }); ok {
		typ = tt.tableType()
	}
	if bb, ok := tv.(interface{ bind(*Backend) *boundTable }); ok {
		tv = bb.bind(be)
	}
	return be.runCheck(ctx, typ, tv)
}

// checkTable is the automatic schema check performed before operations.
func (be *Backend) checkTable(ctx context.Context, typ reflect.Type, tv TableView) error {
	if be.noSchemaCheck {
		return nil
	}
	return be.runCheck(ctx, typ, tv)
}

// runCheck runs the dialect's CheckStructure for tv at most once
// successfully per type, serializing concurrent attempts on the same type.
// typ may be nil for views that are not a registered table, in which case the
// result is not remembered.
func (be *Backend) runCheck(ctx context.Context, typ reflect.Type, tv TableView) error {
	sc, ok := be.Engine().dialect().(SchemaChecker)
	if !ok {
		return nil
	}
	if typ == nil {
		return sc.CheckStructure(ctx, be, tv)
	}

	st := be.checkState(typ)
	if st.done.Load() {
		return nil
	}

	st.lk.Lock()
	defer st.lk.Unlock()
	if st.done.Load() {
		return nil
	}
	if err := sc.CheckStructure(ctx, be, tv); err != nil {
		return err
	}
	st.done.Store(true)
	return nil
}

// checkState returns the check tracker for typ, creating it if needed.
func (be *Backend) checkState(typ reflect.Type) *tableCheck {
	be.checkedLk.RLock()
	st := be.checked[typ]
	be.checkedLk.RUnlock()
	if st != nil {
		return st
	}

	be.checkedLk.Lock()
	defer be.checkedLk.Unlock()
	if be.checked == nil {
		be.checked = make(map[reflect.Type]*tableCheck)
	}
	if st = be.checked[typ]; st == nil {
		st = &tableCheck{}
		be.checked[typ] = st
	}
	return st
}
