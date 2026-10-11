package data

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// countScans counts footer scans for the test, and holds each until gate
// is closed (if gate isn't nil).
func countScans(t *testing.T, gate chan struct{}) *atomic.Int64 {
	var n atomic.Int64
	footerScanHook = func() {
		n.Add(1)
		if gate != nil {
			<-gate
		}
	}
	t.Cleanup(func() { footerScanHook = nil })
	return &n
}

// Ported from tests/test_startup.py::test_summary_computed_once.
func TestFooterSummaryComputedOnce(t *testing.T) {
	_, ds := demoDataset(t)
	gate := make(chan struct{})
	n := countScans(t, gate)
	var wg sync.WaitGroup
	results := make([]any, 3)
	for i := range 3 {
		wg.Go(func() {
			if i == 1 {
				r, err := ds.RowGroupInfo(bg)
				if err != nil {
					t.Error(err)
				}
				results[i] = len(r)
				return
			}
			s, err := ds.FooterSummary(bg)
			if err != nil {
				t.Error(err)
			}
			results[i] = s
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	ds.FooterSummary(bg)
	ds.RowGroupInfo(bg)
	if n.Load() != 1 {
		t.Errorf("%d scans", n.Load())
	}
	if !reflect.DeepEqual(results[0], results[2]) || results[1] != demoRows/demoGroup {
		t.Errorf("results differ: %v", results[1])
	}
}

// Ported from tests/test_startup.py::test_footer_scan_stops_and_starts_over.
func TestFooterScanStopsAndStartsOver(t *testing.T) {
	_, ds := demoDataset(t)
	ctx, cancel := context.WithCancel(bg)
	n := countScans(t, nil)
	footerScanHook = func() { n.Add(1); cancel() } // stopped once it has started
	if _, err := ds.FooterSummary(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped scan: %v", err)
	}
	if ds.an.footer != nil || ds.an.scanning != nil {
		t.Fatal("a stopped scan left a result")
	}
	summ, err := ds.FooterSummary(bg)
	if err != nil || len(summ) != len(ds.Columns()) || n.Load() != 2 {
		t.Fatalf("after a stop: %d paths, %v, %d scans", len(summ), err, n.Load())
	}
}

// A call waiting for another's scan gives up when its ctx is done; the scan
// goes on and is cached.
func TestFooterWaiterCancelled(t *testing.T) {
	_, ds := demoDataset(t)
	gate := make(chan struct{})
	n := countScans(t, gate)
	done := make(chan error)
	go func() { _, err := ds.FooterSummary(bg); done <- err }()
	for n.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(bg, 20*time.Millisecond)
	defer cancel()
	if _, err := ds.RowGroupInfo(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiter: %v", err)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := ds.RowGroupInfo(bg); err != nil || n.Load() != 1 {
		t.Errorf("%v, %d scans", err, n.Load())
	}
}

// Ported from tests/test_data.py::test_metadata, and
// tests/test_startup.py::test_summary_matches_reference's sizes and
// encodings, against arrow-go's own reading of the footer.
func TestFooterSummary(t *testing.T) {
	dm, ds := demoDataset(t)
	summ, err := ds.FooterSummary(bg)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]ChunkSummary{}
	for _, s := range summ {
		byPath[s.Path] = s
	}
	if len(summ) != 8 {
		t.Fatalf("%d leaf paths", len(summ))
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, d := range dm.dec {
		if !math.IsNaN(d) {
			lo, hi = math.Min(lo, d), math.Max(hi, d)
		}
	}
	if s := byPath["dec"]; s.Min != lo || s.Max != hi || s.Physical != "DOUBLE" || s.Logical != "None" || !s.HasStats || s.Compression != "SNAPPY" {
		t.Errorf("dec: %+v (want %v to %v)", s, lo, hi)
	}
	var nulls int64
	for _, n := range dm.magNull {
		if n {
			nulls++
		}
	}
	if s := byPath["mag"]; s.Nulls != nulls {
		t.Errorf("mag: %d nulls, want %d", s.Nulls, nulls)
	}
	if s := byPath["band"]; s.Min != "g" || s.Max != "z" || s.Logical != "String" || s.Physical != "BYTE_ARRAY" {
		t.Errorf("band: %+v", s)
	}
	if s := byPath["ingestTime"]; s.Min != (Timestamp{T: dm.ingest[0], Zoned: true, Unit: time.Microsecond}) || s.Max.(Timestamp).T != dm.ingest[dm.n-1] {
		t.Errorf("ingestTime: %+v", s)
	}

	// sizes, null counts and encodings as arrow-go's API reads them
	md := ds.md
	rgComp := make([]int64, md.NumRowGroups())
	for i, s := range summ {
		var comp, unc, nulls int64
		encs := map[string]bool{}
		for rg := range md.NumRowGroups() {
			c, err := md.RowGroup(rg).ColumnChunk(i)
			if err != nil {
				t.Fatal(err)
			}
			comp += c.TotalCompressedSize()
			unc += c.TotalUncompressedSize()
			rgComp[rg] += c.TotalCompressedSize()
			st, err := c.Statistics()
			if err != nil || st == nil {
				t.Fatalf("%s: no statistics (%v)", s.Path, err)
			}
			nulls += st.NullCount()
			for _, e := range c.Encodings() {
				encs[e.String()] = true
			}
		}
		if s.Compressed != comp || s.Uncompressed != unc || s.Nulls != nulls {
			t.Errorf("%s: %+v, arrow-go %d %d %d", s.Path, s, comp, unc, nulls)
		}
		if got := ds.Encodings(s.Path); len(got) != len(encs) || !encs[got[0]] {
			t.Errorf("%s: encodings %v, arrow-go %v", s.Path, got, encs)
		}
	}
	rgs, err := ds.RowGroupInfo(bg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rgs) != 8 || rgs[1].Start != 2500 || rgs[7].Rows != 2500 {
		t.Fatalf("row groups %+v", rgs)
	}
	for i, r := range rgs {
		if r.Index != i || r.Compressed != rgComp[i] || r.Uncompressed != md.RowGroup(i).TotalByteSize() {
			t.Errorf("row group %d: %+v", i, r)
		}
	}
	if len(ds.Encodings("no such path")) != 0 {
		t.Error("encodings of a missing path")
	}
}

// Statistics decoded as their logical types: dates, times, timestamps,
// decimals, unsigned integers, fixed-size binary, booleans, float32, and
// leaves of nested columns.
func TestFooterSummaryLogicalTypes(t *testing.T) {
	mem := memory.DefaultAllocator
	sc := arrow.NewSchema([]arrow.Field{
		{Name: "u32", Type: arrow.PrimitiveTypes.Uint32},
		{Name: "u64", Type: arrow.PrimitiveTypes.Uint64},
		{Name: "i8", Type: arrow.PrimitiveTypes.Int8},
		{Name: "date", Type: arrow.FixedWidthTypes.Date32},
		{Name: "t_ms", Type: arrow.FixedWidthTypes.Time32ms},
		{Name: "ts_ns", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}},
		{Name: "ts_ms", Type: &arrow.TimestampType{Unit: arrow.Millisecond, TimeZone: "UTC"}},
		{Name: "dec", Type: &arrow.Decimal128Type{Precision: 20, Scale: 3}},
		{Name: "fbin", Type: &arrow.FixedSizeBinaryType{ByteWidth: 3}},
		{Name: "b", Type: arrow.FixedWidthTypes.Boolean},
		{Name: "f32", Type: arrow.PrimitiveTypes.Float32},
		{Name: "l", Type: arrow.ListOf(arrow.PrimitiveTypes.Int32)},
	}, nil)
	b := array.NewRecordBuilder(mem, sc)
	defer b.Release()
	const n = 50
	base := time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)
	for i := range n {
		b.Field(0).(*array.Uint32Builder).Append(uint32(math.MaxUint32 - i))
		b.Field(1).(*array.Uint64Builder).Append(uint64(1)<<63 + uint64(i))
		b.Field(2).(*array.Int8Builder).Append(int8(i - 20))
		b.Field(3).(*array.Date32Builder).Append(arrow.Date32(19000 + i))
		b.Field(4).(*array.Time32Builder).Append(arrow.Time32(1000 * i))
		b.Field(5).(*array.TimestampBuilder).Append(arrow.Timestamp(base.UnixNano() + int64(i)))
		b.Field(6).(*array.TimestampBuilder).Append(arrow.Timestamp(base.UnixMilli() - int64(i)))
		b.Field(7).(*array.Decimal128Builder).Append(decimal128.FromI64(int64(i*1000 - 7)))
		b.Field(8).(*array.FixedSizeBinaryBuilder).Append([]byte{byte(i), 0xff, byte(i)})
		b.Field(9).(*array.BooleanBuilder).Append(true)
		b.Field(10).(*array.Float32Builder).Append(float32(i) / 4)
		lb := b.Field(11).(*array.ListBuilder)
		lb.Append(true)
		lb.ValueBuilder().(*array.Int32Builder).Append(int32(-i))
	}
	rec := b.NewRecordBatch()
	defer rec.Release()
	tbl := array.NewTableFromRecords(sc, []arrow.RecordBatch{rec})
	defer tbl.Release()
	path := filepath.Join(t.TempDir(), "types.parquet")
	writeTable(t, path, tbl, 20, 1<<20)
	dsI, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer dsI.Close()
	summ, err := dsI.FooterSummary(bg)
	if err != nil {
		t.Fatal(err)
	}
	dec := func(v int64) Decimal { return Decimal{Unscaled: big.NewInt(v), Scale: 3, Precision: 20} }
	want := map[string][2]Value{
		"u32":            {uint64(math.MaxUint32 - n + 1), uint64(math.MaxUint32)},
		"u64":            {uint64(1 << 63), uint64(1<<63 + n - 1)},
		"i8":             {int64(-20), int64(n - 21)},
		"date":           {Date(19000), Date(19000 + n - 1)},
		"t_ms":           {TimeOfDay(0), TimeOfDay(int64(n-1) * 1e9)},
		"ts_ns":          {Timestamp{T: base, Unit: time.Nanosecond}, Timestamp{T: base.Add(n - 1), Unit: time.Nanosecond}},
		"ts_ms":          {Timestamp{T: base.Add(-(n - 1) * time.Millisecond), Zoned: true, Unit: time.Millisecond}, Timestamp{T: base, Zoned: true, Unit: time.Millisecond}},
		"dec":            {dec(-7), dec((n-1)*1000 - 7)},
		"fbin":           {[]byte{0, 0xff, 0}, []byte{n - 1, 0xff, n - 1}},
		"b":              {true, true},
		"f32":            {float32(math.Copysign(0, -1)), float32(n-1) / 4}, // writers store a zero minimum as -0
		"l.list.element": {int64(-(n - 1)), int64(0)},
	}
	if len(summ) != len(want) {
		t.Fatalf("%d paths: %+v", len(summ), summ)
	}
	for _, s := range summ {
		w, ok := want[s.Path]
		if !ok {
			t.Errorf("unexpected path %s", s.Path)
			continue
		}
		if fmt.Sprint(s.Min) != fmt.Sprint(w[0]) || fmt.Sprint(s.Max) != fmt.Sprint(w[1]) || !s.HasStats {
			t.Errorf("%s: %#v to %#v, want %#v to %#v", s.Path, s.Min, s.Max, w[0], w[1])
		}
	}
}

func TestInfo(t *testing.T) {
	_, ds := demoDataset(t)
	fi := ds.Info()
	raw, err := os.ReadFile(ds.path)
	if err != nil {
		t.Fatal(err)
	}
	footer := int64(binary.LittleEndian.Uint32(raw[len(raw)-8:]))
	if fi.Path != ds.path || fi.Size != int64(len(raw)) || fi.FooterSize != footer || fi.NumLeaves != 8 || fi.CreatedBy == "" {
		t.Errorf("info %+v (footer %d)", fi, footer)
	}
	if fi.FormatVersion != "2.6" && fi.FormatVersion != "1.0" {
		t.Errorf("format version %q", fi.FormatVersion)
	}
	rgs, _ := ds.RowGroupInfo(bg)
	var comp, unc int64
	for _, r := range rgs {
		comp += r.Compressed
		unc += r.Uncompressed
	}
	if fi.Compressed != comp || fi.Uncompressed != unc {
		t.Errorf("sizes %d %d, row groups %d %d", fi.Compressed, fi.Uncompressed, comp, unc)
	}
}

// BenchmarkFooterSummary scans the footer of a file of 2,000 row groups of
// 20 columns (40,000 chunks); pqx takes about 2.5 s per million chunks.
func BenchmarkFooterSummary(b *testing.B) {
	mem := memory.DefaultAllocator
	fields := make([]arrow.Field, 20)
	for i := range fields {
		typ := arrow.DataType(arrow.PrimitiveTypes.Float64)
		switch i % 4 {
		case 1:
			typ = arrow.PrimitiveTypes.Int64
		case 2:
			typ = arrow.BinaryTypes.String
		case 3:
			typ = &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}
		}
		fields[i] = arrow.Field{Name: fmt.Sprintf("c%d", i), Type: typ, Nullable: true}
	}
	sc := arrow.NewSchema(fields, nil)
	bld := array.NewRecordBuilder(mem, sc)
	const rows, group = 20_000, 10
	for r := range rows {
		for i := range fields {
			switch f := bld.Field(i).(type) {
			case *array.Float64Builder:
				f.Append(float64(r) / 3)
			case *array.Int64Builder:
				f.Append(int64(r))
			case *array.StringBuilder:
				f.Append(fmt.Sprintf("s%06d", r))
			case *array.TimestampBuilder:
				f.Append(arrow.Timestamp(int64(r) * 1e6))
			}
		}
	}
	rec := bld.NewRecordBatch()
	tbl := array.NewTableFromRecords(sc, []arrow.RecordBatch{rec})
	path := filepath.Join(b.TempDir(), "many.parquet")
	writeTable(b, path, tbl, group, 1<<20)
	tbl.Release()
	rec.Release()
	bld.Release()
	ds, err := Open(path, Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer ds.Close()
	d := ds.(*dataset)
	b.ResetTimer()
	for b.Loop() {
		if _, err := d.scanFooter(bg); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(rows/group*len(fields)), "ns/chunk")
}
