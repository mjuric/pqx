package data

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/duckdb/duckdb-go/v2"
)

// IsSQLQuery reports whether text is a full query rather than a WHERE
// expression (pqx's is_sql_query): it starts with SELECT, WITH, FROM, PIVOT,
// UNPIVOT, DESCRIBE or SUMMARIZE. (A helper of this package, not of the
// contract: the filter bar uses it to tell a query from a filter.)
func IsSQLQuery(text string) bool { return sqlStart.MatchString(text) }

// tableName is the name a SQL view's query calls the file by.
const tableName = "t"

// viewState is what the views need beyond the prototype's dataset: DuckDB's
// types of the columns, and how each column is read in the plain view.
type viewState struct {
	// DuckDB's type of each column (nil where unknown) and its Arrow type for it
	duckInfos []duckdb.TypeInfo
	duckArrow []arrow.DataType
	// direct converts arrow-go's values of each column to DuckDB's; nil
	// where they don't convert exactly (DuckDB reads that column).
	direct []cellFunc
	// wide marks the columns holding decimals wider than 38 digits, which
	// only arrow-go reads right.
	wide []bool
	// DuckDB reads glob patterns: a file whose name can't be escaped as one
	// is read through a symbolic link in linkDir (see duckPathFor).
	duckPath, linkDir string

	fb fallback
}

// DuckDB's keywords (lower case), for writing type names: the same for
// every dataset.
var (
	kwMu     sync.Mutex
	keywords map[string]bool
)

