package psql

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
)

// TxProxy wraps a *sql.Tx with support for nested transactions via SQL savepoints.
// Create one with [BeginTx] or [Tx]. Calling [TxProxy.BeginTx] on an existing
// TxProxy creates a savepoint instead of a new transaction. Commit releases the
// savepoint (or commits the real transaction at depth 0), and Rollback rolls back
// to the savepoint (or the full transaction at depth 0).
//
// Nested transactions must be finished (committed or rolled back) innermost
// first; finishing an outer TxProxy while an inner one is still open is an
// error. Commit and Rollback may be called at most once per TxProxy; further
// calls return [ErrTxAlreadyProcessed], which makes "defer tx.Rollback()"
// after a successful Commit harmless.
type TxProxy struct {
	*sql.Tx
	ctrl  *txController
	depth int
	ctx   context.Context // context given to BeginTx, used for savepoint statements
	done  atomic.Bool
}

type txController struct {
	depth int
	lk    sync.Mutex
	tx    *sql.Tx
}

// BeginTx starts a nested transaction as a SQL savepoint on the same
// underlying transaction. opts is ignored: the isolation level and read-only
// flag of the enclosing transaction apply.
func (t *TxProxy) BeginTx(ctx context.Context, opts *sql.TxOptions) (*TxProxy, error) {
	return t.ctrl.beginSubTx(ctx, opts)
}

func newTxCtrl(ctx context.Context, tx *sql.Tx, err error) (*TxProxy, error) {
	if err != nil {
		return nil, err
	}

	ctrl := &txController{
		tx: tx,
	}
	res := &TxProxy{
		Tx:   tx,
		ctrl: ctrl,
		ctx:  ctx,
	}

	return res, nil
}

func (c *txController) beginSubTx(ctx context.Context, _ *sql.TxOptions) (*TxProxy, error) {
	c.lk.Lock()
	defer c.lk.Unlock()

	depth := c.depth + 1

	// create checkpoint
	_, err := c.tx.ExecContext(ctx, fmt.Sprintf("SAVEPOINT L%d", depth))
	if err != nil {
		return nil, err
	}
	c.depth = depth

	return &TxProxy{
		Tx:    c.tx,
		ctrl:  c,
		depth: depth,
		ctx:   ctx,
	}, nil
}

// Commit commits the transaction, or releases the savepoint of a nested
// transaction. Returns [ErrTxAlreadyProcessed] if Commit or Rollback was
// already called on this TxProxy. If a nested transaction started from this
// one is still open, Commit fails without touching the transaction; finish
// the nested one and call Commit again.
func (tx *TxProxy) Commit() error {
	return tx.ctrl.commit(tx)
}

func (c *txController) commit(tx *TxProxy) error {
	c.lk.Lock()
	defer c.lk.Unlock()

	if tx.done.Load() {
		return ErrTxAlreadyProcessed
	}
	if err := c.checkDepth("commit", tx.depth); err != nil {
		return err
	}
	tx.done.Store(true)
	ctx, depth := tx.ctx, tx.depth

	if depth > 0 {
		_, err := c.tx.ExecContext(ctx, fmt.Sprintf("RELEASE SAVEPOINT L%d", depth))
		c.depth = depth - 1
		return err
	}

	// actually commit
	return c.tx.Commit()
}

// Rollback rolls back the transaction, or rolls back to the savepoint of a
// nested transaction. Returns [ErrTxAlreadyProcessed] if Commit or Rollback
// was already called on this TxProxy. Like Commit, it fails without touching
// the transaction while a nested transaction started from this one is open.
func (tx *TxProxy) Rollback() error {
	return tx.ctrl.rollback(tx)
}

func (c *txController) rollback(tx *TxProxy) error {
	c.lk.Lock()
	defer c.lk.Unlock()

	if tx.done.Load() {
		return ErrTxAlreadyProcessed
	}
	if err := c.checkDepth("rollback", tx.depth); err != nil {
		return err
	}
	tx.done.Store(true)
	ctx, depth := tx.ctx, tx.depth

	if depth > 0 {
		_, err := c.tx.ExecContext(ctx, fmt.Sprintf("ROLLBACK TO SAVEPOINT L%d", depth))
		c.depth = depth - 1
		return err
	}

	// full rollback
	return c.tx.Rollback()
}

// checkDepth verifies that the transaction at depth is the innermost open one.
func (c *txController) checkDepth(op string, depth int) error {
	if c.depth == depth {
		return nil
	}
	if depth == 0 {
		return fmt.Errorf("cannot %s transaction: %d nested transaction(s) still open (commit or roll back the inner TxProxy first)", op, c.depth)
	}
	return fmt.Errorf("cannot %s nested transaction at depth %d: innermost open transaction is at depth %d", op, depth, c.depth)
}
