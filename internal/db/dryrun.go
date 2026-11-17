package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync/atomic"
)

// DryRun runs fn against a view of the database that sees its own writes and
// then throws every one of them away.
//
// It takes the one writer connection, begins an IMMEDIATE transaction on it,
// and builds a *DB whose reader and writer pools both resolve to that single
// connection. WithTx on the view is a SAVEPOINT that releases (never commits)
// on success, and Read() sees the uncommitted rows, so code that writes and
// then reads back (the common shape of an op) behaves as it would for real.
// When fn returns, the outer transaction is rolled back, always.
//
// changes is the number of rows fn inserted, updated or deleted, from SQLite's
// own counter. Nothing is committed, so no other connection ever sees any of
// it, and the real database is byte-for-byte what it was.
//
// fn must use only the view it is given: using the real *DB from inside fn
// waits for the writer this call holds and fails with ErrWriterBusy.
func (d *DB) DryRun(ctx context.Context, fn func(view *DB) error) (changes int64, err error) {
	cctx, cancel := context.WithTimeout(ctx, acquireTimeout)
	conn, cerr := d.w.Conn(cctx)
	cancel()
	if cerr != nil {
		if errors.Is(cerr, context.DeadlineExceeded) {
			return 0, ErrWriterBusy
		}
		return 0, cerr
	}
	defer conn.Close()
	rerr := conn.Raw(func(dc any) error {
		raw, ok := dc.(rawConn)
		if !ok {
			return fmt.Errorf("db: dry run: driver connection %T is not usable", dc)
		}
		if _, err := raw.ExecContext(ctx, "BEGIN IMMEDIATE", nil); err != nil {
			return err
		}
		defer func() {
			// A rollback can only fail if the connection is already gone.
			_, _ = raw.ExecContext(context.WithoutCancel(ctx), "ROLLBACK", nil)
		}()
		before, err := totalChanges(ctx, raw)
		if err != nil {
			return err
		}
		pool := sql.OpenDB(&viewConnector{raw: raw})
		defer pool.Close()
		view := &DB{Path: d.Path, w: pool, r: pool}
		err = fn(view)
		if after, cerr := totalChanges(ctx, raw); cerr == nil {
			changes = after - before
		}
		return err
	})
	return changes, rerr
}

// rawConn is what the SQLite driver's connection offers; every method used here
// is part of database/sql/driver.
type rawConn interface {
	driver.Conn
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
}

func totalChanges(ctx context.Context, raw rawConn) (int64, error) {
	rows, err := raw.QueryContext(ctx, "SELECT total_changes()", nil)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	dest := make([]driver.Value, 1)
	if err := rows.Next(dest); err != nil {
		return 0, err
	}
	n, _ := dest[0].(int64)
	return n, nil
}

// viewConnector hands database/sql wrappers around the one connection. Several
// wrappers may exist at once (an op may query while a savepoint is open); they
// are used from one goroutine at a time, which SQLite permits on a single
// connection.
type viewConnector struct {
	raw rawConn
	seq atomic.Int64
}

func (c *viewConnector) Connect(context.Context) (driver.Conn, error) {
	return &viewConn{rawConn: c.raw, c: c}, nil
}
func (c *viewConnector) Driver() driver.Driver { return viewDriver{} }

type viewDriver struct{}

func (viewDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("db: the dry-run view cannot be opened by name")
}

// viewConn is a driver.Conn over the shared connection. Close does nothing
// (the real connection belongs to DryRun) and a "transaction" is a savepoint.
type viewConn struct {
	rawConn
	c *viewConnector
}

func (v *viewConn) Close() error                       { return nil }
func (v *viewConn) ResetSession(context.Context) error { return nil }
func (v *viewConn) IsValid() bool                      { return true }
func (v *viewConn) Begin() (driver.Tx, error) {
	return v.BeginTx(context.Background(), driver.TxOptions{})
}

func (v *viewConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	name := fmt.Sprintf("dry_%d", v.c.seq.Add(1))
	if _, err := v.rawConn.ExecContext(ctx, "SAVEPOINT "+name, nil); err != nil {
		return nil, err
	}
	return &viewTx{raw: v.rawConn, name: name}, nil
}

type viewTx struct {
	raw  rawConn
	name string
}

// Commit keeps the savepoint's changes inside the outer transaction, which
// DryRun rolls back.
func (t *viewTx) Commit() error {
	_, err := t.raw.ExecContext(context.Background(), "RELEASE "+t.name, nil)
	return err
}

func (t *viewTx) Rollback() error {
	ctx := context.Background()
	if _, err := t.raw.ExecContext(ctx, "ROLLBACK TO "+t.name, nil); err != nil {
		return err
	}
	_, err := t.raw.ExecContext(ctx, "RELEASE "+t.name, nil)
	return err
}
