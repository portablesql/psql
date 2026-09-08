package psql

import (
	"fmt"
	"strings"
)

// renderContext carries the state of a single query rendering: the target
// engine and dialect, the SQL fragments produced so far, the collected
// arguments (when rendering a parameterized query) and the first error
// encountered while rendering.
type renderContext struct {
	e       Engine
	d       Dialect
	req     []string
	args    []any
	useArgs bool
	err     error
}

// newRenderContext returns a renderContext for the given engine.
func newRenderContext(e Engine, useArgs bool) *renderContext {
	return &renderContext{e: e, d: e.dialect(), useArgs: useArgs}
}

// fallbackRenderContext returns a non-parameterized renderContext for the
// unknown engine. It is used when a value is rendered without any context
// (for example through [Escape] or an EscapeValue method). Errors recorded on
// such a context are lost since those APIs only return a string.
func fallbackRenderContext() *renderContext {
	return newRenderContext(EngineUnknown, false)
}

// setErr records err as the rendering error unless one was already recorded.
// It is safe to call on a nil context (the error is then dropped).
func (ctx *renderContext) setErr(err error) {
	if ctx == nil || err == nil || ctx.err != nil {
		return
	}
	ctx.err = err
}

// errorf records a formatted rendering error (see setErr).
func (ctx *renderContext) errorf(format string, args ...any) {
	ctx.setErr(fmt.Errorf(format, args...))
}

// engine returns the engine the context renders for, or EngineUnknown for a nil context.
func (ctx *renderContext) engine() Engine {
	if ctx == nil {
		return EngineUnknown
	}
	return ctx.e
}

func (ctx *renderContext) append(v ...string) {
	ctx.req = append(ctx.req, v...)
}

func (ctx *renderContext) appendCommaValues(vals ...any) error {
	b := &strings.Builder{}

	for n, v := range vals {
		if n != 0 {
			b.WriteByte(',')
		}
		b.WriteString(escapeCtx(ctx, v))
	}

	ctx.append(b.String())
	return nil
}

func (ctx *renderContext) appendCommaValuesSort(vals ...SortValueable) error {
	b := &strings.Builder{}

	for n, v := range vals {
		if n != 0 {
			b.WriteByte(',')
		}
		// Use engine-aware rendering if available (e.g., vector distance operators)
		if vc, ok := v.(sortValueCtxable); ok {
			b.WriteString(vc.sortEscapeValueCtx(ctx))
		} else {
			b.WriteString(v.sortEscapeValue())
		}
	}

	ctx.append(b.String())
	return nil
}

// sortValueCtxable is an optional interface for SortValueable implementations
// that need engine-aware or parameterized rendering (e.g., PostgreSQL vector
// operators, comparisons used as sort keys).
type sortValueCtxable interface {
	sortEscapeValueCtx(ctx *renderContext) string
}

func (ctx *renderContext) appendArg(arg any) string {
	if ctx.useArgs {
		arg = ctx.d.ExportArg(arg)
		ctx.args = append(ctx.args, arg)
		return ctx.d.Placeholder(len(ctx.args))
	}
	return escapeCtx(ctx, arg)
}
