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
  `*sql.TxOptions` of a nested transaction are ignored.
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
