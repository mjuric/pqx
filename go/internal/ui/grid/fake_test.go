package grid

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/mjuric/pqx/go/internal/data"
)

// fakeDS is an in-memory data.Dataset (the filter hint's one-row read of
// row 0 is neither gated nor logged). Column "id" holds the file row
// number; "name" holds "r<row>"; column c<j> holds row*1000 + j (int64),
// except that every column whose index j has j%10 == 5 is a float column
// f<j> holding row + j/1000, made much wider at every 97th row (as
// tests/test_lazycols.py's fixture). A filter "id % K = 0" (any K) keeps
// every K-th row; "none" keeps no rows; "bad" fails on read; anything with
// an unbalanced parenthesis fails Validate. Sorting reverses the order when
// descending and keeps it ascending. A SQL view ("select …") has columns id
// and name and no file rows.
type fakeDS struct {
	data.Unimplemented
	rows int64
	cols []data.Column

	mu        sync.Mutex
	calls     []call
	cancelled int
	countsCan int

	// gate, when set, makes Fetch wait for a value (or ctx) before reading.
	gate chan struct{}
	// colsGate likewise for FetchColumns.
	colsGate chan struct{}
	// colsHook, when set, is called by FetchColumns before reading; an
	// error it returns is FetchColumns' result.
	colsHook func(ctx context.Context, cols []string) error
	// interruptErr, when set, is what a cancelled Fetch or Count returns
	// instead of ctx.Err(), as DuckDB returns "INTERRUPT Error".
	interruptErr error
	// countGate likewise for Count.
	countGate chan struct{}
	// noColumnsAPI makes FetchColumns return ErrNotImplemented.
	noColumnsAPI bool
	// footer, if set, is FooterSummary's answer.
	footer []data.ChunkSummary
}

type call struct {
	kind  string // "fetch" or "columns"
	view  data.View
	start int64
	n     int
	rows  []int64
	cols  []string
}

func newFake(rows int64, ncols int) *fakeDS {
	f := &fakeDS{rows: rows}
	f.cols = append(f.cols, data.Column{Name: "id", Type: "BIGINT", Arrow: arrow.PrimitiveTypes.Int64},
		data.Column{Name: "name", Type: "VARCHAR", Arrow: arrow.BinaryTypes.String})
	for j := 2; j < ncols; j++ {
		if j%10 == 5 {
			f.cols = append(f.cols, data.Column{Name: fmt.Sprintf("f%03d", j), Type: "DOUBLE", Arrow: arrow.PrimitiveTypes.Float64})
		} else {
			f.cols = append(f.cols, data.Column{Name: fmt.Sprintf("c%03d", j), Type: "BIGINT", Arrow: arrow.PrimitiveTypes.Int64})
		}
	}
	for i := range f.cols {
		f.cols[i].SQLName = f.cols[i].Name
	}
	return f
}

// truth is the value of column name at file row fr.
func truth(name string, fr int64) data.Value {
	switch {
	case name == "id":
		return fr
	case name == "name":
		return "r" + strconv.FormatInt(fr, 10)
	case name[0] == 'f':
		j, _ := strconv.Atoi(name[1:])
		v := float64(fr) + float64(j)/1000
		if fr%97 == 0 {
			v *= -1e5
		}
		return v
	}
	j, _ := strconv.Atoi(name[1:])
	return fr*1000 + int64(j)
}

func (f *fakeDS) Path() string           { return "/data/test.parquet" }
func (f *fakeDS) NumRows() int64         { return f.rows }
func (f *fakeDS) Columns() []data.Column { return f.cols }
func (f *fakeDS) RowGroups() []int64     { return []int64{f.rows} }

func (f *fakeDS) log() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}
func (f *fakeDS) clearLog()         { f.mu.Lock(); f.calls = nil; f.mu.Unlock() }
func (f *fakeDS) cancels() int      { f.mu.Lock(); defer f.mu.Unlock(); return f.cancelled }
func (f *fakeDS) countCancels() int { f.mu.Lock(); defer f.mu.Unlock(); return f.countsCan }

