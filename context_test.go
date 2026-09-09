package psql_test

import (
	"context"
	"testing"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
)

func TestGetBackendDefault(t *testing.T) {
	// With background context, returns DefaultBackend (may be nil if no Init called)
	be := psql.GetBackend(context.Background())
	// DefaultBackend is nil in test environment without a database
	_ = be
}

func TestGetBackendNilCtx(t *testing.T) {
	//lint:ignore SA1012 exercising nil-context handling on purpose
	be := psql.GetBackend(nil)
	// DefaultBackend is nil in test environment without a database
	_ = be
}

func TestContextBackend(t *testing.T) {
	be := &psql.Backend{}
	ctx := psql.ContextBackend(context.Background(), be)
	got := psql.GetBackend(ctx)
	assert.Equal(t, be, got)
}

func TestContextBackendNested(t *testing.T) {
	be1 := &psql.Backend{}
	be2 := &psql.Backend{}
	ctx := psql.ContextBackend(context.Background(), be1)
	ctx2 := psql.ContextBackend(ctx, be2)

	// Should get the innermost backend
	got := psql.GetBackend(ctx2)
	assert.Equal(t, be2, got)
}

func TestBackendEngine(t *testing.T) {
	be := &psql.Backend{}
	assert.Equal(t, psql.EngineUnknown, be.Engine())

	var nilBe *psql.Backend
	assert.Equal(t, psql.EngineUnknown, nilBe.Engine())
}

func TestBackendNamer(t *testing.T) {
	be := &psql.Backend{}
	n := be.Namer()
	assert.NotNil(t, n)

	var nilBe *psql.Backend
	n = nilBe.Namer()
	assert.NotNil(t, n)
}

func TestBackendPlug(t *testing.T) {
	be := &psql.Backend{}
	ctx := be.Plug(context.Background())
	got := psql.GetBackend(ctx)
	assert.Equal(t, be, got)
}
