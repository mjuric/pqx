package data

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"slices"
	"sort"
	"sync"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
)

// How the plain view reads column chunks. With a buffered stream, arrow-go
// reads a chunk through a buffer of ReadBufferSize bytes as it goes, so a
// window near the start of a row group reads little and a cancel is seen
// between reads; without it, it reads each whole column chunk up front.
// Variables so the benchmarks can compare.
var (
	BufferedStream = true
	ReadBufferSize = int64(1 << 20)
	// ReadParallel is how many columns the plain view reads at once.
	ReadParallel = defaultReadParallel
)

var defaultReadParallel = runtime.GOMAXPROCS(0)

// ctxReader is the file as arrow-go reads it for one Fetch: every read checks
// the Fetch's context first, so a cancelled Fetch stops at its next read.
type ctxReader struct {
	ctx context.Context
	*io.SectionReader
}

func (r ctxReader) ReadAt(p []byte, off int64) (int, error) {
	if beforeRead != nil {
		beforeRead()
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.SectionReader.ReadAt(p, off)
}

// beforeRead, if set (by tests), runs before every read of the plain view.
var beforeRead func()

// fetchPlain reads file rows [start, start+n) of the plain view, each column
// with the reader that can (readRows): arrow-go, straight from the row
// groups that hold them, for the columns it reads exactly as DuckDB does.
func (d *dataset) fetchPlain(ctx context.Context, start int64, n int, cols []string, idx []int) (Window, error) {
	end := min(start+int64(n), d.numRows)
	if end <= start {
		return emptyWindow(start, cols), nil
	}
	w := Window{Start: start, Len: int(end - start), FileRows: make([]int64, end-start), Cols: make(map[string][]Value, len(cols))}
	for i := range w.FileRows {
		w.FileRows[i] = start + int64(i)
	}
	vals, failed, err := d.readRows(ctx, w.FileRows, distinct(idx))
	if err != nil {
		return Window{}, err
	}
	d.fillWindow(&w, cols, idx, vals, failed)
	return w, nil
}

// readDirect reads the file rows rows (sorted, distinct) of the top-level
// columns fields with arrow-go, converted by d.direct: out[k] holds column
// fields[k]'s values, one per row. In each row group, arrow-go's SeekToRow
// skips to the first row wanted: with no page index it still reads and
// decompresses the pages before it, but doesn't decode their values; rows
// between the first and last wanted are decoded and dropped.
func (d *dataset) readDirect(ctx context.Context, rows []int64, fields []int) ([][]Value, error) {
	out := make([][]Value, len(fields))
	for k := range out {
		out[k] = make([]Value, 0, len(rows))
	}
	if len(rows) == 0 {
		return out, nil
	}
	props := parquet.NewReaderProperties(memory.DefaultAllocator)
	props.BufferedStreamEnabled = BufferedStream
	props.BufferSize = ReadBufferSize
	src := ctxReader{ctx, io.NewSectionReader(d.f, 0, d.size)}
	pf, err := file.NewParquetReader(src, file.WithMetadata(d.md), file.WithReadProps(props))
	if err != nil {
		return nil, d.readErr(ctx, err)
	}
	for i := 0; i < len(rows); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rg := sort.Search(len(d.rgRows), func(k int) bool { return d.rgStart[k+1] > rows[i] })
		base, end := d.rgStart[rg], d.rgStart[rg+1]
		j := i
		for j < len(rows) && rows[j] < end {
			j++
		}
		local := make([]int64, j-i)
		for k := range local {
			local[k] = rows[i+k] - base
		}
		got, err := d.readRowGroup(ctx, pf, rg, local, fields)
		if err != nil {
			return nil, d.readErr(ctx, err)
		}
		for k := range fields {
			out[k] = append(out[k], got[k]...)
		}
		i = j
	}
	return out, nil
}

