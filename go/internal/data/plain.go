package data

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"sort"
	"sync"

	"github.com/apache/arrow-go/v18/arrow"
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

// fetchPlain reads file rows [start, start+n) straight from the row groups
// that hold them, only the requested columns. In each row group, arrow-go's
// SeekToRow skips to the first row wanted: with no page index it still reads
// and decompresses the pages before it, but doesn't decode their values.
func (d *dataset) fetchPlain(ctx context.Context, start int64, n int, cols []string, idx []int) (Window, error) {
	end := min(start+int64(n), d.numRows)
	if end <= start {
		return emptyWindow(start, cols), nil
	}
	w := Window{Start: start, Len: int(end - start), FileRows: make([]int64, end-start), Cols: make(map[string][]string, len(cols))}
	for i := range w.FileRows {
		w.FileRows[i] = start + int64(i)
	}
	// distinct columns, in file order
	fields := make([]int, 0, len(idx))
	seen := make(map[int]bool, len(idx))
	for _, j := range idx {
		if !seen[j] {
			seen[j] = true
			fields = append(fields, j)
		}
	}
	sort.Ints(fields)
	text := make(map[int][]string, len(fields))
	for _, j := range fields {
		text[j] = make([]string, 0, w.Len)
	}

	props := parquet.NewReaderProperties(memory.DefaultAllocator)
	props.BufferedStreamEnabled = BufferedStream
	props.BufferSize = ReadBufferSize
	src := ctxReader{ctx, io.NewSectionReader(d.f, 0, d.size)}
	pf, err := file.NewParquetReader(src, file.WithMetadata(d.md), file.WithReadProps(props))
	if err != nil {
		return Window{}, d.readErr(ctx, err)
	}

	rg := sort.Search(len(d.rgRows), func(i int) bool { return d.rgStart[i+1] > start })
	for ; rg < len(d.rgRows) && d.rgStart[rg] < end; rg++ {
		if err := ctx.Err(); err != nil {
			return Window{}, err
		}
		lo := max(start, d.rgStart[rg]) - d.rgStart[rg]
		hi := min(end, d.rgStart[rg+1]) - d.rgStart[rg]
		if hi <= lo {
			continue
		}
		got, err := d.readRowGroup(ctx, pf, rg, lo, hi, fields)
		if err != nil {
			return Window{}, d.readErr(ctx, err)
		}
		for k, j := range fields {
			text[j] = append(text[j], got[k]...)
		}
	}
	for i, c := range cols {
		w.Cols[c] = text[idx[i]]
	}
	return w, nil
}

// readRowGroup reads rows [lo, hi) of row group rg, for the given top-level
// columns, as cell text; up to ReadParallel columns at once.
func (d *dataset) readRowGroup(ctx context.Context, pf *file.Reader, rg int, lo, hi int64, fields []int) ([][]string, error) {
	fr, err := pqarrow.NewFileReader(pf, pqarrow.ArrowReadProperties{BatchSize: hi - lo}, memory.DefaultAllocator)
	if err != nil {
		return nil, err
	}
	out := make([][]string, len(fields))
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
			cell := FormatCell
			if d.cellText != nil && d.cellText[j] != nil {
				cell = d.cellText[j]
			}
			out[k], errs[k] = readColumn(ctx, fr, rg, d.leaves[j], lo, hi, cell)
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

// readColumn reads rows [lo, hi) of one top-level column (its leaves) in row group rg.
func readColumn(ctx context.Context, fr *pqarrow.FileReader, rg int, leaves []int, lo, hi int64, cell func(arrow.Array, int) string) ([]string, error) {
	rr, err := fr.GetRecordReader(ctx, leaves, []int{rg})
	if err != nil {
		return nil, err
	}
	defer rr.Release()
	if lo > 0 {
		if err := rr.SeekToRow(lo); err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, hi-lo)
	for int64(len(out)) < hi-lo {
		if !rr.Next() {
			if err := rr.Err(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("row group %d ended %d rows early", rg, hi-lo-int64(len(out)))
		}
		rec := rr.RecordBatch()
		if rec.NumCols() != 1 {
			return nil, fmt.Errorf("arrow-go read %d columns for one", rec.NumCols())
		}
		k := min(int(rec.NumRows()), int(hi-lo)-len(out))
		col := rec.Column(0)
		for i := range k {
			out = append(out, cell(col, i))
		}
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
