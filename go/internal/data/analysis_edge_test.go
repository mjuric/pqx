package data

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
)

// writeFloats writes a file of float64 columns, one per name, of the same
// length, in row groups of group rows.
func writeFloats(t testing.TB, path string, group int64, names []string, cols ...[]float64) {
	t.Helper()
	fields := make([]arrow.Field, len(names))
	arrs := make([]arrow.Array, len(names))
	for i, n := range names {
		fields[i] = arrow.Field{Name: n, Type: arrow.PrimitiveTypes.Float64}
		b := array.NewFloat64Builder(memory.DefaultAllocator)
		b.AppendValues(cols[i], nil)
		arrs[i] = b.NewArray()
		b.Release()
	}
	sc := arrow.NewSchema(fields, nil)
	rec := array.NewRecordBatch(sc, arrs, int64(len(cols[0])))
	tbl := array.NewTableFromRecords(sc, []arrow.RecordBatch{rec})
	writeTable(t, path, tbl, group, 1<<20)
	tbl.Release()
	rec.Release()
	for _, a := range arrs {
		a.Release()
	}
}

func openT(t testing.TB, path string) *dataset {
	t.Helper()
	ds, err := Open(path, Options{Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ds.Close() })
	return ds.(*dataset)
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// DuckDB's own temporary file for an existing target is tmp_<name>: if that
// is the file being explored, or someone else's, it must not be touched.
func TestExportNeverWritesTmpName(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tmp_x.parquet")
	writeInts(t, src, 1, 2, 3)
	before, _ := os.ReadFile(src)
	target := filepath.Join(dir, "x.parquet")
	writeInts(t, target, 9)
	ds := openT(t, src)
	n, err := ds.Export(bg, View{}, target, ExportParquet, nil)
	if err != nil || n != 3 {
		t.Fatalf("export: %d %v", n, err)
	}
	after, _ := os.ReadFile(src)
	if string(after) != string(before) {
		t.Fatal("the source changed")
	}
	if got := openT(t, target); got.NumRows() != 3 {
		t.Errorf("target has %d rows", got.NumRows())
	}

	other := filepath.Join(dir, "tmp_y.csv")
	if err := os.WriteFile(other, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	y := filepath.Join(dir, "y.csv")
	if err := os.WriteFile(y, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.Export(bg, View{}, y, ExportCSV, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(other); string(b) != "keep" {
		t.Errorf("tmp_y.csv now %q", b)
	}
	if b, _ := os.ReadFile(y); string(b) != "a\n1\n2\n3\n" {
		t.Errorf("y.csv: %q", b)
	}
	if fi, _ := os.Stat(y); fi.Mode().Perm() != 0o640 {
		t.Errorf("y.csv's mode %v", fi.Mode())
	}
	want := []string{"tmp_x.parquet", "tmp_y.csv", "x.parquet", "y.csv"}
	if got := dirNames(t, dir); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("directory: %v", got)
	}
}

// A cancelled or failed export leaves the target as it was (or absent) and
// no temporary file.
func TestExportCancelLeavesNothing(t *testing.T) {
	_, dsI := fixture(t)
	ds := dsI.(*dataset)
	dir := t.TempDir()
	existing := filepath.Join(dir, "old.csv")
	os.WriteFile(existing, []byte("old"), 0o644)
	for _, target := range []string{filepath.Join(dir, "new.csv"), existing} {
		ctx, cancel := context.WithCancel(bg)
		time.AfterFunc(100*time.Millisecond, cancel)
		if _, err := ds.Export(ctx, View{Where: slow}, target, ExportCSV, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s: %v", target, err)
		}
		// the query stops in the background; then its file goes
		deadline := time.Now().Add(10 * time.Second)
		for len(dirNames(t, dir)) != 1 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := dirNames(t, dir); len(got) != 1 || got[0] != "old.csv" {
			t.Errorf("after a cancel: %v", got)
		}
		if b, _ := os.ReadFile(existing); string(b) != "old" {
			t.Errorf("old.csv: %q", b)
		}
	}
	if _, err := ds.Export(bg, View{Where: "nope > 1"}, existing, ExportCSV, nil); err == nil {
		t.Error("bad filter: no error")
	}
	if got := dirNames(t, dir); len(got) != 1 {
		t.Errorf("after a failure: %v", got)
	}
}

// Each way of being the source: the file Open opened (which may since
// have been replaced at its path), and the file now at its path.
func TestExportIsSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.parquet")
	writeInts(t, path, 1)
	ds := openT(t, path)
	opened := filepath.Join(dir, "opened.parquet")
	if err := os.Link(path, opened); err != nil {
		t.Fatal(err)
	}
	// replace the file at the path
	repl := filepath.Join(dir, "r.parquet")
	writeInts(t, repl, 2)
	if err := os.Rename(repl, path); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(dir, "current.parquet")
	if err := os.Link(path, current); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other.parquet")
	writeInts(t, other, 3)
	for p, want := range map[string]bool{opened: true, current: true, other: false, filepath.Join(dir, "none"): false} {
		if got := ds.isSource(p); got != want {
			t.Errorf("isSource(%s) = %v", filepath.Base(p), got)
		}
		_, err := ds.Export(bg, View{}, p, ExportCSV, nil)
		if errors.Is(err, ErrOverwriteSource) != want {
			t.Errorf("export to %s: %v", filepath.Base(p), err)
		}
	}
}

func TestPrepareCopy(t *testing.T) {
	_, dsI := fixture(t)
	ds := dsI.(*dataset)
	out := filepath.Join(t.TempDir(), "o.csv")
	err := ds.withConn(bg, func(c *duckdbConn) error {
		for q, ok := range map[string]bool{
			"COPY (SELECT 1) TO " + quoteStr(out) + " (FORMAT csv)": true,
			"SELECT 1": false,
			"COPY (SELECT 1) TO " + quoteStr(out) + "; SELECT 1":               false,
			"CREATE TABLE x AS SELECT 1":                                       false,
			"COPY (SELECT 1) TO " + quoteStr(out) + "; COPY (SELECT 2) TO 'y'": false,
		} {
			st, err := prepareCopy(c, q)
			if (err == nil) != ok {
				t.Errorf("%q: %v", q, err)
			}
			if st != nil {
				st.Close()
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExportEmptyColumnList(t *testing.T) {
	_, ds := demoDataset(t)
	out := filepath.Join(t.TempDir(), "all.parquet")
	if n, err := ds.Export(bg, View{Where: "diaSourceId < 3"}, out, ExportParquet, []string{}); err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	if got := openT(t, out); len(got.Columns()) != len(ds.Columns()) {
		t.Errorf("%d columns", len(got.Columns()))
	}
}

// Ranges wider than a float64 holds bin in halves.
func TestBinsHugeRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.parquet")
	v := []float64{-1e308, 0, 1e308}
	writeFloats(t, path, 10, []string{"x", "y"}, v, v)
	ds := openT(t, path)
	lo, hi := -1.7e308, 1.7e308
	h, err := ds.Histogram(bg, View{}, "x", HistOptions{Bins: 4, Lo: &lo, Hi: &hi})
	if err != nil {
		t.Fatal(err)
	}
	if h.Edges[0] != lo || h.Edges[2] != 0 || h.Edges[4] != hi || sum(h.Counts) != 3 || h.Counts[0] != 1 || h.Counts[2] != 1 || h.Counts[3] != 1 {
		t.Errorf("given limits: %v %v", h.Edges, h.Counts)
	}
	h, err = ds.Histogram(bg, View{}, "x", HistOptions{Bins: 2})
	if err != nil || h.Edges[0] != -1e308 || h.Edges[2] != 1e308 || h.Counts[0] != 1 || h.Counts[1] != 2 {
		t.Errorf("its own limits: %v %v %v", h.Edges, h.Counts, err)
	}
	g, err := ds.XYCounts(bg, View{}, "x", "y", 2, 2, Sample{}, &[2]float64{lo, hi}, &[2]float64{lo, hi})
	if err != nil || g.Counts[0][0] != 1 || g.Counts[1][1] != 2 {
		t.Errorf("xy, given limits: %v %v", g.Counts, err)
	}
	g, err = ds.XYCounts(bg, View{}, "x", "y", 2, 2, Sample{}, nil, nil)
	if err != nil || g.X != [2]float64{-1e308, 1e308} || g.Counts[0][0] != 1 || g.Counts[1][1] != 1 {
		t.Errorf("xy, its own limits: %v %v %v", g.X, g.Counts, err)
	}
	// one value too large to add 1 to
	h, err = ds.Histogram(bg, View{Where: "x > 0"}, "x", HistOptions{Bins: 3})
	if err != nil || h.Edges[0] != 1e308 || !(h.Edges[3] > 1e308) || sum(h.Counts) != 1 {
		t.Errorf("one huge value: %v %v %v", h.Edges, h.Counts, err)
	}
	inf := math.Inf(1)
	if _, err := ds.Histogram(bg, View{}, "x", HistOptions{Hi: &inf}); err == nil || !strings.Contains(err.Error(), "must be finite") {
		t.Errorf("an infinite Hi: %v", err)
	}
	if _, err := ds.XYCounts(bg, View{}, "x", "y", 2, 2, Sample{}, &[2]float64{0, inf}, nil); err == nil {
		t.Error("an infinite x limit: no error")
	}
}

func TestBinsTooMany(t *testing.T) {
	_, ds := demoDataset(t)
	for _, res := range []float64{1e-7, 1e-3, 1e-300} {
		if _, err := ds.SkyCounts(bg, View{}, "ra", "dec", res, Sample{}); err == nil {
			t.Errorf("resolution %v: no error", res)
		}
	}
	if _, err := ds.XYCounts(bg, View{}, "mag", "snr", 5000, 5000, Sample{}, nil, nil); err == nil {
		t.Error("25M cells: no error")
	}
	if _, err := ds.Histogram(bg, View{}, "mag", HistOptions{Bins: 1 << 30}); err == nil {
		t.Error("2^30 bins: no error")
	}
	if _, err := ds.SkyCounts(bg, View{}, "ra", "dec", 0.1, Sample{}); err != nil {
		t.Errorf("0.1°: %v", err)
	}
}

// The 0.1–99.9% quantiles are used only if they differ (">", not ">="), and
// are of the finite values only.
func TestXYRobustRange(t *testing.T) {
	const n = 10_000
	x, y, z := make([]float64, n), make([]float64, n), make([]float64, n)
	for i := range x {
		x[i], y[i], z[i] = 5, float64(i%100), 7
		if i%3 == 0 {
			y[i] = math.Inf(1 - 2*(i%2)) // ±inf: left out
		}
	}
	x[1], x[2] = 0, 10 // (rows whose y is finite)
	z[4], z[5] = -3, 20
	path := filepath.Join(t.TempDir(), "q.parquet")
	writeFloats(t, path, 2500, []string{"x", "y", "z"}, x, y, z)
	ds := openT(t, path)
	g, err := ds.XYCounts(bg, View{}, "x", "y", 4, 4, Sample{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if g.X != [2]float64{0, 10} {
		t.Errorf("x limits %v: quantiles 5 and 5 should give way to min and max", g.X)
	}
	if !(g.Y[0] >= 0 && g.Y[1] <= 99 && g.Y[0] < g.Y[1]) {
		t.Errorf("y limits %v", g.Y)
	}
	g, err = ds.XYCounts(bg, View{}, "y", "z", 4, 4, Sample{}, nil, nil)
	if err != nil || g.Y != [2]float64{-3, 20} {
		t.Errorf("z limits %v (%v): quantiles 7 and 7 should give way to min and max", g.Y, err)
	}
	// a third of the values are ±inf, which would be the 1% and 99% quantiles
	st, err := ds.ColumnStats(bg, View{}, "y", Sample{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Max != math.Inf(1) || st.Min != math.Inf(-1) || len(st.Quantiles) != 7 {
		t.Errorf("y: %+v", st)
	}
	for q, v := range st.Quantiles {
		if !(v >= 0 && v <= 99) {
			t.Errorf("quantile %v = %v: not of the finite values", q, v)
		}
	}
}

// A float column with more than 1000 distinct values has no top list (an
// integer one with the same values does).
func TestStatsFloatTopSkip(t *testing.T) {
	const n = 6000
	f := make([]float64, n)
	for i := range f {
		f[i] = float64(i % 2000)
	}
	path := filepath.Join(t.TempDir(), "f.parquet")
	writeFloats(t, path, n, []string{"f"}, f)
	ds := openT(t, path)
	st, err := ds.ColumnStats(bg, View{}, "f", Sample{})
	if err != nil || st.Top != nil || st.Distinct <= 1000 {
		t.Errorf("float: %+v %v", st, err)
	}
	st, err = ds.ColumnStats(bg, View{SQL: "SELECT f::BIGINT AS i FROM t"}, "i", Sample{})
	if err != nil || len(st.Top) != 10 || st.Top[0].Count != 3 {
		t.Errorf("integer: %+v %v", st, err)
	}
	// a trailing comment in a sampled SQL view
	st, err = ds.ColumnStats(bg, View{SQL: "SELECT * FROM t -- a note"}, "f", Sample{Rows: 100})
	if err != nil || st.Count != 100 || !st.Sampled {
		t.Errorf("sampled, commented: %+v %v", st, err)
	}
}

// Leaves with the same path add up; Encodings covers all of them.
func TestFooterSharedPaths(t *testing.T) {
	sc := arrow.NewSchema([]arrow.Field{
		{Name: "a", Type: arrow.PrimitiveTypes.Int64},
		{Name: "b", Type: arrow.PrimitiveTypes.Int64},
		{Name: "a", Type: arrow.BinaryTypes.String},
	}, nil)
	b := array.NewRecordBuilder(memory.DefaultAllocator, sc)
	defer b.Release()
	for i := range 100 {
		b.Field(0).(*array.Int64Builder).Append(int64(i))
		b.Field(1).(*array.Int64Builder).Append(int64(-i))
		b.Field(2).(*array.StringBuilder).Append("s")
	}
	rec := b.NewRecordBatch()
	defer rec.Release()
	tbl := array.NewTableFromRecords(sc, []arrow.RecordBatch{rec})
	defer tbl.Release()
	path := filepath.Join(t.TempDir(), "dup.parquet")
	f, _ := os.Create(path)
	props := parquet.NewWriterProperties(parquet.WithMaxRowGroupLength(50), parquet.WithDictionaryFor("a", false))
	if err := pqarrow.WriteTable(tbl, f, 50, props, pqarrow.NewArrowWriterProperties()); err != nil {
		t.Skipf("arrow-go won't write two columns named a: %v", err)
	}
	dsI, err := Open(path, Options{})
	if err != nil {
		t.Skipf("can't open it: %v", err)
	}
	defer dsI.Close()
	ds := dsI.(*dataset)
	summ, err := ds.FooterSummary(bg)
	if err != nil {
		t.Fatal(err)
	}
	if len(summ) != 2 || summ[0].Path != "a" || summ[1].Path != "b" {
		t.Fatalf("paths %+v", summ)
	}
	// give the second leaf an encoding of its own, so both must be read
	for _, rg := range ds.md.RowGroups {
		cm := rg.Columns[2].MetaData
		cm.Encodings = append(cm.Encodings, 9) // BYTE_STREAM_SPLIT
	}
	var comp int64
	encs := map[string]bool{}
	for rg := range ds.md.NumRowGroups() {
		for _, i := range []int{0, 2} {
			c, _ := ds.md.RowGroup(rg).ColumnChunk(i)
			comp += c.TotalCompressedSize()
			for _, e := range c.Encodings() {
				encs[e.String()] = true
			}
		}
	}
	if summ[0].Compressed != comp || summ[0].Physical != "INT64" {
		t.Errorf("a: %+v, both leaves %d", summ[0], comp)
	}
	if got := ds.Encodings("a"); len(got) != len(encs) || !encs["BYTE_STREAM_SPLIT"] || !slices.Contains(got, "BYTE_STREAM_SPLIT") {
		t.Errorf("encodings %v, both leaves %v", got, encs)
	}
}

// Chunks whose statistics have neither min/max nor a null count, or a
// malformed min, count as having none; returned values are copies.
func TestFooterOddStatistics(t *testing.T) {
	dm, ds := demoDataset(t)
	cols := ds.md.RowGroups[0].Columns
	st := cols[0].MetaData.Statistics // diaSourceId: nothing
	st.Min, st.Max, st.MinValue, st.MaxValue, st.NullCount = nil, nil, nil, nil, nil
	cols[2].MetaData.Statistics.MinValue = []byte{1} // mag: a 1-byte DOUBLE
	for _, rg := range ds.md.RowGroups {
		rg.Columns[1].MetaData.Codec = 5 // band: Hadoop's LZ4
	}
	summ, err := ds.FooterSummary(bg)
	if err != nil {
		t.Fatal(err)
	}
	if s := summ[0]; s.HasStats || s.Min != int64(demoGroup) || s.Max != int64(dm.n-1) {
		t.Errorf("diaSourceId: %+v", s)
	}
	var nulls int64
	for i := demoGroup; i < dm.n; i++ {
		if dm.magNull[i] {
			nulls++
		}
	}
	if s := summ[2]; s.HasStats || s.Nulls != nulls {
		t.Errorf("mag: %+v (nulls after the first row group %d)", s, nulls)
	}
	if summ[1].Compression != "LZ4_HADOOP" {
		t.Errorf("codec 5: %s", summ[1].Compression)
	}
}

// Old writers' statistics of FIXED_LEN_BYTE_ARRAY decimals are wrong
// (PARQUET-1655) and are dropped here, as arrow-go's reader drops them
// (PyArrow 25 keeps them: a deliberate difference); returned decimals
// and bytes are copies.
func TestFooterOldDecimalStatistics(t *testing.T) {
	sc := arrow.NewSchema([]arrow.Field{
		{Name: "dec", Type: &arrow.Decimal128Type{Precision: 30, Scale: 2}},
		{Name: "bin", Type: arrow.BinaryTypes.Binary},
	}, nil)
	b := array.NewRecordBuilder(memory.DefaultAllocator, sc)
	defer b.Release()
	for i := range 10 {
		b.Field(0).(*array.Decimal128Builder).Append(decimal128.FromI64(int64(i - 5)))
		b.Field(1).(*array.BinaryBuilder).Append([]byte{byte(i)})
	}
	rec := b.NewRecordBatch()
	defer rec.Release()
	tbl := array.NewTableFromRecords(sc, []arrow.RecordBatch{rec})
	defer tbl.Release()
	for _, c := range []struct {
		createdBy string
		stats     bool
	}{{"parquet-cpp-arrow version 3.0.0", false}, {"parquet-cpp-arrow version 4.0.0", true}} {
		path := filepath.Join(t.TempDir(), "dec.parquet")
		f, _ := os.Create(path)
		props := parquet.NewWriterProperties(parquet.WithCreatedBy(c.createdBy))
		if err := pqarrow.WriteTable(tbl, f, 10, props, pqarrow.NewArrowWriterProperties()); err != nil {
			t.Fatal(err)
		}
		ds := openT(t, path)
		if got := ds.Info().CreatedBy; got != c.createdBy {
			t.Fatalf("created by %q", got)
		}
		summ, err := ds.FooterSummary(bg)
		if err != nil {
			t.Fatal(err)
		}
		s := summ[0]
		if s.Physical != "FIXED_LEN_BYTE_ARRAY" || s.HasStats != c.stats || (s.Min != nil) != c.stats {
			t.Errorf("%s: %+v", c.createdBy, s)
		}
		if !c.stats {
			continue
		}
		// copies: changing what one call returned changes nothing for the next
		s.Min.(Decimal).Unscaled.SetInt64(1000)
		summ[1].Min.([]byte)[0] = 99
		again, _ := ds.FooterSummary(bg)
		if again[0].Min.(Decimal).Unscaled.Cmp(big.NewInt(-5)) != 0 || again[1].Min.([]byte)[0] != 0 {
			t.Errorf("the cache changed: %v %v", again[0].Min, again[1].Min)
		}
	}
}

// New files get the umask's permissions; an existing target keeps its own.
func TestExportUmask(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "s.parquet")
	writeInts(t, src, 1)
	ds := openT(t, src)
	for _, c := range []struct {
		umask int
		want  os.FileMode
	}{{0o077, 0o600}, {0o002, 0o664}} {
		old := syscall.Umask(c.umask)
		out := filepath.Join(dir, fmt.Sprintf("u%o.csv", c.umask))
		_, err := ds.Export(bg, View{}, out, ExportCSV, nil)
		syscall.Umask(old)
		if err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Stat(out); fi.Mode().Perm() != c.want {
			t.Errorf("umask %o: mode %v", c.umask, fi.Mode())
		}
	}
}

// A long target name works; a directory or a read-only target is refused,
// by its name.
func TestExportTargets(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "s.parquet")
	writeInts(t, src, 1)
	ds := openT(t, src)
	long := filepath.Join(dir, strings.Repeat("n", 240)+".csv")
	if _, err := ds.Export(bg, View{}, long, ExportCSV, nil); err != nil {
		t.Errorf("a 244-character name: %v", err)
	}
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	if _, err := ds.Export(bg, View{}, sub, ExportCSV, nil); err == nil || !strings.Contains(err.Error(), sub+" is a directory") {
		t.Errorf("a directory: %v", err)
	}
	ro := filepath.Join(dir, "ro.csv")
	os.WriteFile(ro, []byte("keep"), 0o444)
	old := writable
	writable = func(p string) bool { return p != ro && old(p) }
	defer func() { writable = old }()
	if _, err := ds.Export(bg, View{}, ro, ExportCSV, nil); err == nil || !strings.Contains(err.Error(), ro+" is read-only") {
		t.Errorf("read-only: %v", err)
	}
	if b, _ := os.ReadFile(ro); string(b) != "keep" {
		t.Errorf("ro.csv: %q", b)
	}
	if got := dirNames(t, dir); len(got) != 4 {
		t.Errorf("directory: %v", got)
	}
}

// A constant column at the largest float64 widens downward; given x-y
// limits that hold nothing give an empty grid, as in pqx.
func TestBinsEdgeRanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "max.parquet")
	m := []float64{math.MaxFloat64, math.MaxFloat64}
	writeFloats(t, path, 10, []string{"x", "y"}, m, []float64{-math.MaxFloat64, -math.MaxFloat64})
	ds := openT(t, path)
	for _, col := range []string{"x", "y"} {
		h, err := ds.Histogram(bg, View{}, col, HistOptions{Bins: 4})
		if err != nil || sum(h.Counts) != 2 || math.IsInf(h.Edges[0], 0) || math.IsInf(h.Edges[4], 0) {
			t.Errorf("%s: %v %v %v", col, h.Edges, h.Counts, err)
		}
	}
	// (x-y limits leave out their upper ends, so x, at the top of its
	// widened range, isn't counted, as pqx leaves out every column's maximum)
	g, err := ds.XYCounts(bg, View{}, "x", "y", 3, 3, Sample{}, nil, nil)
	if err != nil || g.X[1] != math.MaxFloat64 || !(g.X[0] < g.X[1]) || g.Y[0] != -math.MaxFloat64 || !(g.Y[1] > g.Y[0]) {
		t.Errorf("xy: %v %v %v %v", g.X, g.Y, g.Counts, err)
	}
	for _, l := range [][2]float64{{5, 5}, {10, 0}} {
		g, err := ds.XYCounts(bg, View{}, "x", "y", 3, 2, Sample{}, &l, nil)
		if err != nil || g.X != l || len(g.Counts) != 2 || len(g.Counts[0]) != 3 || g.Counts[0][0] != 0 {
			t.Errorf("x limits %v: %+v %v", l, g, err)
		}
	}
}
