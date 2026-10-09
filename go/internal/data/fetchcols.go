package data

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
)

// fallback is what the plain view has learnt about arrow-go's reads (pqx's
// _read_rows and _exclude_unreadable): columns arrow-go can't read, which
// DuckDB reads from then on; row groups where arrow-go failed but no one
// column alone did, which DuckDB reads from then on; and how many reads in a
// row failed for I/O reasons (after three, the columns are probed too).
type fallback struct {
	mu       sync.Mutex
	excluded map[int]bool
	badRGs   map[int]bool
	ioErrors int
}

func (f *fallback) isExcluded(j int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.excluded[j]
}

func (f *fallback) anyBad(rgs []int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, rg := range rgs {
		if f.badRGs[rg] {
			return true
		}
	}
	return false
}

// isIOError reports whether err is the OS failing to read the file (maybe
// for a moment, as on a network filesystem), not arrow-go failing to decode it.
func isIOError(err error) bool {
	var pe *fs.PathError
	var errno syscall.Errno
	return errors.As(err, &pe) || errors.As(err, &errno)
}

// errNoRowNumbers is FetchColumns' error for rows without numbers.
var errNoRowNumbers = errors.New("these rows have no file row numbers (a SQL result, or a filtered or sorted view of a file with its own file_row_number column): fetch them with their view instead")

// errWideNoRows is the reason a decimal wider than 38 digits can't be shown in
// a view whose rows DuckDB can't number.
var errWideNoRows = errors.New("DuckDB reads decimals wider than 38 digits wrongly, and can't number this file's rows to read them otherwise")

// FetchColumns reads cols for the given file rows, in their order (rows may
// repeat). The plain view's rows (one run) are read with arrow-go; others
// with arrow-go or DuckDB, whichever should be faster (see directCheaper).
func (d *dataset) FetchColumns(ctx context.Context, fileRows []int64, cols []string) (Window, error) {
	if err := ctx.Err(); err != nil {
		return Window{}, err
	}
	idx, err := d.columnIndices(cols)
	if err != nil {
		return Window{}, err
	}
	for _, r := range fileRows {
		if r < 0 {
			return Window{}, errNoRowNumbers
		}
		if r >= d.numRows {
			return Window{}, fmt.Errorf("file row %d is out of range (the file has %d rows)", r, d.numRows)
		}
	}
	w := Window{FileRows: slices.Clone(fileRows), Len: len(fileRows), Cols: make(map[string][]Value, len(cols))}
	if w.FileRows == nil {
		w.FileRows = []int64{}
	}
	vals, failed, err := d.columnsByRows(ctx, fileRows, distinct(idx))
	if err != nil {
		return Window{}, err
	}
	d.fillWindow(&w, cols, idx, vals, failed)
	return w, nil
}

// fillWindow puts the values (and failures) of the columns, by field
// index, into w under the requested names.
func (d *dataset) fillWindow(w *Window, cols []string, idx []int, vals map[int][]Value, failed map[int]error) {
	for i, c := range cols {
		j := idx[i]
		if err, ok := failed[j]; ok {
			if w.Failed == nil {
				w.Failed = map[string]error{}
			}
			w.Failed[c] = err
			delete(w.Cols, c)
			continue
		}
		if v, ok := vals[j]; ok {
			w.Cols[c] = v
		}
	}
}

// distinct is idx without repeats, in order.
func distinct(idx []int) []int {
	out := make([]int, 0, len(idx))
	seen := make(map[int]bool, len(idx))
	for _, j := range idx {
		if !seen[j] {
			seen[j] = true
			out = append(out, j)
		}
	}
	return out
}

