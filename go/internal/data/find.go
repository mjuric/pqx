package data

import (
	"context"
	"fmt"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
)

// FindRow is the position of file row fileRow in the view (pqx's find_row).
// An unsorted filter keeps the file's order, so there the position is a
// count of the matching rows before it, which reads only the row groups up
// to it (file_row_number is pushed down); a sorted view numbers its rows. A
// SQL view, or a filtered or sorted view of a file DuckDB can't number, has
// no file rows: found is false.
func (d *dataset) FindRow(ctx context.Context, v View, fileRow int64) (int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	if v.Plain() {
		return fileRow, fileRow >= 0 && fileRow < d.numRows, nil
	}
	if v.IsSQL() {
		return 0, false, nil
	}
	if strings.TrimSpace(v.Where) != "" {
		if _, err := whereSQL(v.Where); err != nil {
			return 0, false, err
		}
	}
	if err := d.waitBound(ctx); err != nil {
		return 0, false, duckError(err)
	}
	if !d.hasRowNum || fileRow < 0 || fileRow >= d.numRows {
		if _, err := d.viewSQL(v, "1"); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	var q string
	if len(v.OrderBy) == 0 {
		w, _ := whereSQL(v.Where)
		q = fmt.Sprintf("SELECT count(*) FILTER (WHERE file_row_number < %d), count(*) FILTER (WHERE file_row_number = %d) "+
			"FROM %s WHERE file_row_number <= %d AND %s", fileRow, fileRow, d.src, fileRow, w)
	} else {
		ob, err := d.orderBy(v.OrderBy)
		if err != nil {
			return 0, false, err
		}
		inner, err := d.viewSQL(View{Where: v.Where}, "file_row_number, row_number() OVER (ORDER BY "+ob+") - 1 AS pos")
		if err != nil {
			return 0, false, err
		}
		q = fmt.Sprintf("SELECT pos, 1 FROM (%s) WHERE file_row_number = %d", inner, fileRow)
	}
	var pos int64
	found := false
	err := d.query(ctx, q, func(rec arrow.RecordBatch) error {
		if rec.NumRows() == 0 {
			return nil
		}
		p, ok1 := ValueAt(rec.Column(0), 0).(int64)
		hit, ok2 := ValueAt(rec.Column(1), 0).(int64)
		if !ok1 || !ok2 {
			return fmt.Errorf("find_row came back as %s, %s", rec.Column(0).DataType(), rec.Column(1).DataType())
		}
		pos, found = p, hit > 0
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	if !found {
		return 0, false, nil
	}
	return pos, true, nil
}

// canFetchAround reports whether FetchAround can find a window of v by
// file row (pqx's can_fetch_around): a filtered, unsorted view of a file
// whose rows DuckDB numbers, which keeps the file's order.
func (d *dataset) canFetchAround(v View) bool {
	return !v.IsSQL() && len(v.OrderBy) == 0 && strings.TrimSpace(v.Where) != "" && d.hasRowNum
}

// FetchAround is Fetch(v, start, n, cols) for a window holding file row
// fileRow, the view's row pos (pqx's fetch_around). For a filtered,
// unsorted view it finds the window's rows by file row number near fileRow
// rather than counting the view's rows from its start, so its cost doesn't
// grow with pos: rows after it are the first matches from it on; rows
// before it are looked for in file-row ranges reaching further back until
// there are enough. If fewer rows of the view precede the record than
// pos-start, the window starts later and Start says where. Other views (and
// a pos outside [start, start+n)) are read with Fetch.
func (d *dataset) FetchAround(ctx context.Context, v View, fileRow, pos, start int64, n int, cols []string) (Window, error) {
	if err := ctx.Err(); err != nil {
		return Window{}, err
	}
	start, n = max(start, 0), max(n, 1)
	if !d.canFetchAround(v) || pos < start || pos >= start+int64(n) {
		return d.Fetch(ctx, v, start, n, cols)
	}
	idx, err := d.columnIndices(cols)
	if err != nil {
		return Window{}, err
	}
	w, err := whereSQL(v.Where)
	if err != nil {
		return Window{}, err
	}
	if err := d.waitBound(ctx); err != nil {
		return Window{}, duckError(err)
	}
	rowNums := func(q string) ([]int64, error) {
		var out []int64
		err := d.query(ctx, q, func(rec arrow.RecordBatch) error {
			c, ok := rec.Column(0).(*array.Int64)
			if !ok {
				return fmt.Errorf("file_row_number came back as %s", rec.Column(0).DataType())
			}
			for i := range int(rec.NumRows()) {
				out = append(out, c.Value(i))
			}
			return nil
		})
		return out, err
	}
	fr := fileRow
	after, err := rowNums(fmt.Sprintf("SELECT file_row_number FROM %s WHERE file_row_number >= %d AND %s LIMIT %d",
		d.src, fr, w, max(1, start+int64(n)-pos)))
	if err != nil {
		return Window{}, err
	}
	hi := fr
	for _, r := range after {
		hi = max(hi, r)
	}
	lo, k := fr, pos-start // k rows of the view before the record are wanted
	span := max(4*k, 1024)
	for k > 0 {
		a := max(0, fr-span)
		got, err := rowNums(fmt.Sprintf("SELECT file_row_number FROM %s WHERE file_row_number >= %d AND file_row_number < %d AND %s "+
			"ORDER BY file_row_number DESC LIMIT %d", d.src, a, fr, w, k))
		if err != nil {
			return Window{}, err
		}
		if int64(len(got)) >= k || a == 0 {
			lo = fr
			for _, r := range got {
				lo = min(lo, r)
			}
			start = pos - int64(len(got))
			break
		}
		span *= 8
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
	sel, err := d.selectList(duckIdx)
	if err != nil {
		return Window{}, err
	}
	q := fmt.Sprintf("SELECT %s FROM %s WHERE file_row_number >= %d AND file_row_number <= %d AND %s ORDER BY file_row_number",
		sel, d.src, lo, hi, w)
	win, err := d.windowFromQuery(ctx, q, start, duckCols)
	if err != nil {
		if ctx.Err() != nil || len(duckCols) == 0 {
			return Window{}, err
		}
		q2 := fmt.Sprintf("SELECT file_row_number FROM %s WHERE file_row_number >= %d AND file_row_number <= %d AND %s ORDER BY file_row_number",
			d.src, lo, hi, w)
		win2, err2 := d.windowFromQuery(ctx, q2, start, nil)
		if err2 != nil {
			return Window{}, err
		}
		win, wideCols, wideIdx = win2, cols, idx
	}
	return d.addByRows(ctx, win, wideCols, wideIdx)
}
