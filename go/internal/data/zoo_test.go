package data

import (
	"context"
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/mjuric/pqx/go/internal/golden"
)

// sameValue is a == b for Values, with NaN equal to NaN (and -0 not equal
// to 0), decimals compared by value, scale and precision, and Timestamps by
// instant, zone flag and unit.
func sameVal(a, b Value) bool {
	switch a := a.(type) {
	case float64:
		b, ok := b.(float64)
		return ok && (math.Float64bits(a) == math.Float64bits(b) || math.IsNaN(a) && math.IsNaN(b))
	case float32:
		b, ok := b.(float32)
		return ok && (math.Float32bits(a) == math.Float32bits(b) || a != a && b != b)
	case Decimal:
		b, ok := b.(Decimal)
		return ok && a.Scale == b.Scale && a.Precision == b.Precision && a.Unscaled.Cmp(b.Unscaled) == 0
	case Timestamp:
		b, ok := b.(Timestamp)
		return ok && a.T.Equal(b.T) && a.Zoned == b.Zoned && a.Unit == b.Unit
	case []byte:
		b, ok := b.([]byte)
		return ok && string(a) == string(b)
	case List:
		b, ok := b.(List)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !sameVal(a[i], b[i]) {
				return false
			}
		}
		return true
	case Struct:
		b, ok := b.(Struct)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i].Name != b[i].Name || !sameVal(a[i].Value, b[i].Value) {
				return false
			}
		}
		return true
	case Map:
		b, ok := b.(Map)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !sameVal(a[i].Key, b[i].Key) || !sameVal(a[i].Value, b[i].Value) {
				return false
			}
		}
		return true
	}
	return a == b
}

// sameWindow fails t unless a and b hold the same rows and values.
func sameWindow(t *testing.T, what string, a, b Window) {
	t.Helper()
	if a.Start != b.Start || a.Len != b.Len || !slices.Equal(a.FileRows, b.FileRows) {
		t.Fatalf("%s: Start %d/%d Len %d/%d FileRows %v / %v", what, a.Start, b.Start, a.Len, b.Len, a.FileRows, b.FileRows)
	}
	if len(a.Cols) != len(b.Cols) || len(a.Failed) != len(b.Failed) {
		t.Fatalf("%s: %d/%d columns, failed %v / %v", what, len(a.Cols), len(b.Cols), a.Failed, b.Failed)
	}
	for c, av := range a.Cols {
		bv, ok := b.Cols[c]
		if !ok || len(av) != len(bv) {
			t.Fatalf("%s %s: %d vs %d values", what, c, len(av), len(bv))
		}
		for i := range av {
			if !sameVal(av[i], bv[i]) {
				t.Errorf("%s %s row %d: %#v vs %#v", what, c, a.FileRows[i], av[i], bv[i])
			}
		}
	}
}

func openFixture(t *testing.T, name string) *dataset {
	t.Helper()
	ds, err := Open(golden.Fixture(name), Options{Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ds.Close() })
	return ds.(*dataset)
}

// The plain view's reader (arrow-go, converted) and DuckDB give the same Value
// for every cell of the zoo of types (pqx's test_zoo_direct_equals_duckdb), and
// arrow-go reads every column exactly but the ones it can't.
func TestZooDirectEqualsDuckDB(t *testing.T) {
	d := openFixture(t, "types.parquet")
	ctx := context.Background()
	cols := colNames(d)
	var duckOnly []string
	for i, c := range d.cols {
		if d.direct[i] == nil {
			duckOnly = append(duckOnly, c.Name)
		}
	}
	t.Logf("columns DuckDB reads in the plain view: %v", duckOnly)
	// arrow-go can't read durations: DuckDB reads them (its BIGINT counts)
	if !slices.Equal(duckOnly, []string{"dur_s", "dur_ms", "dur_us", "dur_ns"}) {
		t.Errorf("columns arrow-go doesn't read exactly: %v", duckOnly)
	}
	defer func() {
		if len(d.fb.excluded) > 0 || len(d.fb.badRGs) > 0 {
			t.Errorf("arrow-go failed on columns %v, row groups %v", d.fb.excluded, d.fb.badRGs)
		}
	}()
	var narrow []string // all but the wide decimals, which DuckDB reads wrong
	var wideIdx []int
	for i, c := range cols {
		if d.wide[i] {
			wideIdx = append(wideIdx, i)
		} else {
			narrow = append(narrow, c)
		}
	}
	idx, _ := d.columnIndices(narrow)
	for _, w := range []struct {
		start int64
		n     int
	}{{0, 60}, {0, 1}, {5, 10}, {6, 1}, {7, 7}, {13, 30}, {59, 5}, {60, 5}, {1000, 5}, {3, 0}} {
		what := fmt.Sprintf("window %d+%d", w.start, w.n)
		plain, err := d.Fetch(ctx, View{}, w.start, w.n, narrow)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		duck, err := d.fetchPlainDuck(ctx, w.start, w.n, narrow, idx)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		sameWindow(t, what+" arrow-go vs DuckDB", plain, duck)
		all, err := d.Fetch(ctx, View{Where: "true"}, w.start, w.n, narrow)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		sameWindow(t, what+" plain vs filtered", plain, all)
		if w.n > 0 && w.start < 60 {
			rows := slices.Clone(plain.FileRows)
			slices.Reverse(rows)
			byRows, err := d.FetchColumns(ctx, rows, narrow)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range narrow {
				got := slices.Clone(byRows.Cols[c])
				slices.Reverse(got)
				for i := range got {
					if !sameVal(got[i], plain.Cols[c][i]) {
						t.Errorf("%s FetchColumns %s row %d: %#v vs %#v", what, c, plain.FileRows[i], got[i], plain.Cols[c][i])
					}
				}
			}
		}
	}
	if len(wideIdx) != 2 {
		t.Errorf("wide decimal columns: %v", wideIdx)
	}
}
