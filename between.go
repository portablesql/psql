package psql

import "strings"

type betweenComp struct {
	a, start, end any
}

// Between is a BETWEEN SQL operation.
// The BETWEEN operator is inclusive: begin and end values are included.
func Between(a, start, end any) EscapeValueable {
	return &betweenComp{a, start, end}
}

func (c *betweenComp) EscapeValue() string {
	return c.escapeValueCtx(nil)
}

func (c *betweenComp) escapeValueCtx(ctx *renderContext) string {
	// A BETWEEN start AND end
	b := &strings.Builder{}
	b.WriteString(escapeCtx(ctx, c.a))
	b.WriteString(" BETWEEN ")
	b.WriteString(escapeCtx(ctx, c.start))
	b.WriteString(" AND ")
	b.WriteString(escapeCtx(ctx, c.end))
	return b.String()
}

func (c *betweenComp) sortEscapeValue() string {
	return c.EscapeValue()
}

func (c *betweenComp) sortEscapeValueCtx(ctx *renderContext) string {
	return c.escapeValueCtx(ctx)
}