func (f *fakeDS) fetches() []call {
	var out []call
	for _, c := range f.log() {
		if c.kind == "fetch" {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeDS) Validate(ctx context.Context, v data.View) ([]data.Column, error) {
	if v.IsSQL() {
		return f.cols[:2], nil
	}
	if strings.Count(v.Where, "(") != strings.Count(v.Where, ")") {
		return nil, errors.New("unbalanced parentheses")
	}
	if strings.Contains(v.Where, ";") {
		return nil, errors.New("only one expression is allowed")
	}
	return f.Columns(), nil
}

// step is the K of a "id % K = 0" filter; 1 for no filter.
func step(v data.View) (int64, error) {
	if strings.TrimSpace(v.Where) == "" {
		return 1, nil
	}
	if v.Where == "bad" {
		return 0, errors.New(`Binder Error: column "bad" not found`)
	}
	var k int64
	if _, err := fmt.Sscanf(v.Where, "id %% %d = 0", &k); err != nil || k <= 0 {
		return 1, nil
	}
	return k, nil
}

func (f *fakeDS) viewRows(v data.View) (int64, int64, error) {
	if v.Where == "none" {
		return 1, 0, nil
	}
	k, err := step(v)
	if err != nil {
		return 0, 0, err
	}
	return k, (f.rows + k - 1) / k, nil
}

// fileRow is the file row at position i of view v.
func (f *fakeDS) fileRow(v data.View, i int64) int64 {
	k, total, _ := f.viewRows(v)
	if len(v.OrderBy) > 0 && v.OrderBy[0].Desc {
		i = total - 1 - i
	}
	return i * k
}

func (f *fakeDS) wait(ctx context.Context, gate chan struct{}, counter *int) error {
	if gate == nil {
		return nil
	}
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		f.mu.Lock()
		*counter++
		f.mu.Unlock()
		if f.interruptErr != nil {
			return f.interruptErr
		}
		return ctx.Err()
	}
}

func (f *fakeDS) Fetch(ctx context.Context, v data.View, start int64, n int, cols []string) (data.Window, error) {
	f.mu.Lock()
	gate := f.gate
	if n == 1 && start == 0 && v.Plain() {
		gate = nil // the filter hint's read: not gated nor logged
	} else {
		f.calls = append(f.calls, call{kind: "fetch", view: v, start: start, n: n, cols: append([]string(nil), cols...)})
	}
	f.mu.Unlock()
	if err := f.wait(ctx, gate, &f.cancelled); err != nil {
		return data.Window{}, err
	}
	_, total, err := f.viewRows(v)
	if err != nil {
		return data.Window{}, err
	}
	end := min(start+int64(n), total)
	w := data.Window{Start: start, Cols: map[string][]data.Value{}}
	if end > start {
		w.Len = int(end - start)
	}
	rows := make([]int64, w.Len)
	for i := range rows {
		rows[i] = f.fileRow(v, start+int64(i))
	}
	if !v.IsSQL() {
		w.FileRows = rows
	}
	f.fill(&w, rows, cols)
	return w, nil
}

func (f *fakeDS) fill(w *data.Window, rows []int64, cols []string) {
	for _, c := range cols {
		vals := make([]data.Value, len(rows))
		for i, fr := range rows {
			vals[i] = truth(c, fr)
		}
		w.Cols[c] = vals
	}
}

func (f *fakeDS) FetchColumns(ctx context.Context, fileRows []int64, cols []string) (data.Window, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{kind: "columns", rows: append([]int64(nil), fileRows...), cols: append([]string(nil), cols...)})
	gate, hook := f.colsGate, f.colsHook
	f.mu.Unlock()
	if f.noColumnsAPI {
		return data.Window{}, data.ErrNotImplemented
	}
	if hook != nil {
		if err := hook(ctx, cols); err != nil {
			return data.Window{}, err
		}
	}
	if err := f.wait(ctx, gate, &f.cancelled); err != nil {
		return data.Window{}, err
	}
	w := data.Window{Len: len(fileRows), FileRows: fileRows, Cols: map[string][]data.Value{}}
	f.fill(&w, fileRows, cols)
	return w, nil
}

func (f *fakeDS) Count(ctx context.Context, v data.View) (int64, error) {
	f.mu.Lock()
	gate := f.countGate
	f.mu.Unlock()
	if err := f.wait(ctx, gate, &f.countsCan); err != nil {
		return 0, err
	}
	_, total, err := f.viewRows(v)
	return total, err
}

func (f *fakeDS) FooterSummary(ctx context.Context) ([]data.ChunkSummary, error) {
	if f.footer == nil {
		return nil, data.ErrNotImplemented
	}
	return f.footer, nil
}
