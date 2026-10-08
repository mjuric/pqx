package data

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/duckdb/duckdb-go/v2"
)

// errNotOneSelect is pqx's check_select: every query built around user text
// must be exactly one SELECT.
var errNotOneSelect = errors.New("only a single SELECT query is allowed here")

// dbError is an error from DuckDB, its message cut to what a person needs.
type dbError struct {
	msg string
	err error
}

func (e *dbError) Error() string { return e.msg }
func (e *dbError) Unwrap() error { return e.err }

// duckError shortens a DuckDB error to its first part (DuckDB adds the query
// text, which is pqx's, not the user's), puts it on one line and sanitizes it
// (it can quote names and values from the file).
func duckError(err error) error {
	if err == nil {
		return nil
	}
	var de *dbError
	if errors.As(err, &de) {
		return err
	}
	msg := err.Error()
	if i := strings.Index(msg, "\n\nLINE "); i >= 0 {
		msg = msg[:i]
	}
	msg = strings.Join(strings.Fields(msg), " ")
	return &dbError{msg: Sanitize(msg), err: err}
}

// withConn runs fn on a connection of its own (so that cancelling one call
// interrupts only that call's query).
//
// It runs fn on a goroutine and returns ctx's error as soon as ctx is done,
// even if fn is stuck where DuckDB can't be interrupted (preparing a query
// binds its table functions: read_csv of a FIFO in a filter blocks in the
// OS). fn's goroutine then finishes, and gives back its connection, later;
// fn must not touch anything the caller reads after an error.
// duckdbConn is duckdb-go's connection.
type duckdbConn = duckdb.Conn

func (d *dataset) withConn(ctx context.Context, fn func(c *duckdbConn) error) error {
	done := make(chan error, 1)
	go func() {
		conn, err := d.db.Conn(ctx)
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		done <- conn.Raw(func(dc any) error {
			c, ok := dc.(*duckdb.Conn)
			if !ok {
				return errors.New("not a DuckDB connection")
			}
			return fn(c)
		})
	}()
	select {
	case err := <-done:
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// checkSelect prepares (binds) query without running it: an error if it
// doesn't bind or isn't exactly one SELECT. duckdb-go's Prepare refuses more
// than one statement (unlike its QueryContext, which runs all but the last
// and returns the last one's rows).
func checkSelect(c *duckdbConn, query string) error {
	st, err := prepareSelect(context.Background(), c, query)
	if st != nil {
		st.Close()
	}
	return err
}

// query runs query (after checkSelect) and calls fn with each record batch of its result.
func (d *dataset) query(ctx context.Context, query string, fn func(rec arrow.RecordBatch) error) error {
	return d.queryTyped(ctx, query, func(rec arrow.RecordBatch, _ []cellFunc) error { return fn(rec) })
}

// CheckWhereTimeout bounds how long CheckWhere waits for DuckDB.
var CheckWhereTimeout = 10 * time.Second

// CheckWhere refuses a filter that isn't one expression (whereSQL's rules),
// then has DuckDB bind it in a query over the file, without running it, so a
// filter naming a column that isn't there, or with a syntax error, gets
// DuckDB's message. It gives up after CheckWhereTimeout.
func (d *dataset) CheckWhere(where string) error {
	return d.checkWhere(context.Background(), where)
}

// checkWhere is CheckWhere, also stopped by ctx.
func (d *dataset) checkWhere(ctx context.Context, where string) error {
	if strings.TrimSpace(where) == "" {
		return nil
	}
	w, err := whereSQL(where)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, CheckWhereTimeout)
	defer cancel()
	err = d.waitBound(ctx)
	if err == nil {
		err = d.withConn(ctx, func(c *duckdb.Conn) error {
			st, err := prepareSelect(ctx, c, "SELECT count(*) FROM "+d.src+" WHERE "+w)
			if st != nil {
				st.Close()
			}
			return err
		})
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("DuckDB took over %v to check the filter (does it read from something that blocks?): %w", CheckWhereTimeout, err)
	}
	return err
}

func (d *dataset) countFiltered(ctx context.Context, where string) (int64, error) {
	w, err := whereSQL(where)
	if err != nil {
		return 0, err
	}
	if err := d.waitBound(ctx); err != nil {
		return 0, err
	}
	var n int64 = -1
	err = d.query(ctx, "SELECT count(*) FROM "+d.src+" WHERE "+w, func(rec arrow.RecordBatch) error {
		if rec.NumRows() == 0 {
			return nil
		}
		c, ok := rec.Column(0).(*array.Int64)
		if !ok {
			return fmt.Errorf("count(*) came back as %s", rec.Column(0).DataType())
		}
		n = c.Value(0)
		return nil
	})
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, errors.New("count(*) returned no rows")
	}
	return n, nil
}

// selectList is the file row number and the columns idx (by DuckDB's names).
func (d *dataset) selectList(idx []int) (string, error) {
	parts := make([]string, 0, len(idx)+1)
	if d.hasRowNum {
		parts = append(parts, "file_row_number")
	} else {
		parts = append(parts, "-1::BIGINT") // DuckDB can't number this file's rows
	}
	for _, j := range idx {
		q, err := quoteIdent(d.duckNames[j])
		if err != nil {
			return "", err
		}
		parts = append(parts, q)
	}
	return strings.Join(parts, ", "), nil
}

// fetchPlainDuck reads file rows [start, start+n) in DuckDB, which skips to
// them through its file_row_number filter (pqx's other reader for the plain
// view; kept for comparing the two).
func (d *dataset) fetchPlainDuck(ctx context.Context, start int64, n int, cols []string, idx []int) (Window, error) {
	if err := d.waitBound(ctx); err != nil {
		return Window{}, err
	}
	end := min(start+int64(n), d.numRows)
	if end <= start {
		return emptyWindow(start, cols), nil
	}
	if !d.hasRowNum {
		return Window{}, errors.New("this file has a column named file_row_number")
	}
	sel, err := d.selectList(idx)
	if err != nil {
		return Window{}, err
	}
	q := fmt.Sprintf("SELECT %s FROM %s WHERE file_row_number >= %d AND file_row_number < %d ORDER BY file_row_number",
		sel, d.src, start, end)
	return d.windowFromQuery(ctx, q, start, cols)
}

// windowFromQuery runs q, whose first column is the file row number and the
// others cols in order, into a Window.
func (d *dataset) windowFromQuery(ctx context.Context, q string, start int64, cols []string) (Window, error) {
	win := Window{Start: start, FileRows: []int64{}, Cols: make(map[string][]Value, len(cols))}
	vals := make([][]Value, len(cols))
	err := d.queryTyped(ctx, q, func(rec arrow.RecordBatch, conv []cellFunc) error {
		if int(rec.NumCols()) != len(cols)+1 {
			return fmt.Errorf("DuckDB returned %d columns, not %d", rec.NumCols(), len(cols)+1)
		}
		rn, ok := rec.Column(0).(*array.Int64)
		if !ok {
			return fmt.Errorf("file_row_number came back as %s", rec.Column(0).DataType())
		}
		m := int(rec.NumRows())
		for i := range m {
			win.FileRows = append(win.FileRows, rn.Value(i))
		}
		for k := range cols {
			vals[k] = append(vals[k], valueColumn(rec.Column(k+1), 0, m, conv[k+1])...)
		}
		return nil
	})
	if err != nil {
		return Window{}, err
	}
	win.Len = len(win.FileRows)
	for k, c := range cols {
		if vals[k] == nil {
			vals[k] = []Value{}
		}
		win.Cols[c] = vals[k]
	}
	return win, nil
}
