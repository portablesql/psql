package psql

import (
	"strings"
)

// VectorDistanceOp represents the type of vector distance operation.
type VectorDistanceOp int

const (
	// VectorL2 is the L2 (Euclidean) distance operator.
	// PostgreSQL/CockroachDB: <->   Display fallback: vec_l2_distance()
	VectorL2 VectorDistanceOp = iota
	// VectorCosine is the cosine distance operator.
	// PostgreSQL/CockroachDB: <=>   Display fallback: vec_cosine_distance()
	VectorCosine
	// VectorInnerProduct is the negative inner product operator.
	// PostgreSQL/CockroachDB: <#>   Display fallback: vec_inner_product()
	VectorInnerProduct
)

// VectorDistance represents a vector distance calculation between a field and a vector.
// It implements [EscapeValueable] and [SortValueable] so it can be used in WHERE and ORDER BY.
//
// Usage:
//
//	// In ORDER BY (nearest neighbor search):
//	psql.B().Select().From("items").OrderBy(psql.VecL2Distance(psql.F("Embedding"), queryVec))
//
//	// In WHERE (filter by distance threshold):
//	psql.B().Select().From("items").Where(
//	    psql.Lt(psql.VecCosineDistance(psql.F("Embedding"), queryVec), 0.5),
//	)
type VectorDistance struct {
	Field any              // typically a fieldName via psql.F()
	Vec   Vector           // the query vector
	Op    VectorDistanceOp // distance type
}

// VecL2Distance creates an L2 (Euclidean) distance expression.
// PostgreSQL/CockroachDB renders as: field <-> '[1,2,3]'
func VecL2Distance(field any, vec Vector) *VectorDistance {
	return &VectorDistance{Field: field, Vec: vec, Op: VectorL2}
}

// VecCosineDistance creates a cosine distance expression.
// PostgreSQL/CockroachDB renders as: field <=> '[1,2,3]'
func VecCosineDistance(field any, vec Vector) *VectorDistance {
	return &VectorDistance{Field: field, Vec: vec, Op: VectorCosine}
}

// VecInnerProduct creates a negative inner product distance expression.
// PostgreSQL/CockroachDB renders as: field <#> '[1,2,3]'
func VecInnerProduct(field any, vec Vector) *VectorDistance {
	return &VectorDistance{Field: field, Vec: vec, Op: VectorInnerProduct}
}

// String returns a display representation (not engine-specific).
func (d *VectorDistance) String() string {
	return d.EscapeValue()
}

// EscapeValue renders the distance expression without engine context, using a
// vec_*_distance(field, vector) function call syntax. This is only a display
// form: rendering a query through [QueryBuilder.Render] on an engine without
// vector support returns an error instead.
func (d *VectorDistance) EscapeValue() string {
	return d.renderFunc(nil)
}

// escapeValueCtx renders the distance expression with engine-specific syntax.
// Rendering for an engine whose dialect does not implement [VectorRenderer]
// records an error on the context (the query fails to render).
func (d *VectorDistance) escapeValueCtx(ctx *renderContext) string {
	if ctx != nil {
		if vr, ok := ctx.d.(VectorRenderer); ok {
			fieldExpr := escapeCtx(ctx, d.Field)
			vecExpr := escapeCtx(ctx, d.Vec.String())
			return vr.VectorDistanceExpr(fieldExpr, vecExpr, d.Op)
		}
		ctx.errorf("psql: vector distance is not supported on %s", ctx.e)
	}
	return d.renderFunc(ctx)
}

// sortEscapeValue implements SortValueable for use in ORDER BY.
func (d *VectorDistance) sortEscapeValue() string {
	return d.EscapeValue()
}

// sortEscapeValueCtx implements sortValueCtxable for engine-aware ORDER BY rendering.
func (d *VectorDistance) sortEscapeValueCtx(ctx *renderContext) string {
	return d.escapeValueCtx(ctx)
}

// renderFunc renders using function call syntax (display fallback when no engine context is available).
func (d *VectorDistance) renderFunc(ctx *renderContext) string {
	b := &strings.Builder{}

	var funcName string
	switch d.Op {
	case VectorL2:
		funcName = "vec_l2_distance"
	case VectorCosine:
		funcName = "vec_cosine_distance"
	case VectorInnerProduct:
		funcName = "vec_inner_product"
	default:
		funcName = "vec_l2_distance"
	}

	b.WriteString(funcName)
	b.WriteByte('(')
	if ctx != nil {
		b.WriteString(escapeCtx(ctx, d.Field))
		b.WriteString(", ")
		b.WriteString(escapeCtx(ctx, d.Vec.String()))
	} else {
		b.WriteString(Escape(d.Field))
		b.WriteString(", ")
		b.WriteString(Escape(d.Vec.String()))
	}
	b.WriteByte(')')

	return b.String()
}

// VecOrderBy is a convenience that returns a [SortValueable] for ordering by vector distance.
// The direction defaults to ASC (nearest first).
//
// Usage:
//
//	psql.B().Select().From("items").OrderBy(psql.VecOrderBy(psql.F("Embedding"), queryVec, psql.VectorL2))
func VecOrderBy(field any, vec Vector, op VectorDistanceOp) SortValueable {
	return &ordVecDistance{
		dist: &VectorDistance{Field: field, Vec: vec, Op: op},
		ord:  "ASC",
	}
}

type ordVecDistance struct {
	dist *VectorDistance
	ord  string
}

func (o *ordVecDistance) sortEscapeValue() string {
	return o.dist.EscapeValue() + " " + o.ord
}

func (o *ordVecDistance) sortEscapeValueCtx(ctx *renderContext) string {
	return o.dist.escapeValueCtx(ctx) + " " + o.ord
}

// VectorComparison represents a vector equality or inequality comparison.
// It implements [EscapeValueable] for use in WHERE clauses.
//
// Use [VecEqual] and [VecNotEqual] instead of constructing directly.
type VectorComparison struct {
	Field any    // typically a fieldName via psql.F()
	Vec   Vector // the vector to compare against
	Op    string // "=" or "<>"
}

// VecEqual creates a vector equality comparison (field = vector).
//
//	psql.B().Select().From("items").Where(psql.VecEqual(psql.F("Embedding"), targetVec))
func VecEqual(field any, vec Vector) *VectorComparison {
	return &VectorComparison{Field: field, Vec: vec, Op: "="}
}

// VecNotEqual creates a vector inequality comparison (field <> vector).
//
//	psql.B().Select().From("items").Where(psql.VecNotEqual(psql.F("Embedding"), targetVec))
func VecNotEqual(field any, vec Vector) *VectorComparison {
	return &VectorComparison{Field: field, Vec: vec, Op: "<>"}
}

// EscapeValue renders the comparison without engine context.
func (c *VectorComparison) EscapeValue() string {
	return c.escapeValueCtx(nil)
}

// escapeValueCtx renders the comparison with engine context.
func (c *VectorComparison) escapeValueCtx(ctx *renderContext) string {
	b := &strings.Builder{}
	if ctx != nil {
		b.WriteString(escapeCtx(ctx, c.Field))
	} else {
		b.WriteString(Escape(c.Field))
	}
	b.WriteString(" ")
	b.WriteString(c.Op)
	b.WriteString(" ")
	if ctx != nil {
		b.WriteString(escapeCtx(ctx, c.Vec.String()))
	} else {
		b.WriteString(Escape(c.Vec.String()))
	}
	return b.String()
}

// String returns a display representation.
func (c *VectorComparison) String() string {
	return c.EscapeValue()
}
