package psql

import "strings"

// Comparison represents a binary comparison expression (e.g., A = B, A > B).
// Use the helper constructors [Equal], [Gt], [Gte], [Lt], [Lte] instead of
// creating Comparison values directly.
//
// Comparing with nil renders IS NULL (for "=") or IS NOT NULL (for "!=" / "<>").
type Comparison struct {
	A, B any
	Op   string // one of "=", "<", ">", etc...
}

// Equal creates an equality comparison (A = B). Use with [F] for field references:
//
//	psql.Equal(psql.F("status"), "active")
func Equal(a, b any) EscapeValueable {
	return &Comparison{a, b, "="}
}

// Gt creates a greater-than comparison (A > B).
func Gt(a, b any) EscapeValueable {
	return &Comparison{a, b, ">"}
}

// Gte creates a greater-than-or-equal comparison (A >= B).
func Gte(a, b any) EscapeValueable {
	return &Comparison{a, b, ">="}
}

// Lt creates a less-than comparison (A < B).
func Lt(a, b any) EscapeValueable {
	return &Comparison{a, b, "<"}
}

// Lte creates a less-than-or-equal comparison (A <= B).
func Lte(a, b any) EscapeValueable {
	return &Comparison{a, b, "<="}
}

// EscapeValue renders the comparison as non-parameterized SQL: A Op B.
func (c *Comparison) EscapeValue() string {
	return c.escapeValueCtx(nil)
}

func (c *Comparison) escapeValueCtx(ctx *renderContext) string {
	op := strings.TrimSpace(c.Op)
	if op == "" {
		ctx.errorf("psql: comparison without operator")
		return "FALSE"
	}
	return renderComparison(ctx, escapeCtx(ctx, c.A), op, c.B)
}

func (c *Comparison) sortEscapeValue() string {
	return c.EscapeValue()
}

func (c *Comparison) sortEscapeValueCtx(ctx *renderContext) string {
	return c.escapeValueCtx(ctx)
}

// opStr returns the operator to use, negated if not is set. The second
// return value is false when the operator is empty or cannot be negated.
func (c *Comparison) opStr(not bool) (string, bool) {
	op := strings.TrimSpace(c.Op)
	if op == "" {
		return "", false
	}
	if !not {
		// as is
		return op, true
	}
	// NOT
	switch op {
	case "=":
		return "!=", true
	case "!=", "<>":
		return "=", true
	case "<":
		return ">=", true
	case "<=":
		return ">", true
	case ">":
		return "<=", true
	case ">=":
		return "<", true
	default:
		return "", false
	}
}
