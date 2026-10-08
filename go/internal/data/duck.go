package data

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

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
func (d *dataset) withConn(ctx context.Context, fn func(c *duckdb.Conn) error) error {
	conn, err := d.db.Conn(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	defer conn.Close()
	err = conn.Raw(func(dc any) error {
		c, ok := dc.(*duckdb.Conn)
		if !ok {
			return errors.New("not a DuckDB connection")
		}
		return fn(c)
	})
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// checkSelect prepares (binds) query without running it: an error if it
// doesn't bind or isn't exactly one SELECT. duckdb-go's Prepare refuses more
// than one statement (unlike its QueryContext, which runs all but the last
// and returns the last one's rows).
func checkSelect(c *duckdb.Conn, query string) error {
	st, err := c.Prepare(query)
	if err != nil {
		if strings.Contains(err.Error(), "multiple statements") || strings.Contains(err.Error(), "PrepareContext") {
			return errNotOneSelect
		}
		return duckError(err)
	}
	defer st.Close()
	ds, ok := st.(*duckdb.Stmt)
	if !ok {
		return errors.New("not a DuckDB statement")
	}
	typ, err := ds.StatementType()
	if err != nil {
		return duckError(err)
	}
	if typ != duckdb.STATEMENT_TYPE_SELECT {
		return errNotOneSelect
	}
	return nil
}

// query runs query (after checkSelect) and calls fn with each record batch of its result.
func (d *dataset) query(ctx context.Context, query string, fn func(rec arrow.RecordBatch) error) error {
	return d.withConn(ctx, func(c *duckdb.Conn) error {
		if err := checkSelect(c, query); err != nil {
			return err
		}
		ar, err := duckdb.NewArrowFromConn(c)
		if err != nil {
			return err
		}
		rr, err := ar.QueryContext(ctx, query)
		if err != nil {
			return duckError(err)
		}
		defer rr.Release()
		for rr.Next() {
			if err := fn(rr.RecordBatch()); err != nil {
				return err
			}
		}
		return duckError(rr.Err())
	})
}

// CheckWhere refuses a filter that isn't one expression (whereSQL's rules),
// then has DuckDB bind it in a query over the file, without running it, so a
// filter naming a column that isn't there, or with a syntax error, gets
// DuckDB's message.
func (d *dataset) CheckWhere(where string) error {
	if strings.TrimSpace(where) == "" {
		return nil
	}
	w, err := whereSQL(where)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := d.waitBound(ctx); err != nil {
		return err
	}
	return d.withConn(ctx, func(c *duckdb.Conn) error {
		return checkSelect(c, "SELECT count(*) FROM "+d.src+" WHERE "+w)
	})
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

// fetchFiltered reads rows [start, start+n) of the filtered view in DuckDB.
// Like pqx, it relies on DuckDB keeping the file's order for a filtered scan
// (preserve_insertion_order is on by default), so there is no ORDER BY.
func (d *dataset) fetchFiltered(ctx context.Context, where string, start int64, n int, cols []string, idx []int) (Window, error) {
	w, err := whereSQL(where)
	if err != nil {
		return Window{}, err
	}
	if err := d.waitBound(ctx); err != nil {
		return Window{}, err
	}
	if n == 0 {
		return emptyWindow(start, cols), nil
	}
	sel, err := d.selectList(idx)
	if err != nil {
		return Window{}, err
	}
	q := "SELECT " + sel + " FROM " + d.src + " WHERE " + w +
		" LIMIT " + strconv.Itoa(n) + " OFFSET " + strconv.FormatInt(start, 10)
	return d.windowFromQuery(ctx, q, start, cols)
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
	win := Window{Start: start, FileRows: []int64{}, Cols: make(map[string][]string, len(cols))}
	text := make([][]string, len(cols))
	err := d.query(ctx, q, func(rec arrow.RecordBatch) error {
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
			text[k] = append(text[k], formatColumn(rec.Column(k+1), 0, m)...)
		}
		return nil
	})
	if err != nil {
		return Window{}, err
	}
	win.Len = len(win.FileRows)
	for k, c := range cols {
		if text[k] == nil {
			text[k] = []string{}
		}
		win.Cols[c] = text[k]
	}
	return win, nil
}
