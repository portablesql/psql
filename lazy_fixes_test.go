package psql_test

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"testing"

	"github.com/portablesql/psql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lfUser struct {
	psql.Name `sql:"lf_user"`
	ID        int64  `sql:",key=PRIMARY"`
	Login     string `sql:",type=VARCHAR,size=64"`
}

func TestLazyFixesNoBackendReturnsErrNotReady(t *testing.T) {
	saved := psql.DefaultBackend
	psql.DefaultBackend = nil
	defer func() { psql.DefaultBackend = saved }()

	f := psql.Lazy[lfUser]("ID", "1")
	//lint:ignore SA1012 exercising nil-context handling
	_, err := f.Resolve(nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, psql.ErrNotReady), "got %v", err)

	// json.Marshal must report the error instead of panicking.
	_, err = json.Marshal(struct {
		U *psql.Future[lfUser] `json:"u"`
	}{U: f})
	require.Error(t, err)
	assert.True(t, errors.Is(err, psql.ErrNotReady), "got %v", err)

	_, err = f.MarshalContextJSON(context.Background())
	assert.True(t, errors.Is(err, psql.ErrNotReady), "got %v", err)

	// A future created with LazyCtx keeps its context but that context has no backend either.
	//lint:ignore SA1012 exercising nil-context handling
	_, err = psql.LazyCtx[lfUser](context.Background(), "ID", "1").Resolve(nil)
	assert.True(t, errors.Is(err, psql.ErrNotReady), "got %v", err)
}

func TestLazyFixesDedup(t *testing.T) {
	// Plain Lazy: same column and value share one future.
	a := psql.Lazy[lfUser]("Login", "dedup")
	b := psql.Lazy[lfUser]("Login", "dedup")
	assert.Same(t, a, b)
	assert.NotSame(t, a, psql.Lazy[lfUser]("Login", "other"))

	// Same batch: shared; different batch or no batch: distinct.
	ctx := psql.WithLazyBatch(context.Background())
	assert.True(t, psql.LazyBatchInContext(ctx))
	assert.False(t, psql.LazyBatchInContext(context.Background()))
	c := psql.LazyCtx[lfUser](ctx, "Login", "dedup")
	d := psql.LazyCtx[lfUser](ctx, "Login", "dedup")
	assert.Same(t, c, d)
	assert.NotSame(t, a, c, "batch futures must not join the global registry")
	assert.NotSame(t, c, psql.LazyCtx[lfUser](psql.WithLazyBatch(context.Background()), "Login", "dedup"))
	e := psql.LazyCtx[lfUser](context.Background(), "Login", "dedup")
	assert.NotSame(t, e, psql.LazyCtx[lfUser](context.Background(), "Login", "dedup"))

	// Different backends inside one batch get different futures.
	be1 := psql.NewBackend(psql.EngineSQLite, nil)
	be2 := psql.NewBackend(psql.EngineSQLite, nil)
	f1 := psql.LazyCtx[lfUser](be1.Plug(ctx), "Login", "dedup")
	f2 := psql.LazyCtx[lfUser](be2.Plug(ctx), "Login", "dedup")
	assert.NotSame(t, f1, f2)
	assert.Same(t, f1, psql.LazyCtx[lfUser](be1.Plug(ctx), "Login", "dedup"))
}

//go:noinline
func makeDroppedFutures(n int) {
	for i := 0; i < n; i++ {
		_ = psql.Lazy[lfUser]("ID", "gc-"+string(rune('a'+i)))
	}
}

func TestLazyFixesWeakRegistryCleanup(t *testing.T) {
	// Drain anything left by other tests.
	runtime.GC()
	psql.LazyPending[lfUser]()

	keep := psql.Lazy[lfUser]("ID", "kept")
	makeDroppedFutures(20)
	assert.GreaterOrEqual(t, psql.LazyPending[lfUser](), 1)

	runtime.GC()
	runtime.GC()
	assert.Equal(t, 1, psql.LazyPending[lfUser](), "dropped futures must be collected")
	runtime.KeepAlive(keep)
}
