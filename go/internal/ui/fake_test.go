package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/mjuric/pqx/go/internal/data"
)

// fakeDS is an in-memory data.Dataset. Column "id" holds the file row
// number; "name" holds "r<row>"; other columns hold "<col>:<row>". A filter
// "id % K = 0" (any K) keeps every K-th row; "none" keeps no rows; "bad"
// fails on read; anything
// with an unbalanced parenthesis fails CheckWhere.
type fakeDS struct {
	rows int64
	cols []data.Column

	mu        sync.Mutex
	fetches   []fetchCall
	cancelled int
	counts    int
	countsCan int

	// gate, when set, makes Fetch wait for a value (or ctx) before reading.
	gate chan struct{}
	// interruptErr, when set, is what a cancelled Fetch or Count returns
	// instead of ctx.Err(), as DuckDB returns "INTERRUPT Error".
	interruptErr error
	// countGate likewise for Count.
	countGate chan struct{}
}

type fetchCall struct {
	view  data.View
	start int64
	n     int
	cols  []string
}

func newFake(rows int64, ncols int) *fakeDS {
	f := &fakeDS{rows: rows}
	f.cols = append(f.cols, data.Column{Name: "id", Type: "BIGINT"}, data.Column{Name: "name", Type: "VARCHAR"})
	for i := 2; i < ncols; i++ {
		f.cols = append(f.cols, data.Column{Name: fmt.Sprintf("c%d", i), Type: "DOUBLE"})
	}
	return f
}

func (f *fakeDS) Path() string           { return "/data/test.parquet" }
func (f *fakeDS) NumRows() int64         { return f.rows }
func (f *fakeDS) Columns() []data.Column { return f.cols }
func (f *fakeDS) RowGroups() []int64     { return []int64{f.rows} }
func (f *fakeDS) Close() error           { return nil }
func (f *fakeDS) calls() []fetchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fetchCall(nil), f.fetches...)
}
func (f *fakeDS) cancels() int      { f.mu.Lock(); defer f.mu.Unlock(); return f.cancelled }
func (f *fakeDS) countCancels() int { f.mu.Lock(); defer f.mu.Unlock(); return f.countsCan }

func (f *fakeDS) CheckWhere(w string) error {
	if strings.Count(w, "(") != strings.Count(w, ")") {
		return errors.New("unbalanced parentheses")
	}
	if strings.Contains(w, ";") {
		return errors.New("only one expression is allowed")
	}
	return nil
}

// step is the K of a "id % K = 0" filter; 1 for the plain view.
func step(v data.View) (int64, error) {
	if v.Plain() {
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

func (f *fakeDS) Fetch(ctx context.Context, v data.View, start int64, n int, cols []string) (data.Window, error) {
	f.mu.Lock()
	f.fetches = append(f.fetches, fetchCall{v, start, n, append([]string(nil), cols...)})
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			f.mu.Lock()
			f.cancelled++
			f.mu.Unlock()
			if f.interruptErr != nil {
				return data.Window{}, f.interruptErr
			}
			return data.Window{}, ctx.Err()
		}
	}
	k, total, err := f.viewRows(v)
	if err != nil {
		return data.Window{}, err
	}
	end := min(start+int64(n), total)
	w := data.Window{Start: start, Cols: map[string][]string{}}
	if end > start {
		w.Len = int(end - start)
	}
	for i := 0; i < w.Len; i++ {
		w.FileRows = append(w.FileRows, (start+int64(i))*k)
	}
	for _, c := range cols {
		vals := make([]string, w.Len)
		for i, fr := range w.FileRows {
			switch c {
			case "id":
				vals[i] = strconv.FormatInt(fr, 10)
			case "name":
				vals[i] = "r" + strconv.FormatInt(fr, 10)
			default:
				vals[i] = c + ":" + strconv.FormatInt(fr, 10)
			}
		}
		w.Cols[c] = vals
	}
	return w, nil
}

func (f *fakeDS) Count(ctx context.Context, v data.View) (int64, error) {
	f.mu.Lock()
	f.counts++
	gate := f.countGate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			f.mu.Lock()
			f.countsCan++
			f.mu.Unlock()
			if f.interruptErr != nil {
				return 0, f.interruptErr
			}
			return 0, ctx.Err()
		}
	}
	_, total, err := f.viewRows(v)
	return total, err
}
