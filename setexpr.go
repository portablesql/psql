package psql

// Increment represents a SET expression that increments a field by a value.
// Used in UPDATE queries: SET "field"="field"+(value)
type Increment struct {
	Value any
}

// Decrement represents a SET expression that decrements a field by a value.
// Used in UPDATE queries: SET "field"="field"-(value)
type Decrement struct {
	Value any
}

// SetRaw represents a SET expression using raw SQL.
// Used in UPDATE queries: SET "field"=<raw SQL>
type SetRaw struct {
	SQL string
}

// Incr creates an [Increment] expression for use in UPDATE SET clauses:
//
//	psql.B().Update("counters").Set(map[string]any{"views": psql.Incr(1)})
//	// UPDATE "counters" SET "views"="views"+(1)
func Incr(v any) *Increment {
	return &Increment{Value: v}
}

// Decr creates a [Decrement] expression for use in UPDATE SET clauses:
//
//	psql.B().Update("counters").Set(map[string]any{"stock": psql.Decr(1)})
//	// UPDATE "counters" SET "stock"="stock"-(1)
func Decr(v any) *Decrement {
	return &Decrement{Value: v}
}
