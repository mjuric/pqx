package data

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

var bg = context.Background()

func relClose(a, b float64) bool {
	return a == b || math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b))
}

// Ported from tests/test_data.py::test_column_stats.
func TestColumnStats(t *testing.T) {
	dm, ds := demoDataset(t)

	st, err := ds.ColumnStats(bg, View{}, "mag", Sample{})
	if err != nil {
		t.Fatal(err)
	}
	var nulls int64
	var sum float64
	lo, hi := math.Inf(1), math.Inf(-1)
	for i, m := range dm.mag {
		if dm.magNull[i] {
			nulls++
			continue
		}
		sum += m
		lo, hi = math.Min(lo, m), math.Max(hi, m)
	}
	nonNull := int64(dm.n) - nulls
	if st.Count != int64(dm.n) || st.Nulls != nulls || st.NaNs != 0 || st.Sampled {
		t.Errorf("mag: count %d nulls %d nans %d sampled %v", st.Count, st.Nulls, st.NaNs, st.Sampled)
	}
	if st.Min != lo || st.Max != hi {
		t.Errorf("mag: min %v max %v, want %v %v", st.Min, st.Max, lo, hi)
	}
	if st.Mean == nil || !relClose(*st.Mean, sum/float64(nonNull)) || st.Std == nil {
		t.Errorf("mag: mean %v, want %v", st.Mean, sum/float64(nonNull))
	}
	if len(st.Quantiles) != 7 || st.Quantiles[0.25] < lo || st.Quantiles[0.25] > hi {
		t.Errorf("mag: quantiles %v", st.Quantiles)
	}
	if st.Top != nil || st.DistinctExact || st.Distinct < nonNull*9/10 || st.Distinct > nonNull {
		t.Errorf("mag (near unique): distinct %d exact %v top %v", st.Distinct, st.DistinctExact, st.Top)
	}

	// NaNs are counted and left out; inf stays in min/max but not in the mean
	st, err = ds.ColumnStats(bg, View{}, "psfFlux", Sample{})
	if err != nil {
		t.Fatal(err)
	}
	var nans, finite int64
	sum, lo = 0, math.Inf(1)
	for _, f := range dm.flux {
		switch {
		case math.IsNaN(f):
			nans++
		case !math.IsInf(f, 0):
			finite++
			sum += f
			lo = math.Min(lo, f)
		}
	}
	if st.NaNs != nans || st.Max != math.Inf(1) || st.Min != lo || st.Nulls != 0 {
		t.Errorf("psfFlux: nans %d (want %d) min %v (want %v) max %v", st.NaNs, nans, st.Min, lo, st.Max)
	}
	if st.Mean == nil || !relClose(*st.Mean, sum/float64(finite)) {
		t.Errorf("psfFlux: mean %v, want %v", *st.Mean, sum/float64(finite))
	}

	// a short distribution: the whole of it, exactly
	counts := map[string]int64{}
	for _, b := range dm.band {
		counts[b]++
	}
	for _, v := range []View{{}, {SQL: "SELECT * FROM t"}} {
		st, err = ds.ColumnStats(bg, v, "band", Sample{})
		if err != nil {
			t.Fatal(err)
		}
		if st.Distinct != 6 || !st.DistinctExact || len(st.Top) != 6 || st.NaNs != -1 || st.Mean != nil || st.Quantiles != nil {
			t.Fatalf("band (%+v): %+v", v, st)
		}
		for i, tc := range st.Top {
			s, _ := tc.Value.(string)
			if tc.Count != counts[s] {
				t.Errorf("band %q: %d, want %d", s, tc.Count, counts[s])
			}
			if i > 0 && (st.Top[i-1].Count < tc.Count || st.Top[i-1].Count == tc.Count && st.Top[i-1].Value.(string) > s) {
				t.Errorf("top not in order: %v", st.Top)
			}
		}
		if st.Min != "g" || st.Max != "z" {
			t.Errorf("band: min %v max %v", st.Min, st.Max)
		}
	}
	st, err = ds.ColumnStats(bg, View{Where: "band = 'g'"}, "band", Sample{})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Top) != 1 || st.Top[0].Value != "g" || st.Top[0].Count != counts["g"] || st.Count != counts["g"] {
		t.Errorf("band = 'g': %+v", st)
	}

	// integers: no NaN count; quantiles as floats
	st, err = ds.ColumnStats(bg, View{}, "diaSourceId", Sample{})
	if err != nil {
		t.Fatal(err)
	}
	if st.NaNs != -1 || st.Min != int64(0) || st.Max != int64(dm.n-1) || *st.Mean != float64(dm.n-1)/2 || len(st.Quantiles) != 7 {
		t.Errorf("diaSourceId: %+v", st)
	}
}

