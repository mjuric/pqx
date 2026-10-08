package data

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

const (
	benchRows  = 2_000_000
	benchGroup = 1 << 20 // like SSSource: about 1M rows per row group
	benchCols  = 30
)

// writeBenchFile writes benchRows rows of benchCols columns (doubles, bigints
// and strings in turn), in row groups of benchGroup rows, with default pages.
func writeBenchFile(b *testing.B, path string) []string {
	b.Helper()
	mem := memory.DefaultAllocator
	var fields []arrow.Field
	for j := range benchCols {
		switch j % 3 {
		case 0:
			fields = append(fields, arrow.Field{Name: fmt.Sprintf("d%d", j), Type: arrow.PrimitiveTypes.Float64})
		case 1:
			fields = append(fields, arrow.Field{Name: fmt.Sprintf("i%d", j), Type: arrow.PrimitiveTypes.Int64})
		default:
			fields = append(fields, arrow.Field{Name: fmt.Sprintf("s%d", j), Type: arrow.BinaryTypes.String, Nullable: true})
		}
	}
	sc := arrow.NewSchema(fields, nil)
	var recs []arrow.RecordBatch
	const chunk = 250_000
	for start := 0; start < benchRows; start += chunk {
		bld := array.NewRecordBuilder(mem, sc)
		for j := range benchCols {
			for i := start; i < start+chunk; i++ {
				switch fb := bld.Field(j).(type) {
				case *array.Float64Builder:
					fb.Append(float64(i) * 0.37 * float64(j+1))
				case *array.Int64Builder:
					fb.Append(int64(i*j) ^ 0x5555)
				case *array.StringBuilder:
					fb.Append("value-" + strconv.Itoa(i%9973))
				}
			}
		}
		recs = append(recs, bld.NewRecordBatch())
		bld.Release()
	}
	tbl := array.NewTableFromRecords(sc, recs)
	writeTable(b, path, tbl, benchGroup, 1<<20)
	tbl.Release()
	for _, r := range recs {
		r.Release()
	}
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return names
}

// BenchmarkWindow fetches a 100-row window of 15 columns at the start, the
// middle and the end of a generated file, with arrow-go (the plain view) and
// with DuckDB's file_row_number filter.
func BenchmarkWindow(b *testing.B) {
	path := filepath.Join(b.TempDir(), "bench.parquet")
	names := writeBenchFile(b, path)
	ds, err := Open(path, Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer ds.Close()
	d := ds.(*dataset)
	cols := names[:15]
	idx, _ := d.columnIndices(cols)
	ctx := context.Background()
	for _, pos := range []struct {
		name  string
		start int64
	}{{"start", 0}, {"middle", benchRows / 2}, {"end", benchRows - 100}} {
		b.Run("arrow/"+pos.name, func(b *testing.B) {
			for b.Loop() {
				if _, err := ds.Fetch(ctx, View{}, pos.start, 100, cols); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("duckdb/"+pos.name, func(b *testing.B) {
			for b.Loop() {
				if _, err := d.fetchPlainDuck(ctx, pos.start, 100, cols, idx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	b.Run("filtered/middle", func(b *testing.B) {
		for b.Loop() {
			if _, err := ds.Fetch(ctx, View{Where: "i1 >= 0"}, benchRows/2, 100, cols); err != nil {
				b.Fatal(err)
			}
		}
	})
}