// columnsByRows reads the columns fields (distinct) for fileRows (valid row
// numbers, any order, may repeat): values aligned with fileRows, and the
// columns neither reader could read.
func (d *dataset) columnsByRows(ctx context.Context, fileRows []int64, fields []int) (map[int][]Value, map[int]error, error) {
	uniq := slices.Clone(fileRows)
	slices.Sort(uniq)
	uniq = slices.Compact(uniq)
	got, failed, err := d.readRows(ctx, uniq, fields)
	if err != nil {
		return nil, nil, err
	}
	if len(uniq) == len(fileRows) && slices.Equal(uniq, fileRows) {
		return got, failed, nil
	}
	pos := make(map[int64]int, len(uniq))
	for i, r := range uniq {
		pos[r] = i
	}
	out := make(map[int][]Value, len(got))
	for j, v := range got {
		o := make([]Value, len(fileRows))
		for i, r := range fileRows {
			o[i] = v[pos[r]]
		}
		out[j] = o
	}
	return out, failed, nil
}

// readRows reads the columns fields (distinct) for the file rows rows
// (sorted, distinct, in range), each with the reader that can:
//
//   - arrow-go, where its values convert exactly to DuckDB's (d.direct), it
//     hasn't failed on the column or those row groups before, and the rows are
//     one run or arrow-go should be faster (directCheaper);
//   - DuckDB for the others, and for every column if arrow-go fails (as pqx);
//   - arrow-go alone for columns DuckDB reads wrongly (wide decimals) and
//     when DuckDB can't read the file.
//
// Columns that fail on their own in the reader left to them are in failed.
func (d *dataset) readRows(ctx context.Context, rows []int64, fields []int) (map[int][]Value, map[int]error, error) {
	vals := make(map[int][]Value, len(fields))
	failed := map[int]error{}
	if len(fields) == 0 {
		return vals, failed, nil
	}
	if len(rows) == 0 {
		for _, j := range fields {
			vals[j] = []Value{}
		}
		return vals, failed, nil
	}
	rgs := d.rowGroupsOf(rows)
	contiguous := rows[len(rows)-1]-rows[0]+1 == int64(len(rows))
	noDuck := d.bindErr != nil || d.direct == nil
	bad := d.fb.anyBad(rgs)
	cheap := !forceDuck && (contiguous || d.directCheaper(rows, fields))
	// arrow-go reads a repeated column from its row group's start (see
	// readColumn): DuckDB is faster for rows deep in a row group (100 rows of
	// two list columns 512k rows in: 106 ms against 23 ms)
	deep := d.deepestLocal(rows) >= repeatedSkipRows
	var direct, duck []int
	for _, j := range fields {
		switch {
		case noDuck || d.wide[j]:
			direct = append(direct, j)
		case d.direct[j] == nil || bad || !cheap || d.fb.isExcluded(j) || deep && d.repeated(j):
			duck = append(duck, j)
		default:
			direct = append(direct, j)
		}
	}
	if len(direct) > 0 {
		got, err := d.readDirect(ctx, rows, direct)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			d.noteDirectFailure(ctx, err, rows, rgs, direct)
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			// as pqx: DuckDB reads the whole lot this time; the columns
			// only arrow-go can read are read one by one
			for _, j := range direct {
				if !noDuck && !d.wide[j] {
					duck = append(duck, j)
					continue
				}
				one, err := d.readDirect(ctx, rows, []int{j})
				if err != nil {
					if ctx.Err() != nil {
						return nil, nil, ctx.Err()
					}
					failed[j] = safeErr(err)
					continue
				}
				vals[j] = one[0]
			}
		} else {
			d.fb.mu.Lock()
			d.fb.ioErrors = 0
			d.fb.mu.Unlock()
			for k, j := range direct {
				vals[j] = got[k]
			}
		}
	}
	if len(duck) > 0 {
		slices.Sort(duck)
		got, err := d.readDuckRows(ctx, rows, duck)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			// one column at a time, to tell the ones DuckDB can't read
			for _, j := range duck {
				one, err := d.readDuckRows(ctx, rows, []int{j})
				if err != nil {
					if ctx.Err() != nil {
						return nil, nil, ctx.Err()
					}
					failed[j] = err
					continue
				}
				vals[j] = one[0]
			}
		} else {
			for k, j := range duck {
				vals[j] = got[k]
			}
		}
	}
	return vals, failed, nil
}

// forceDuck, set by tests, has DuckDB read every column it can.
var forceDuck bool