// Ported from tests/test_data.py::test_timestamps_in_utc.
func TestStatsTimestampsInUTC(t *testing.T) {
	dm, ds := demoDataset(t)
	st, err := ds.ColumnStats(bg, View{Where: "mag < 21"}, "ingestTime", Sample{})
	if err != nil {
		t.Fatal(err)
	}
	first := -1
	for i, m := range dm.mag {
		if !dm.magNull[i] && m < 21 {
			first = i
			break
		}
	}
	ts, ok := st.Min.(Timestamp)
	if !ok || !ts.Zoned || ts.T.Location() != time.UTC || !ts.T.Equal(dm.ingest[first]) || ts.Unit != time.Microsecond {
		t.Errorf("min %#v, want %v", st.Min, dm.ingest[first])
	}
}

// Ported from tests/test_data.py::test_odd_file (its stats).
func TestColumnStatsOddTypes(t *testing.T) {
	mem := memory.DefaultAllocator
	sc := arrow.NewSchema([]arrow.Field{
		{Name: "file_row_number", Type: arrow.PrimitiveTypes.Int64},
		{Name: "x", Type: arrow.PrimitiveTypes.Float64},
		{Name: "tags", Type: arrow.ListOf(arrow.BinaryTypes.String), Nullable: true},
		{Name: "pos", Type: arrow.StructOf(arrow.Field{Name: "ra", Type: arrow.PrimitiveTypes.Float64})},
		{Name: "blob", Type: arrow.BinaryTypes.Binary},
		{Name: "flag", Type: arrow.FixedWidthTypes.Boolean, Nullable: true},
		{Name: "allnull", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		{Name: "dec", Type: &arrow.Decimal128Type{Precision: 10, Scale: 2}},
	}, nil)
	b := array.NewRecordBuilder(mem, sc)
	defer b.Release()
	const n = 1000
	for i := range n {
		b.Field(0).(*array.Int64Builder).Append(int64(i * 10))
		x := float64(i%100) / 7
		if i%50 == 0 {
			x = math.NaN()
		}
		if i == 1 {
			x = math.Inf(1)
		}
		b.Field(1).(*array.Float64Builder).Append(x)
		lb := b.Field(2).(*array.ListBuilder)
		lb.Append(true)
		for j := range i % 3 {
			lb.ValueBuilder().(*array.StringBuilder).Append([]string{"a", "b"}[j])
		}
		sb := b.Field(3).(*array.StructBuilder)
		sb.Append(true)
		sb.FieldBuilder(0).(*array.Float64Builder).Append(float64(i))
		b.Field(4).(*array.BinaryBuilder).Append([]byte(strings.Repeat("\x01", i%20)))
		if i%3 == 0 {
			b.Field(5).AppendNull()
		} else {
			b.Field(5).(*array.BooleanBuilder).Append(i%3 == 1)
		}
		b.Field(6).AppendNull()
		b.Field(7).(*array.Decimal128Builder).Append(decimalOf(int64(i)))
	}
	rec := b.NewRecordBatch()
	defer rec.Release()
	tbl := array.NewTableFromRecords(sc, []arrow.RecordBatch{rec})
	defer tbl.Release()
	path := filepath.Join(t.TempDir(), "odd.parquet")
	writeTable(t, path, tbl, 300, 1<<20)
	dsI, err := Open(path, Options{Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer dsI.Close()
	ds := dsI.(*dataset)

	st, err := ds.ColumnStats(bg, View{}, "x", Sample{})
	if err != nil {
		t.Fatal(err)
	}
	if st.NaNs != 20 || st.Max != math.Inf(1) || st.Min != 1.0/7 {
		t.Errorf("x: %+v", st)
	}
	// sampling needs the row number; this file has a column of that name
	st, err = ds.ColumnStats(bg, View{}, "x", Sample{Rows: 10})
	if err != nil || st.Count != n || !st.Sampled {
		t.Errorf("x sampled: %+v %v", st, err)
	}
	for _, col := range []string{"tags", "pos"} {
		st, err := ds.ColumnStats(bg, View{}, col, Sample{})
		if err != nil {
			t.Fatal(col, err)
		}
		if st.Distinct != -1 || st.Min != nil || st.Max != nil || st.Top != nil || st.Count != n {
			t.Errorf("%s: %+v", col, st)
		}
	}
	st, err = ds.ColumnStats(bg, View{}, "flag", Sample{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Distinct != 2 || !st.DistinctExact || len(st.Top) != 3 || st.Nulls != 334 || st.Min != false || st.Max != true {
		t.Errorf("flag: %+v", st)
	}
	st, err = ds.ColumnStats(bg, View{}, "blob", Sample{})
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := st.Max.([]byte); !ok || len(b) != 19 || st.Distinct < 15 || st.Distinct > 20 || len(st.Top) != 10 || st.DistinctExact {
		t.Errorf("blob: %+v", st)
	}
	st, err = ds.ColumnStats(bg, View{}, "allnull", Sample{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Nulls != n || st.Min != nil || st.Mean != nil || st.Distinct != 0 || !st.DistinctExact || len(st.Top) != 1 || st.Top[0].Value != nil {
		t.Errorf("allnull: %+v", st)
	}
	// decimals: exact min and max, no mean or quantiles (pqx: numeric, but not for those)
	st, err = ds.ColumnStats(bg, View{}, "dec", Sample{})
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := st.Max.(Decimal); !ok || d.Unscaled.Int64() != 999 || d.Scale != 2 || st.Mean != nil || st.Quantiles != nil || st.NaNs != -1 {
		t.Errorf("dec: %+v", st)
	}
	if _, err := ds.ColumnStats(bg, View{}, "nope", Sample{}); err == nil {
		t.Error("stats of a missing column: no error")
	}
}

// Ported from tests/test_data.py::test_sampling and
// tests/test_startup.py::test_sampling_does_not_scan_the_footer.
func TestSampling(t *testing.T) {
	dm, ds := demoDataset(t)
	// pqx's conditions on the same layout (8 row groups of 2,500), its row
	// number renamed
	want := map[[2]int64]string{
		{5000, 16}:  "((__pqx_row >= 0 AND __pqx_row < 625) OR (__pqx_row >= 2500 AND __pqx_row < 3125) OR (__pqx_row >= 5000 AND __pqx_row < 5625) OR (__pqx_row >= 7500 AND __pqx_row < 8125) OR (__pqx_row >= 10000 AND __pqx_row < 10625) OR (__pqx_row >= 12500 AND __pqx_row < 13125) OR (__pqx_row >= 15000 AND __pqx_row < 15625) OR (__pqx_row >= 17500 AND __pqx_row < 18125))",
		{3000, 4}:   "((__pqx_row >= 0 AND __pqx_row < 750) OR (__pqx_row >= 5000 AND __pqx_row < 5750) OR (__pqx_row >= 12500 AND __pqx_row < 13250) OR (__pqx_row >= 17500 AND __pqx_row < 18250))",
		{19999, 16}: "((__pqx_row >= 0 AND __pqx_row < 2500) OR (__pqx_row >= 2500 AND __pqx_row < 5000) OR (__pqx_row >= 5000 AND __pqx_row < 7500) OR (__pqx_row >= 7500 AND __pqx_row < 10000) OR (__pqx_row >= 10000 AND __pqx_row < 12500) OR (__pqx_row >= 12500 AND __pqx_row < 15000) OR (__pqx_row >= 15000 AND __pqx_row < 17500) OR (__pqx_row >= 17500 AND __pqx_row < 20000))",
		// 3 picks of 8 row groups: round(3.5) is 4 (half to even)
		{300, 3}:    "((__pqx_row >= 0 AND __pqx_row < 100) OR (__pqx_row >= 10000 AND __pqx_row < 10100) OR (__pqx_row >= 17500 AND __pqx_row < 17600))",
		{20000, 16}: "",
		{0, 16}:     "",
	}
	for k, w := range want {
		got := strings.ReplaceAll(ds.sampleCondition(k[0], int(k[1])), "file_row_number", "__pqx_row")
		if got != w {
			t.Errorf("sampleCondition(%d, %d) = %s\nwant %s", k[0], k[1], got, w)
		}
	}
	// a half that rounds down: 6 row groups, 3 picks at 0, 2.5 → 2, 5
	d6 := &dataset{numRows: 60, hasRowNum: true, rgRows: []int64{10, 10, 10, 10, 10, 10}, rgStart: []int64{0, 10, 20, 30, 40, 50, 60}}
	if got := d6.sampleCondition(3, 3); !strings.Contains(got, ">= 20 AND") || strings.Contains(got, ">= 30 AND") {
		t.Errorf("6 row groups, 3 slices: %s", got)
	}

	st, err := ds.ColumnStats(bg, View{}, "mag", Sample{Rows: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Sampled || st.Count != 5000 {
		t.Errorf("sampled: %v count %d", st.Sampled, st.Count)
	}
	st, err = ds.ColumnStats(bg, View{SQL: "SELECT mag FROM t WHERE band <> 'u'"}, "mag", Sample{Rows: 3000})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Sampled || st.Count != 3000 {
		t.Errorf("SQL sampled: %v count %d", st.Sampled, st.Count)
	}
	h, err := ds.Histogram(bg, View{Where: "band = 'r'"}, "mag", HistOptions{Sample: Sample{Rows: 5000}})
	if err != nil {
		t.Fatal(err)
	}
	var want2 int64
	for i := range dm.n {
		if i%2500 < 625 && dm.band[i] == "r" && !dm.magNull[i] {
			want2++
		}
	}
	if s := sum(h.Counts); s != want2 {
		t.Errorf("sampled, filtered histogram: %d rows, want %d", s, want2)
	}
	if ds.an.footer != nil {
		t.Error("sampling scanned the footer")
	}
}

func sum(xs []int64) int64 {
	var s int64
	for _, x := range xs {
		s += x
	}
	return s
}

// binOf is pqx's bin for v: floor((v - lo) / w), clipped to the bins.
func binOf(v, lo, w float64, bins int) int {
	return min(max(int(math.Floor((v-lo)/w)), 0), bins-1)
}

// Ported from tests/test_data.py::test_histogram, against brute force.
func TestHistogram(t *testing.T) {
	dm, ds := demoDataset(t)
	h, err := ds.Histogram(bg, View{}, "mag", HistOptions{Bins: 20})
	if err != nil {
		t.Fatal(err)
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for i, m := range dm.mag {
		if !dm.magNull[i] {
			lo, hi = math.Min(lo, m), math.Max(hi, m)
		}
	}
	check := func(name string, h Histogram, vals []float64, lo, hi float64, bins int) {
		t.Helper()
		if len(h.Edges) != bins+1 || len(h.Counts) != bins {
			t.Fatalf("%s: %d edges, %d counts", name, len(h.Edges), len(h.Counts))
		}
		w := (hi - lo) / float64(bins)
		want := make([]int64, bins)
		for _, v := range vals {
			if !math.IsNaN(v) && !math.IsInf(v, 0) && v >= lo && v <= hi {
				want[binOf(v, lo, w, bins)]++
			}
		}
		for i, e := range h.Edges {
			if want := lo + float64(float64(i)*w); e != want { // (unfused, as Python)
				t.Errorf("%s: edge %d = %v, want %v", name, i, e, want)
			}
		}
		for i := range want {
			if h.Counts[i] != want[i] {
				t.Errorf("%s: counts %v\nwant %v", name, h.Counts, want)
				break
			}
		}
	}
	check("mag", h, dm.mag, lo, hi, 20)

	l, u := 18.0, 22.5
	h, err = ds.Histogram(bg, View{}, "mag", HistOptions{Lo: &l, Hi: &u})
	if err != nil {
		t.Fatal(err)
	}
	check("mag 18–22.5", h, dm.mag, l, u, 40)

	// temporal: epoch seconds
	h, err = ds.Histogram(bg, View{}, "ingestTime", HistOptions{Bins: 10, Temporal: true})
	if err != nil {
		t.Fatal(err)
	}
	secs := make([]float64, dm.n)
	for i, ts := range dm.ingest {
		secs[i] = float64(ts.UnixMicro()) / 1e6
	}
	check("ingestTime", h, secs, secs[0], secs[dm.n-1], 10)
	if sum(h.Counts) != int64(dm.n) {
		t.Errorf("ingestTime: %d rows", sum(h.Counts))
	}

	// log: only the positive values
	h, err = ds.Histogram(bg, View{}, "psfFlux", HistOptions{Bins: 10, Log: true})
	if err != nil {
		t.Fatal(err)
	}
	var pos int64
	for _, f := range dm.flux {
		if f > 0 && !math.IsInf(f, 0) {
			pos++
		}
	}
	if sum(h.Counts) != pos {
		t.Errorf("log psfFlux: %d rows, want %d", sum(h.Counts), pos)
	}

	// one value: a range of 1 from it; nothing: no histogram
	h, err = ds.Histogram(bg, View{Where: "diaSourceId = 3"}, "mag", HistOptions{Bins: 4})
	if err != nil {
		t.Fatal(err)
	}
	if h.Edges[0] != dm.mag[3] || h.Edges[4] != dm.mag[3]+1 || h.Counts[0] != 1 || sum(h.Counts) != 1 {
		t.Errorf("one value: %+v", h)
	}
	h, err = ds.Histogram(bg, View{Where: "false"}, "mag", HistOptions{})
	if err != nil || h.Edges != nil || h.Counts != nil {
		t.Errorf("no values: %+v %v", h, err)
	}
	nan := math.NaN()
	if _, err := ds.Histogram(bg, View{}, "mag", HistOptions{Lo: &nan, Hi: &u}); err == nil {
		t.Error("NaN limit: no error")
	}
}

// Ported from tests/test_data.py::test_sky_and_xy_counts, against brute force.
func TestSkyCounts(t *testing.T) {
	dm, ds := demoDataset(t)
	for _, res := range []float64{1, 0.7, 2.5} {
		g, err := ds.SkyCounts(bg, View{}, "ra", "dec", res, Sample{})
		if err != nil {
			t.Fatal(err)
		}
		nlon, nlat := int(math.RoundToEven(360/res)), int(math.RoundToEven(180/res))
		if len(g.Counts) != nlat || len(g.Counts[0]) != nlon || g.X != [2]float64{0, 360} || g.Y != [2]float64{-90, 90} {
			t.Fatalf("res %v: %d × %d", res, len(g.Counts), len(g.Counts[0]))
		}
		want := newGrid(nlat, nlon)
		var n int64
		for k := range dm.n {
			a, d := dm.ra[k], dm.dec[k]
			if math.IsNaN(a) || math.IsNaN(d) || d < -90 || d > 90 {
				continue
			}
			a = math.Mod(math.Mod(a, 360)+360, 360)
			i := min(int(math.Floor(a/res)), nlon-1)
			j := min(int(math.Floor((d+90)/res)), nlat-1)
			want[j][i]++
			n++
		}
		for j := range want {
			for i := range want[j] {
				if g.Counts[j][i] != want[j][i] {
					t.Fatalf("res %v: cell (%d, %d) = %d, want %d", res, j, i, g.Counts[j][i], want[j][i])
				}
			}
		}
		if n != int64(dm.n-2) {
			t.Errorf("res %v: %d positions on the sky", res, n)
		}
	}
	g, err := ds.SkyCounts(bg, View{Where: "dec > 0"}, "ra", "dec", 1, Sample{})
	if err != nil {
		t.Fatal(err)
	}
	var south, north int64
	for j, row := range g.Counts {
		for _, c := range row {
			if j < 90 {
				south += c
			} else {
				north += c
			}
		}
	}
	if south != 0 || north == 0 {
		t.Errorf("dec > 0: %d south, %d north", south, north)
	}
	if _, err := ds.SkyCounts(bg, View{}, "ra", "dec", 0, Sample{}); err == nil {
		t.Error("resolution 0: no error")
	}
}

func TestXYCounts(t *testing.T) {
	dm, ds := demoDataset(t)
	g, err := ds.XYCounts(bg, View{}, "mag", "snr", 40, 20, Sample{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, row := range g.Counts {
		total += sum(row)
	}
	if len(g.Counts) != 20 || len(g.Counts[0]) != 40 || total <= 19_000 || total > 20_000 {
		t.Errorf("robust range: %d × %d, %d rows", len(g.Counts), len(g.Counts[0]), total)
	}
	if !(g.X[0] >= 15 && g.X[1] <= 25 && g.X[0] < g.X[1]) {
		t.Errorf("x range %v", g.X)
	}

	xl, yl := [2]float64{18, 22}, [2]float64{0, 100}
	g, err = ds.XYCounts(bg, View{Where: "band <> 'y'"}, "mag", "snr", 7, 5, Sample{}, &xl, &yl)
	if err != nil {
		t.Fatal(err)
	}
	want := newGrid(5, 7)
	wx, wy := (xl[1]-xl[0])/7, (yl[1]-yl[0])/5
	for k := range dm.n {
		x, y := dm.mag[k], dm.snr[k]
		if dm.band[k] == "y" || dm.magNull[k] || !(x >= xl[0] && x < xl[1] && y >= yl[0] && y < yl[1]) {
			continue
		}
		i := min(max(int(math.Floor((x-xl[0])/wx)), 0), 6)
		j := min(max(int(math.Floor((y-yl[0])/wy)), 0), 4)
		want[j][i]++
	}
	for j := range want {
		for i := range want[j] {
			if g.Counts[j][i] != want[j][i] {
				t.Fatalf("cell (%d, %d) = %d, want %d", j, i, g.Counts[j][i], want[j][i])
			}
		}
	}
	if g.X != xl || g.Y != yl {
		t.Errorf("limits %v %v", g.X, g.Y)
	}
	g, err = ds.XYCounts(bg, View{Where: "false"}, "mag", "snr", 3, 2, Sample{}, nil, nil)
	if err != nil || len(g.Counts) != 2 || g.X != [2]float64{0, 1} || g.Y != [2]float64{0, 1} {
		t.Errorf("no rows: %+v %v", g, err)
	}
	// one value: a range of 1 from it
	g, err = ds.XYCounts(bg, View{Where: "diaSourceId = 3"}, "mag", "snr", 4, 4, Sample{}, nil, nil)
	if err != nil || g.X != [2]float64{dm.mag[3], dm.mag[3] + 1} || g.Counts[0][0] != 1 {
		t.Errorf("one row: %+v %v", g, err)
	}
}

func TestAnalysisCancel(t *testing.T) {
	_, ds := demoDataset(t)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	calls := map[string]func() error{
		"stats": func() error { _, err := ds.ColumnStats(ctx, View{}, "mag", Sample{}); return err },
		"hist":  func() error { _, err := ds.Histogram(ctx, View{}, "mag", HistOptions{}); return err },
		"sky":   func() error { _, err := ds.SkyCounts(ctx, View{}, "ra", "dec", 1, Sample{}); return err },
		"xy":    func() error { _, err := ds.XYCounts(ctx, View{}, "mag", "snr", 4, 4, Sample{}, nil, nil); return err },
		"export": func() error {
			_, err := ds.Export(ctx, View{}, filepath.Join(t.TempDir(), "x.csv"), ExportCSV, nil)
			return err
		},
		"footer": func() error { _, err := ds.FooterSummary(ctx); return err },
	}
	for name, f := range calls {
		if err := f(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPyRepr(t *testing.T) {
	// Python's repr of each
	for _, c := range []struct {
		x    float64
		want string
	}{
		{0.1, "0.1"}, {1e-05, "1e-05"}, {0.0001, "0.0001"}, {1e16, "1e+16"}, {1e15, "1000000000000000.0"},
		{123, "123.0"}, {math.Copysign(0, -1), "-0.0"}, {0, "0.0"}, {2.5e-310, "2.5e-310"},
		{math.MaxFloat64, "1.7976931348623157e+308"}, {0.30000000000000004, "0.30000000000000004"},
		{1234567890123456.8, "1234567890123456.8"}, {12345678901234568, "1.2345678901234568e+16"},
		{-1.5e-07, "-1.5e-07"}, {5, "5.0"}, {math.Inf(-1), "-inf"},
	} {
		if got := pyRepr(c.x); got != c.want {
			t.Errorf("pyRepr(%v) = %s, want %s", c.x, got, c.want)
		}
	}
}

func TestDuckClass(t *testing.T) {
	for typ, want := range map[string]typeClass{
		"DOUBLE": {numeric: true, float: true}, "UBIGINT": {numeric: true}, "HUGEINT": {numeric: true, decimal: true},
		"DECIMAL(10,2)": {numeric: true, decimal: true}, "BOOLEAN": {boolean: true}, "VARCHAR": {},
		"INTEGER[]": {nested: true}, "DOUBLE[3]": {nested: true}, "STRUCT(a INTEGER)": {nested: true},
		"MAP(VARCHAR, INTEGER)": {nested: true}, "TIMESTAMP WITH TIME ZONE": {},
	} {
		if got := duckClass(typ); got != want {
			t.Errorf("%s: %+v, want %+v", typ, got, want)
		}
	}
}

func decimalOf(v int64) decimal128.Num { return decimal128.FromI64(v) }