// bindTypes has DuckDB bind the file: its names for the columns, their
// types (TypeInfo, and the type names as DESCRIBE spells them) and DuckDB's
// Arrow types for them. Preparing the query parses the footer (into DuckDB's
// cache); then DuckDB's Arrow types (a LIMIT 0 query) and its keywords (to
// spell the type names) are asked for at once, on two connections.
func (d *dataset) bindTypes(ctx context.Context) error {
	q := "SELECT * FROM " + readParquet(d.duckPath, false)
	err := d.withConn(ctx, func(c *duckdbConn) error {
		st, err := c.Prepare(q)
		if err != nil {
			return err
		}
		defer st.Close()
		ds := st.(*duckdb.Stmt)
		n, err := ds.ColumnCount()
		if err != nil {
			return err
		}
		d.duckInfos = make([]duckdb.TypeInfo, n)
		d.duckNames = make([]string, n)
		for i := range n {
			if d.duckNames[i], err = ds.ColumnName(i); err != nil {
				return err
			}
			if d.duckInfos[i], err = ds.ColumnTypeInfo(i); err != nil {
				d.duckInfos[i] = nil
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	var kw map[string]bool
	kwDone := make(chan struct{})
	go func() {
		defer close(kwDone)
		kw = d.duckKeywords(ctx)
	}()
	err = d.withConn(ctx, func(c *duckdbConn) error {
		ar, err := duckdb.NewArrowFromConn(c)
		if err != nil {
			return err
		}
		rr, err := ar.QueryContext(ctx, q+" LIMIT 0")
		if err != nil {
			return err
		}
		defer rr.Release()
		for _, f := range rr.Schema().Fields() {
			d.duckArrow = append(d.duckArrow, f.Type)
		}
		return nil
	})
	<-kwDone
	if err != nil {
		return err
	}
	d.duckTypes = make([]string, len(d.duckInfos))
	unknown := false
	for i, ti := range d.duckInfos {
		d.duckTypes[i] = duckTypeName(ti, kw)
		unknown = unknown || strings.Contains(d.duckTypes[i], "UNKNOWN")
	}
	if !unknown {
		return nil
	}
	// a type duckTypeName doesn't know: DuckDB's own names
	var types []string
	err = d.query(ctx, "SELECT column_type FROM (DESCRIBE "+q+")", func(rec arrow.RecordBatch) error {
		for i := range int(rec.NumRows()) {
			s, _ := ValueAt(rec.Column(0), i).(string)
			types = append(types, s)
		}
		return nil
	})
	if err == nil && len(types) == len(d.duckTypes) {
		d.duckTypes = types
	}
	return nil
}

// tableView creates (once) the view t over the file, which SQL views query.
func (d *dataset) tableView(ctx context.Context) error { return d.ensureT(ctx) }

// setupConversions decides, once DuckDB has bound the file, how the plain
// view reads each column: arrow-go converted to DuckDB's values where that's
// exact, else DuckDB. Without DuckDB, arrow-go reads everything as it is.
func (d *dataset) setupConversions(sc *arrow.Schema) {
	n := len(d.cols)
	d.direct = make([]cellFunc, n)
	d.wide = make([]bool, n)
	ok := d.bindErr == nil && len(d.duckArrow) == n && len(d.duckInfos) == n
	for i := range d.cols {
		at := sc.Field(i).Type
		d.wide[i] = hasWideDecimal(at)
		if !ok {
			d.direct[i] = ValueAt
			continue
		}
		int96 := len(d.leaves[i]) == 1 && d.md.Schema.Column(d.leaves[i][0]).PhysicalType() == parquet.Types.Int96
		if f, ok := directCell(at, d.duckArrow[i], d.duckInfos[i], int96); ok {
			d.direct[i] = f
		}
	}
}

// duckKeywords are DuckDB's keywords (lower case), for writing type names.
func (d *dataset) duckKeywords(ctx context.Context) map[string]bool {
	kwMu.Lock()
	defer kwMu.Unlock()
	if keywords != nil {
		return keywords
	}
	kw := map[string]bool{}
	err := d.query(ctx, "SELECT keyword_name FROM duckdb_keywords()", func(rec arrow.RecordBatch) error {
		for i := range int(rec.NumRows()) {
			if s, ok := ValueAt(rec.Column(0), i).(string); ok {
				kw[strings.ToLower(s)] = true
			}
		}
		return nil
	})
	if err != nil {
		return map[string]bool{}
	}
	keywords = kw
	return kw
}

// sqlBodyChecked is a SQL view's query as it goes inside pqx's queries (pqx's
// view.sql.strip().rstrip(";")); it is wrapped as "(<body>\n)", so a
// trailing -- comment can't swallow the parenthesis.
func sqlBodyChecked(v View) (string, error) {
	s := strings.TrimRight(strings.TrimSpace(v.SQL), ";")
	if strings.ContainsRune(s, 0) {
		return "", &filterError{"a query can't hold a NUL character"}
	}
	return s, nil
}

// viewSQL is "SELECT sel FROM <file> [WHERE ...] [ORDER BY ...]" for a
// filtered and/or sorted view of the file. Sorts put NULLs last and ties in
// file order (by file row number, when DuckDB can number the rows).
func (d *dataset) viewSQL(v View, sel string) (string, error) {
	q := "SELECT " + sel + " FROM " + d.src
	if strings.TrimSpace(v.Where) != "" {
		w, err := whereSQL(v.Where)
		if err != nil {
			return "", err
		}
		q += " WHERE " + w
	}
	if len(v.OrderBy) > 0 {
		ob, err := d.orderBy(v.OrderBy)
		if err != nil {
			return "", err
		}
		q += " ORDER BY " + ob
	}
	return q, nil
}

// orderBy is the ORDER BY list of a sort.
func (d *dataset) orderBy(keys []Sort) (string, error) {
	parts := make([]string, 0, len(keys)+1)
	for _, k := range keys {
		q, err := d.qcol(k.Column)
		if err != nil {
			return "", err
		}
		dir := " ASC"
		if k.Desc {
			dir = " DESC"
		}
		parts = append(parts, q+dir+" NULLS LAST")
	}
	if d.hasRowNum {
		parts = append(parts, "file_row_number")
	} else {
		// DuckDB can't number this file's rows: break ties on every column
		// of the file instead, so pages of the sort are consistent (rows
		// alike in every column are interchangeable). pqx has no tiebreak
		// here, and its pages can repeat or skip rows of a tie.
		for _, c := range d.cols {
			if q, err := quoteIdent(c.SQLName); err == nil && !strings.HasPrefix(c.Type, "MAP(") {
				parts = append(parts, q+" ASC NULLS LAST")
			}
		}
	}
	return strings.Join(parts, ", "), nil
}

// qcol is the file's column name quoted for SQL, by DuckDB's name for it.
func (d *dataset) qcol(name string) (string, error) { return d.colRef(View{}, name) }

// Validate checks a view without reading data and returns its columns.
func (d *dataset) Validate(ctx context.Context, v View) ([]Column, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if v.IsSQL() {
		return d.validateSQL(ctx, v)
	}
	if v.Plain() {
		return d.Columns(), nil
	}
	if len(v.OrderBy) == 0 {
		if err := d.checkWhere(ctx, v.Where); err != nil {
			return nil, err
		}
		return d.Columns(), nil
	}
	if strings.TrimSpace(v.Where) != "" {
		if _, err := whereSQL(v.Where); err != nil {
			return nil, err
		}
	}
	q, err := d.viewSQL(v, "*")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, CheckWhereTimeout)
	defer cancel()
	if err := d.waitBound(ctx); err != nil {
		return nil, duckError(err)
	}
	err = d.withConn(ctx, func(c *duckdbConn) error {
		st, err := prepareSelect(ctx, c, q)
		if st != nil {
			st.Close()
		}
		return err
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("DuckDB took over %v to check the sort (does the filter read from something that blocks?): %w", CheckWhereTimeout, err)
	}
	if err != nil {
		return nil, err
	}
	return d.Columns(), nil
}

// validateSQL binds a SQL view's query and returns its result columns.
func (d *dataset) validateSQL(ctx context.Context, v View) ([]Column, error) {
	body, err := sqlBodyChecked(v)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, CheckWhereTimeout)
	defer cancel()
	if err := d.waitBound(ctx); err != nil {
		return nil, duckError(err)
	}
	if err := d.tableView(ctx); err != nil {
		return nil, err
	}
	kw := d.duckKeywords(ctx)
	q := "SELECT * FROM (" + body + "\n) LIMIT 0"
	var cols []Column
	err = d.withConn(ctx, func(c *duckdbConn) error {
		st, err := prepareSelect(ctx, c, q)
		if err != nil {
			return err
		}
		n, err := st.ColumnCount()
		if err != nil {
			st.Close()
			return err
		}
		for i := range n {
			name, _ := st.ColumnName(i)
			ti, err := st.ColumnTypeInfo(i)
			if err != nil {
				ti = nil
			}
			cols = append(cols, Column{Name: name, Type: duckTypeName(ti, kw), Nullable: true, SQLName: name})
		}
		st.Close()
		ar, err := duckdb.NewArrowFromConn(c)
		if err != nil {
			return err
		}
		rr, err := ar.QueryContext(ctx, q)
		if err != nil {
			return duckError(err)
		}
		defer rr.Release()
		fs := rr.Schema().Fields()
		if len(fs) != len(cols) {
			return fmt.Errorf("DuckDB returned %d columns, not %d", len(fs), len(cols))
		}
		for i, f := range fs {
			cols[i].Arrow = f.Type
		}
		return nil
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("DuckDB took over %v to check the query (does it read from something that blocks?): %w", CheckWhereTimeout, err)
	}
	if err != nil {
		return nil, err
	}
	return cols, nil
}

// fetchSQL reads rows [start, start+n) of a SQL view's result, columns cols
// (by the result's names). Its rows have no file row numbers.
func (d *dataset) fetchSQL(ctx context.Context, v View, start int64, n int, cols []string) (Window, error) {
	body, err := sqlBodyChecked(v)
	if err != nil {
		return Window{}, err
	}
	if err := d.waitBound(ctx); err != nil {
		return Window{}, duckError(err)
	}
	if err := d.tableView(ctx); err != nil {
		return Window{}, err
	}
	win := Window{Start: start, Cols: make(map[string][]Value, len(cols))}
	if n == 0 {
		for _, c := range cols {
			win.Cols[c] = []Value{}
		}
		return win, nil
	}
	sel := "NULL"
	if len(cols) > 0 {
		parts := make([]string, len(cols))
		for i, c := range cols {
			q, err := quoteIdent(c)
			if err != nil {
				return Window{}, err
			}
			parts[i] = q
		}
		sel = strings.Join(parts, ", ")
	}
	q := "SELECT " + sel + " FROM (" + body + "\n) LIMIT " + strconv.Itoa(n) + " OFFSET " + strconv.FormatInt(start, 10)
	vals := make([][]Value, len(cols))
	err = d.queryTyped(ctx, q, func(rec arrow.RecordBatch, conv []cellFunc) error {
		m := int(rec.NumRows())
		win.Len += m
		for k := range cols {
			vals[k] = append(vals[k], valueColumn(rec.Column(k), 0, m, conv[k])...)
		}
		return nil
	})
	if err != nil {
		return Window{}, err
	}
	for k, c := range cols {
		if vals[k] == nil {
			vals[k] = []Value{}
		}
		win.Cols[c] = vals[k]
	}
	return win, nil
}

// countSQL counts a SQL view's rows.
func (d *dataset) countSQL(ctx context.Context, v View) (int64, error) {
	body, err := sqlBodyChecked(v)
	if err != nil {
		return 0, err
	}
	if err := d.waitBound(ctx); err != nil {
		return 0, duckError(err)
	}
	if err := d.tableView(ctx); err != nil {
		return 0, err
	}
	return d.queryCount(ctx, "SELECT count(*) FROM ("+body+"\n)")
}

// queryCount runs a query whose result is one BIGINT.
func (d *dataset) queryCount(ctx context.Context, q string) (int64, error) {
	var n int64 = -1
	err := d.query(ctx, q, func(rec arrow.RecordBatch) error {
		if rec.NumRows() == 0 {
			return nil
		}
		switch c := rec.Column(0).(type) {
		case *array.Int64:
			n = c.Value(0)
		default:
			v, ok := ValueAt(c, 0).(int64)
			if !ok {
				return fmt.Errorf("count(*) came back as %s", c.DataType())
			}
			n = v
		}
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

// prepareSelect prepares (binds) query without running it, and returns the
// statement if it is exactly one SELECT (pqx's check_select). duckdb-go's
// Prepare refuses more than one statement without running any. The one
// exception, as in pqx: a PIVOT without an IN list, which DuckDB turns into a
// CREATE TYPE of its own and the SELECT. Such a query is accepted only if
// its text has no ; of its own (outside literals and comments) and has the
// word PIVOT; DuckDB then runs its CREATE TYPE while preparing.
func prepareSelect(ctx context.Context, c *duckdbConn, query string) (*duckdb.Stmt, error) {
	st, err := c.Prepare(query)
	if err != nil {
		if !strings.Contains(err.Error(), "multi-statement") && !strings.Contains(err.Error(), "PrepareContext") {
			return nil, duckError(err)
		}
		if !implicitStatementsOnly(query) {
			return nil, errNotOneSelect
		}
		st, err = c.PrepareContext(ctx, query)
		if err != nil {
			return nil, duckError(err)
		}
	}
	ds, ok := st.(*duckdb.Stmt)
	if !ok {
		st.Close()
		return nil, errors.New("not a DuckDB statement")
	}
	typ, err := ds.StatementType()
	if err != nil {
		ds.Close()
		return nil, duckError(err)
	}
	if typ != duckdb.STATEMENT_TYPE_SELECT {
		ds.Close()
		return nil, errNotOneSelect
	}
	return ds, nil
}

// implicitStatementsOnly reports whether query, which DuckDB splits into
// several statements, is one statement of the user's that DuckDB split: no ;
// outside literals and comments, and a PIVOT in it.
func implicitStatementsOnly(query string) bool {
	pivot := false
	err := scanSQLTokens(query, func(_ int, tok string) error {
		switch {
		case tok == ";":
			return errNotOneSelect
		case strings.EqualFold(tok, "pivot"):
			pivot = true
		}
		return nil
	})
	return err == nil && pivot
}

// queryTyped is query, also giving fn how to read each result column
// (duckCell of its DuckDB type: UUIDs back as UUID).
func (d *dataset) queryTyped(ctx context.Context, query string, fn func(rec arrow.RecordBatch, conv []cellFunc) error) error {
	return d.withConn(ctx, func(c *duckdb.Conn) error {
		return queryOn(ctx, c, query, fn)
	})
}

// queryOn runs query (after prepareSelect) on connection c.
func queryOn(ctx context.Context, c *duckdb.Conn, query string, fn func(rec arrow.RecordBatch, conv []cellFunc) error) error {
	st, err := prepareSelect(ctx, c, query)
	if err != nil {
		return err
	}
	var conv []cellFunc
	if n, err := st.ColumnCount(); err == nil {
		conv = make([]cellFunc, n)
		for i := range n {
			ti, err := st.ColumnTypeInfo(i)
			if err != nil {
				ti = nil
			}
			conv[i] = duckCell(ti)
		}
	}
	st.Close()
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
		rec := rr.RecordBatch()
		for len(conv) < int(rec.NumCols()) {
			conv = append(conv, ValueAt)
		}
		if err := fn(rec, conv); err != nil {
			return err
		}
	}
	return duckError(rr.Err())
}

// fetchView reads rows [start, start+n) of a filtered and/or sorted view in
// DuckDB. Like pqx, an unsorted filter relies on DuckDB keeping the file's
// order (preserve_insertion_order is on by default), so it has no ORDER BY.
//
// Columns holding wide decimals are read with arrow-go for the rows' file
// numbers (pqx's _fix_wide_decimals). If the query fails, and DuckDB can
// number the rows, the rows are found alone and each column read for them
// by file row (FetchColumns), so a column neither reader can read is in
// Failed rather than failing the window.
func (d *dataset) fetchView(ctx context.Context, v View, start int64, n int, cols []string, idx []int) (Window, error) {
	if err := d.waitBound(ctx); err != nil {
		return Window{}, duckError(err)
	}
	if strings.TrimSpace(v.Where) != "" {
		if _, err := whereSQL(v.Where); err != nil {
			return Window{}, err
		}
	}
	if n == 0 {
		if _, err := d.viewSQL(v, "1"); err != nil {
			return Window{}, err
		}
		return emptyWindow(start, cols), nil
	}
	var duckCols, wideCols []string
	var duckIdx, wideIdx []int
	for i, c := range cols {
		if d.wide[idx[i]] {
			wideCols, wideIdx = append(wideCols, c), append(wideIdx, idx[i])
		} else {
			duckCols, duckIdx = append(duckCols, c), append(duckIdx, idx[i])
		}
	}
	limit := " LIMIT " + strconv.Itoa(n) + " OFFSET " + strconv.FormatInt(start, 10)
	sel, err := d.selectList(duckIdx)
	if err != nil {
		return Window{}, err
	}
	q, err := d.viewSQL(v, sel)
	if err != nil {
		return Window{}, err
	}
	win, err := d.windowFromQuery(ctx, q+limit, start, duckCols)
	if err != nil {
		if ctx.Err() != nil || !d.hasRowNum || len(duckCols) == 0 {
			return Window{}, err
		}
		q2, err2 := d.viewSQL(v, "file_row_number")
		if err2 != nil {
			return Window{}, err
		}
		win2, err2 := d.windowFromQuery(ctx, q2+limit, start, nil)
		if err2 != nil {
			return Window{}, err
		}
		win = win2
		wideCols, wideIdx = cols, idx // every column, by file row
	}
	return d.addByRows(ctx, win, wideCols, wideIdx)
}

// addByRows adds the columns cols (field indices idx) to win, read by its
// file rows (arrow-go where it can), or as failed if the rows have no numbers.
func (d *dataset) addByRows(ctx context.Context, win Window, cols []string, idx []int) (Window, error) {
	if len(cols) == 0 {
		return win, nil
	}
	if slices.Contains(win.FileRows, -1) {
		if win.Failed == nil {
			win.Failed = map[string]error{}
		}
		for _, c := range cols {
			win.Failed[c] = errWideNoRows
		}
		return win, nil
	}
	vals, failed, err := d.columnsByRows(ctx, win.FileRows, distinct(idx))
	if err != nil {
		return Window{}, err
	}
	d.fillWindow(&win, cols, idx, vals, failed)
	return win, nil
}