// repeatedSkipRows is how far into a row group arrow-go still reads a
// repeated column (from the row group's start) for the plain view.
var repeatedSkipRows = int64(32 << 10)

// deepestLocal is the largest row number, within its row group, of rows.
func (d *dataset) deepestLocal(rows []int64) int64 {
	deepest := int64(0)
	for _, r := range rows {
		rg := sort.Search(len(d.rgRows), func(k int) bool { return d.rgStart[k+1] > r })
		deepest = max(deepest, r-d.rgStart[rg])
	}
	return deepest
}

// rowGroupsOf is the row groups holding rows (sorted).
func (d *dataset) rowGroupsOf(rows []int64) []int {
	var out []int
	for i := 0; i < len(rows); {
		rg := sort.Search(len(d.rgRows), func(k int) bool { return d.rgStart[k+1] > rows[i] })
		out = append(out, rg)
		end := d.rgStart[rg+1]
		for i < len(rows) && rows[i] < end {
			i++
		}
	}
	return out
}

// noteDirectFailure learns from a failed arrow-go read of the columns fields:
// an I/O error counts (three in a row and the columns are probed); any
// other error probes them now: each column read alone (one row) that fails
// is left to DuckDB from now on; if none fails alone, the row groups are.
func (d *dataset) noteDirectFailure(ctx context.Context, err error, rows []int64, rgs []int, fields []int) {
	if isIOError(err) {
		d.fb.mu.Lock()
		d.fb.ioErrors++
		n := d.fb.ioErrors
		d.fb.mu.Unlock()
		if n < 3 {
			return
		}
	}
	probe := []int64{d.rgStart[rgs[0]]}
	found := false
	for _, j := range fields {
		if _, err := d.readDirect(ctx, probe, []int{j}); err != nil {
			if ctx.Err() != nil {
				return
			}
			if isIOError(err) {
				continue
			}
			d.fb.mu.Lock()
			if d.fb.excluded == nil {
				d.fb.excluded = map[int]bool{}
			}
			d.fb.excluded[j] = true
			d.fb.mu.Unlock()
			found = true
		}
	}
	if !found {
		d.fb.mu.Lock()
		if d.fb.badRGs == nil {
			d.fb.badRGs = map[int]bool{}
		}
		for _, rg := range rgs {
			d.fb.badRGs[rg] = true
		}
		d.fb.mu.Unlock()
	}
}

// Cost model for rows that aren't one run (pqx's _direct_estimate): arrow-go
// decodes each row group from its start (it decompresses the pages before a
// row even when it skips them) to the last row wanted; DuckDB skips rows
// faster but sets up a scan of every row group in the file per query.
// Milliseconds and bytes. arrowNsPerByte is from SSSource and mpc_orbits
// (1.1 to 2.3 ns per uncompressed byte for 50 rows of 20 columns); DuckDB's
// constants are pqx's. DuckDB reads row groups in parallel and its time
// varied less (40 to 700 ms there, against 20 ms to 14 s for arrow-go,
// whose worst case is a wide text column decoded up to a row deep in a row
// group), so the model leans to DuckDB.
var (
	arrowNsPerByte    = 1.5
	duckSkipRatio     = 0.3 // DuckDB's cost to skip a byte, relative to arrow-go decoding it
	duckMsBase        = 4.0
	duckMsPerCol      = 0.1
	duckMsPerRG       = 0.16
	duckMsPerRGCol    = 0.001
	directAlwaysBytes = float64(4 << 20) // below this much decoding, arrow-go anyway
)

