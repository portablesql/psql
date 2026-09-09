# Transactions

Transactions are carried by the context: every psql operation using a
context that holds a transaction runs inside it, including raw
`psql.Q(...)` queries and the automatic schema check.

## Callback Style

```go
err := psql.Tx(ctx, func(ctx context.Context) error {
    if err := psql.Insert(ctx, &User{ID: 1, Login: "Alice"}); err != nil {
        return err // rollback
    }
    if err := psql.Insert(ctx, &Profile{ID: 1, UserID: 1, Bio: "Hello"}); err != nil {
        return err // rollback
    }
    return nil // commit
})
```

`psql.Tx` commits when the callback returns nil and rolls back otherwise
(the callback's error is returned). A panic in the callback also rolls back,
through the deferred `Rollback`.

`psql.Tx` is `psql.TxWithOptions(ctx, nil, cb)`: it may **re-run the
callback** after a retryable failure (see [Retries](#retries) below), so
the callback should not have side effects outside the database.

## Options and Retries

```go
opts := &psql.TxOptions{
    Isolation:  sql.LevelSerializable, // passed to database/sql
    ReadOnly:   false,
    MaxRetries: 5,                     // 0: psql.DefaultTxRetries (3), negative: no retry
    Backoff:    nil,                   // func(attempt int) time.Duration; nil: ~10ms doubling, jittered, capped at 1s
}
err := psql.TxWithOptions(ctx, opts, func(ctx context.Context) error {
    from, err := psql.Get[Account](ctx, map[string]any{"ID": int64(1)}, psql.FetchLock)
    if err != nil {
        return err
    }
    from.Balance -= 10
    return psql.Update(ctx, from)
})
if errors.Is(err, psql.ErrTxRetriesExhausted) {
    // every attempt failed with a retryable error; errors.As still reaches the driver error
}
```

### Retries

A top-level transaction whose callback or commit fails with an error the
backend's dialect reports as *retryable* (`psql.IsRetryable`: serialization
failures and deadlocks, SQLSTATE `40001`/`40P01` on PostgreSQL, "restart
transaction" on CockroachDB, errors 1213/1205 on MySQL and MariaDB,
"database is locked" on SQLite) is rolled back and, after the backoff, the
callback runs again with a fresh transaction, up to `MaxRetries` times.
Everything the callback did inside the transaction is discarded by the
rollback; everything else (emails, messages, in-memory state, results
captured in outer variables) is repeated, so the callback must be
idempotent or reset such state at its start. Once the retries are
exhausted the last error is returned wrapped with
`psql.ErrTxRetriesExhausted`; a cancelled context stops the wait.

Nested transactions (savepoints) never retry: the error propagates so the
outer, top-level transaction can retry as a whole. `Isolation` and
`ReadOnly` are ignored for nested transactions too.

## Named Locks

`psql.NamedLock` and `psql.WithNamedLock` take a cooperative, server-wide
lock on a name (`GET_LOCK` on MySQL/MariaDB, `pg_advisory_xact_lock` on
PostgreSQL) to serialize jobs or migrations across processes:

```go
err := psql.WithNamedLock(ctx, "migrate", 30*time.Second, func(ctx context.Context) error {
    return runMigrations(ctx) // ctx carries the transaction holding the lock
})
if errors.Is(err, psql.ErrLockTimeout) {
    // someone else held the lock for more than 30s
}

release, err := psql.NamedLock(ctx, "nightly-report", 0) // 0: wait forever, negative: do not wait
if err != nil {
    return err // psql.ErrNotSupported on SQLite and CockroachDB
}
defer release() // exactly once
```

When `ctx` already carries a transaction the lock is taken inside it (and,
on PostgreSQL, released with it); otherwise `NamedLock` pins a connection
until `release`, and `WithNamedLock` opens a transaction around `fn`.
`be.Supports(psql.FeatureAdvisoryLocks)` tells whether the product has
named locks. See [Advanced features](advanced.md#named-locks).

## Manual Style

```go
tx, err := psql.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
if err != nil {
    return err
}
defer tx.Rollback() // no-op after a successful Commit

txCtx := psql.ContextTx(ctx, tx)
if err := psql.Insert(txCtx, &User{ID: 1, Login: "Alice"}); err != nil {
    return err
}
return tx.Commit()
```

`psql.BeginTx` returns a `*psql.TxProxy`, which embeds the underlying
`*sql.Tx`. `Commit` and `Rollback` may each be called once: any later call
returns `psql.ErrTxAlreadyProcessed`, which is what makes the deferred
`Rollback` harmless.

## Nested Transactions (Savepoints)

Starting a transaction inside a transactional context creates a savepoint
on the same connection rather than a new transaction. This works with both
styles and on every engine:

```go
err := psql.Tx(ctx, func(ctx context.Context) error {
    psql.Insert(ctx, &User{ID: 1, Login: "Alice"})

    err := psql.Tx(ctx, func(ctx context.Context) error {
        psql.Insert(ctx, &User{ID: 2, Login: "Bob"})
        return errors.New("oops") // ROLLBACK TO SAVEPOINT: Bob is discarded
    })
    _ = err // the outer transaction continues

    psql.Insert(ctx, &User{ID: 3, Login: "Charlie"})
    return nil // COMMIT: Alice and Charlie are saved
})
```

- The nested `BeginTx` issues `SAVEPOINT Ln`; its `Commit` issues `RELEASE
  SAVEPOINT Ln` and its `Rollback` issues `ROLLBACK TO SAVEPOINT Ln`. The
  `*sql.TxOptions` (or `psql.TxOptions`) of a nested transaction are
  ignored, and a nested `Tx` / `TxWithOptions` is never retried.
- Nested transactions must be finished innermost first. Committing or
  rolling back an outer `TxProxy` while an inner one is still open returns
  an error ("nested transaction(s) still open") and leaves the transaction
  untouched, so the outer proxy stays usable once the inner one is finished.
- Depth is tracked per transaction, not per goroutine: do not use one
  transaction from several goroutines.

## Running Queries Outside a Transaction

Since routing is based on the context, a query that must persist regardless
of the transaction's outcome (an audit log, an error record) only needs a
context without the transaction. Keep the original context, or derive one
with `psql.EscapeTx`, which returns the context that was active just below
the innermost transaction (backend and other values preserved) and `false`
when no transaction is present:

```go
func logEvent(ctx context.Context, event string) {
    outerCtx, ok := psql.EscapeTx(ctx)
    if !ok {
        outerCtx = ctx // no transaction active
    }
    _ = psql.Insert(outerCtx, &AuditLog{Event: event})
}

err := psql.Tx(ctx, func(txCtx context.Context) error {
    if err := psql.Insert(txCtx, &Order{ID: 1, Status: "pending"}); err != nil {
        logEvent(txCtx, "order_insert_failed") // committed immediately, survives the rollback
        return err
    }
    return nil
})
```

The escaped query runs on another connection from the pool while the
transaction holds its own. On a SQLite in-memory database the pool has a
single connection, so a query outside the transaction while it is open
blocks forever waiting for that connection; file-based SQLite databases
have a small pool and are fine.

## Safe Deletion

`psql.DeleteOne` runs the delete in its own (possibly nested) transaction
and commits only if exactly one row was affected; otherwise it rolls back
and returns an error wrapping `psql.ErrDeleteBadAssert`:

```go
err := psql.DeleteOne[User](ctx, map[string]any{"ID": uint64(1)})
```
