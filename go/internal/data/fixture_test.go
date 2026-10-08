package data

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
)

const (
	fixRows  = 1000
	fixGroup = 300 // rows per row group: 300, 300, 300, 100
)

// hostile strings, as in pqx's tests: escape sequences, C1, bidi, zero-width, newlines
var hostile = []string{
	"\x1b]0;pwned\x07", "a\u009bb\x7f\x00", "abc‮dcba", "x​y", "tab\tnew\nline", "plain", "é ✓ 漢字",
}

var fixEpoch = time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

// writeFixture writes a small Parquet file with several row groups (and
// several data pages in each), NULLs, a float32, hostile strings,
// timestamps and two columns whose names differ only in case.
func writeFixture(t testing.TB, path string) {
	t.Helper()
	mem := memory.DefaultAllocator
	sc := arrow.NewSchema([]arrow.Field{
		{Name: "id", Type: arrow.PrimitiveTypes.Int64},
		{Name: "i32", Type: arrow.PrimitiveTypes.Int32, Nullable: true},
		{Name: "f32", Type: arrow.PrimitiveTypes.Float32},
		{Name: "f64", Type: arrow.PrimitiveTypes.Float64, Nullable: true},
		{Name: "s", Type: arrow.BinaryTypes.String, Nullable: true},
		{Name: "ts", Type: &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}},
		{Name: "tsl", Type: &arrow.TimestampType{Unit: arrow.Microsecond}},
		{Name: "b", Type: arrow.FixedWidthTypes.Boolean},
		{Name: "Name", Type: arrow.PrimitiveTypes.Int64},
		{Name: "name", Type: arrow.PrimitiveTypes.Int64},
		{Name: "with space \"q\"", Type: arrow.PrimitiveTypes.Int16},
	}, nil)
	bld := array.NewRecordBuilder(mem, sc)
	defer bld.Release()
	for i := range fixRows {
		bld.Field(0).(*array.Int64Builder).Append(int64(i))
		if i%7 == 3 {
			bld.Field(1).AppendNull()
		} else {
			bld.Field(1).(*array.Int32Builder).Append(int32(i * 3))
		}
		bld.Field(2).(*array.Float32Builder).Append(float32(i) / 3)
		if i%5 == 0 {
			bld.Field(3).AppendNull()
		} else {
			bld.Field(3).(*array.Float64Builder).Append(float64(i) * 1.0000001e10)
		}
		if i%11 == 0 {
			bld.Field(4).AppendNull()
		} else {
			bld.Field(4).(*array.StringBuilder).Append(fmt.Sprintf("%s#%d", hostile[i%len(hostile)], i))
		}
		ts := fixEpoch.Add(time.Duration(i) * 1500 * time.Millisecond).Add(time.Duration(i%3) * time.Microsecond)
		bld.Field(5).(*array.TimestampBuilder).Append(arrow.Timestamp(ts.UnixMicro()))
		bld.Field(6).(*array.TimestampBuilder).Append(arrow.Timestamp(ts.UnixMicro()))
		bld.Field(7).(*array.BooleanBuilder).Append(i%2 == 0)
		bld.Field(8).(*array.Int64Builder).Append(int64(i))
		bld.Field(9).(*array.Int64Builder).Append(int64(-i))
		bld.Field(10).(*array.Int16Builder).Append(int16(i % 100))
	}
	rec := bld.NewRecordBatch()
	defer rec.Release()
	tbl := array.NewTableFromRecords(sc, []arrow.RecordBatch{rec})
	defer tbl.Release()
	writeTable(t, path, tbl, fixGroup, 512)
}

func writeTable(t testing.TB, path string, tbl arrow.Table, group, pageSize int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	props := parquet.NewWriterProperties(
		parquet.WithDataPageSize(pageSize),
		parquet.WithCompression(compress.Codecs.Snappy),
		parquet.WithMaxRowGroupLength(group),
	)
	// (WriteTable closes f)
	if err := pqarrow.WriteTable(tbl, f, group, props, pqarrow.NewArrowWriterProperties(pqarrow.WithStoreSchema())); err != nil {
		t.Fatal(err)
	}
}

// writeInts writes a one-column (a BIGINT) file.
func writeInts(t testing.TB, path string, vals ...int64) {
	t.Helper()
	sc := arrow.NewSchema([]arrow.Field{{Name: "a", Type: arrow.PrimitiveTypes.Int64}}, nil)
	b := array.NewInt64Builder(memory.DefaultAllocator)
	b.AppendValues(vals, nil)
	arr := b.NewArray()
	rec := array.NewRecordBatch(sc, []arrow.Array{arr}, int64(len(vals)))
	tbl := array.NewTableFromRecords(sc, []arrow.RecordBatch{rec})
	writeTable(t, path, tbl, 1<<20, 1<<20)
	tbl.Release()
	rec.Release()
	arr.Release()
	b.Release()
}

func fixture(t testing.TB) (string, Dataset) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fix.parquet")
	writeFixture(t, path)
	ds, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ds.Close() })
	return path, ds
}

func colNames(ds Dataset) []string {
	var out []string
	for _, c := range ds.Columns() {
		out = append(out, c.Name)
	}
	return out
}