// directCheaper reports whether arrow-go should read rows (sorted) of
// fields faster than DuckDB.
func (d *dataset) directCheaper(rows []int64, fields []int) bool {
	decoded := 0.0
	for i := 0; i < len(rows); {
		rg := sort.Search(len(d.rgRows), func(k int) bool { return d.rgStart[k+1] > rows[i] })
		end := d.rgStart[rg+1]
		last := rows[i]
		for i < len(rows) && rows[i] < end {
			last = rows[i]
			i++
		}
		n := d.rgRows[rg]
		if n <= 0 {
			continue
		}
		frac := float64(last-d.rgStart[rg]+1) / float64(n)
		g := d.md.RowGroup(rg)
		for _, j := range fields {
			for _, leaf := range d.leaves[j] {
				if c, err := g.ColumnChunk(leaf); err == nil {
					decoded += float64(c.TotalUncompressedSize()) * frac
				}
			}
		}
	}
	if decoded <= directAlwaysBytes {
		return true
	}
	arrowMs := arrowNsPerByte * 1e-6 * decoded
	k := float64(len(fields))
	duckMs := duckMsBase + duckMsPerCol*k + float64(len(d.rgRows))*(duckMsPerRG+duckMsPerRGCol*k) + duckSkipRatio*arrowMs
	return arrowMs <= duckMs
}

// readDuckRows reads the columns fields for rows (sorted, distinct) with
// DuckDB: by file_row_number (a range, or a list), or, for a file DuckDB
// can't number, by LIMIT/OFFSET for each run of rows.
func (d *dataset) readDuckRows(ctx context.Context, rows []int64, fields []int) ([][]Value, error) {
	if err := d.waitBound(ctx); err != nil {
		return nil, duckError(err)
	}
	cols := make([]string, len(fields))
	for k, j := range fields {
		q, err := quoteIdent(d.duckNames[j])
		if err != nil {
			return nil, err
		}
		cols[k] = q
	}
	sel := strings.Join(cols, ", ")
	out := make([][]Value, len(fields))
	var got []int64
	read := func(q string, numbered bool) func(*duckdbConn) error {
		return func(c *duckdbConn) error {
			return queryOn(ctx, c, q, func(rec arrow.RecordBatch, conv []cellFunc) error {
				m := int(rec.NumRows())
				off := 0
				if numbered {
					rn, ok := rec.Column(0).(*array.Int64)
					if !ok {
						return fmt.Errorf("file_row_number came back as %s", rec.Column(0).DataType())
					}
					for i := range m {
						got = append(got, rn.Value(i))
					}
					off = 1
				}
				for k := range fields {
					out[k] = append(out[k], valueColumn(rec.Column(k+off), 0, m, conv[k+off])...)
				}
				return nil
			})
		}
	}
	var err error
	if d.hasRowNum {
		var cond string
		if rows[len(rows)-1]-rows[0]+1 == int64(len(rows)) {
			cond = fmt.Sprintf("file_row_number >= %d AND file_row_number < %d", rows[0], rows[len(rows)-1]+1)
		} else {
			var b strings.Builder
			// (the range lets DuckDB skip the row groups outside it)
			fmt.Fprintf(&b, "file_row_number >= %d AND file_row_number <= %d AND file_row_number IN (", rows[0], rows[len(rows)-1])
			for i, r := range rows {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(strconv.FormatInt(r, 10))
			}
			b.WriteString(")")
			cond = b.String()
		}
		q := "SELECT file_row_number, " + sel + " FROM " + d.src + " WHERE " + cond + " ORDER BY file_row_number"
		err = d.withConn(ctx, read(q, true))
		if err == nil && !slices.Equal(got, rows) {
			err = fmt.Errorf("DuckDB returned %d of %d rows", len(got), len(rows))
		}
	} else {
		err = d.withConn(ctx, func(c *duckdbConn) error {
			for i := 0; i < len(rows); {
				j := i + 1
				for j < len(rows) && rows[j] == rows[j-1]+1 {
					j++
				}
				q := fmt.Sprintf("SELECT %s FROM %s LIMIT %d OFFSET %d", sel, d.src, j-i, rows[i])
				if err := read(q, false)(c); err != nil {
					return err
				}
				i = j
			}
			return nil
		})
		if err == nil && len(out) > 0 && len(out[0]) != len(rows) {
			err = fmt.Errorf("DuckDB returned %d of %d rows", len(out[0]), len(rows))
		}
	}
	if err != nil {
		return nil, err
	}
	for k := range out {
		if out[k] == nil {
			out[k] = []Value{}
		}
	}
	return out, nil
}
