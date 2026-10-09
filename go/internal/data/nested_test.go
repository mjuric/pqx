package data

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
)

// nestedSmallPages writes repeated columns (list, map, list of struct of map
// of list) in row groups of 13 rows and 64-byte pages, so each row group
// has several data pages.
func nestedSmallPages(t *testing.T) *dataset {
	t.Helper()
	const n = 60
	mapT := arrow.MapOf(arrow.BinaryTypes.String, arrow.PrimitiveTypes.Int64)
	inner := arrow.MapOf(arrow.BinaryTypes.String, arrow.ListOf(arrow.PrimitiveTypes.Int32))
	deepT := arrow.ListOf(arrow.StructOf(arrow.Field{Name: "m", Type: inner, Nullable: true}, arrow.Field{Name: "s", Type: arrow.BinaryTypes.String, Nullable: true}))
	sc := arrow.NewSchema([]arrow.Field{
		{Name: "id", Type: arrow.PrimitiveTypes.Int64},
		{Name: "l", Type: arrow.ListOf(arrow.PrimitiveTypes.Int64), Nullable: true},
		{Name: "m", Type: mapT, Nullable: true},
		{Name: "deep", Type: deepT, Nullable: true},
	}, nil)
	var js []string
	for k := range n {
		l := "null"
		if k%5 != 4 {
			var xs []string
			for range k % 4 {
				xs = append(xs, fmt.Sprint(k))
			}
			l = "[" + join(xs) + "]"
		}
		m := "null"
		if k%6 != 5 {
			m = fmt.Sprintf(`[{"key": "k%d", "value": %d}, {"key": "z", "value": null}]`, k, k)
		}
		deep := "null"
		switch k % 4 {
		case 1:
			deep = "[]"
		case 2:
			deep = fmt.Sprintf(`[{"m": [{"key": "a", "value": [%d, null, %d]}], "s": "x%d"}, null]`, k, -k, k)
		case 3:
			deep = fmt.Sprintf(`[{"m": null, "s": null}, {"m": [{"key": "b", "value": []}], "s": "y%d"}]`, k)
		}
		js = append(js, fmt.Sprintf(`{"id": %d, "l": %s, "m": %s, "deep": %s}`, k, l, m, deep))
	}
	rec, _, err := array.RecordFromJSON(memory.DefaultAllocator, sc, readerOf("["+join(js)+"]"))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "nested.parquet")
	tbl := array.NewTableFromRecords(sc, []arrow.RecordBatch{rec})
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	// (arrow-go cuts pages only between write batches: batches of 2 values)
	props := parquet.NewWriterProperties(parquet.WithDataPageSize(64), parquet.WithBatchSize(2),
		parquet.WithMaxRowGroupLength(13), parquet.WithDictionaryDefault(false))
	if err := pqarrow.WriteTable(tbl, f, 13, props, pqarrow.NewArrowWriterProperties(pqarrow.WithStoreSchema())); err != nil {
		t.Fatal(err)
	}
	ds, err := Open(p, Options{Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ds.Close() })
	d := ds.(*dataset)
	return d
}

// Repeated columns read right from a row inside a row group with several
// pages (arrow-go v18.8's SeekToRow miscounts records there), in the plain
// view and in FetchColumns, against DuckDB.
func TestNestedColumnsInsideRowGroups(t *testing.T) {
	d := nestedSmallPages(t)
	cols := colNames(d)
	// (and with DuckDB reading the repeated columns past row 3 of a row group)
	defer func(v int64) { repeatedSkipRows = v }(repeatedSkipRows)
	for _, repeatedSkipRows = range []int64{repeatedSkipRows, 3} {
		testNested(t, d, cols)
	}
}

func testNested(t *testing.T, d *dataset, cols []string) {
	for _, start := range []int64{0, 1, 5, 12, 13, 14, 25, 27, 40, 59} {
		for _, n := range []int{1, 3, 8, 30} {
			directVsDuck(t, d, start, n, cols)
		}
	}
	rows := []int64{50, 1, 14, 14, 27, 3, 59, 40}
	a, err := d.FetchColumns(bg, rows, cols)
	if err != nil {
		t.Fatal(err)
	}
	forceDuck = true
	b, err := d.FetchColumns(bg, rows, cols)
	forceDuck = false
	if err != nil {
		t.Fatal(err)
	}
	sameWindow(t, "FetchColumns", a, b)
	// and the values are the ones written
	w := mustFetch(t, d, View{}, 7, 1, []string{"id", "l"})
	if w.Cols["id"][0] != int64(7) || !slices.EqualFunc(w.Cols["l"][0].(List), List{int64(7), int64(7), int64(7)}, func(a, b Value) bool { return a == b }) {
		t.Fatalf("%v", w.Cols)
	}
}

func join(xs []string) string {
	s := ""
	for i, x := range xs {
		if i > 0 {
			s += ", "
		}
		s += x
	}
	return s
}

func readerOf(s string) *strings.Reader { return strings.NewReader(s) }