// readRowGroup reads the rows local (sorted, distinct, relative to the row
// group) of row group rg, for the given top-level columns, as Values; up to
// ReadParallel columns at once.
func (d *dataset) readRowGroup(ctx context.Context, pf *file.Reader, rg int, local []int64, fields []int) ([][]Value, error) {
	if readRGHook != nil {
		if err := readRGHook(rg, fields); err != nil {
			return nil, err
		}
	}
	lo, hi := local[0], local[len(local)-1]+1
	fr, err := pqarrow.NewFileReader(pf, pqarrow.ArrowReadProperties{BatchSize: min(hi-lo, maxBatch)}, memory.DefaultAllocator)
	if err != nil {
		return nil, err
	}
	// a repeated column is read from the row group's start (see readColumn)
	frRep := fr
	if lo > 0 && slices.ContainsFunc(fields, d.repeated) {
		if frRep, err = pqarrow.NewFileReader(pf, pqarrow.ArrowReadProperties{BatchSize: min(hi, maxBatch)}, memory.DefaultAllocator); err != nil {
			return nil, err
		}
	}
	pick := local
	if int64(len(local)) == hi-lo {
		pick = nil // every row of [lo, hi)
	}
	out := make([][]Value, len(fields))
	errs := make([]error, len(fields))
	sem := make(chan struct{}, max(1, ReadParallel))
	var wg sync.WaitGroup
	for k, j := range fields {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			if ctx.Err() != nil {
				errs[k] = ctx.Err()
				return
			}
			cell := ValueAt
			if d.direct != nil && d.direct[j] != nil {
				cell = d.direct[j]
			}
			if d.repeated(j) {
				out[k], errs[k] = readColumn(ctx, frRep, rg, d.leaves[j], lo, hi, pick, cell, false)
			} else {
				out[k], errs[k] = readColumn(ctx, fr, rg, d.leaves[j], lo, hi, pick, cell, true)
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// maxBatch bounds the rows arrow-go decodes at once when it reads a long
// stretch of a row group for a few rows.
const maxBatch = 64 << 10

// readRGHook, if set (by tests), runs before each read of a row group, with
// the columns read; an error it returns is the read's.
var readRGHook func(rg int, fields []int) error

// readHook, if set (by tests), runs before each column's read in a row
// group; an error it returns is the read's.
var readHook func(rg int, leaves []int) error

// readColumn reads rows [lo, hi) of one top-level column (its leaves) in row
// group rg, keeping only the rows pick (sorted, in [lo, hi)) unless it is nil.
//
// A column with repeated leaves (lists, maps) is read from the row group's
// first row, the rows before lo dropped: arrow-go v18.8's SeekToRow
// miscounts records in a repeated column whose row group has more than one
// data page (it returns rows twice, or panics). Minimal reproduction: write a
// list<int64> column with pqarrow.WriteTable, 13 rows per row group,
// parquet.WithDataPageSize(64) and WithBatchSize(2) (so each row group has
// several pages), values [k]*(k%4) for row k; GetRecordReader on that column
// and row group 0, SeekToRow(1), then Next: it gives more rows than are left
// in the row group, or panics with an index out of range (TestNestedColumnsInsideRowGroups).
//
// A panic in arrow-go (it panics on some corrupt pages) is returned as an
// error.
func readColumn(ctx context.Context, fr *pqarrow.FileReader, rg int, leaves []int, lo, hi int64, pick []int64, cell cellFunc, seek bool) (out []Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("arrow-go failed reading row group %d: %v", rg, r)
		}
	}()
	if readHook != nil {
		if err := readHook(rg, leaves); err != nil {
			return nil, err
		}
	}
	rr, err := fr.GetRecordReader(ctx, leaves, []int{rg})
	if err != nil {
		return nil, err
	}
	defer rr.Release()
	at := int64(0) // row (in the row group) of the next batch's first row
	if lo > 0 && seek {
		if err := rr.SeekToRow(lo); err != nil {
			return nil, err
		}
		at = lo
	}
	want := hi - lo
	if pick != nil {
		want = int64(len(pick))
	}
	out = make([]Value, 0, want)
	p := 0 // next of pick
	for at < hi {
		if !rr.Next() {
			if err := rr.Err(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("row group %d ended %d rows early", rg, hi-at)
		}
		rec := rr.RecordBatch()
		if rec.NumCols() != 1 {
			return nil, fmt.Errorf("arrow-go read %d columns for one", rec.NumCols())
		}
		m := min(rec.NumRows(), hi-at)
		col := rec.Column(0)
		if pick == nil {
			for i := max(0, lo-at); i < m; i++ {
				out = append(out, cell(col, int(i)))
			}
		} else {
			for p < len(pick) && pick[p] < at+m {
				out = append(out, cell(col, int(pick[p]-at)))
				p++
			}
		}
		at += m
	}
	return out, nil
}

// readErr is ctx's error if ctx is done (a read failed because of it), else
// err with the file named.
func (d *dataset) readErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return safeErr(fmt.Errorf("reading %s: %w", d.path, err))
}

// repeated reports whether top-level column j has a repeated leaf (a list or
// map somewhere in it).
func (d *dataset) repeated(j int) bool {
	for _, leaf := range d.leaves[j] {
		if d.md.Schema.Column(leaf).MaxRepetitionLevel() > 0 {
			return true
		}
	}
	return false
}
