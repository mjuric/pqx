package data

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/mjuric/pqx/go/internal/golden"
)

// The fixtures with golden data_<name>.json files from Python pqx.
var goldenFixtures = []string{"demo", "odd", "types", "hostile", "units", "casedup", "rowcol", "nulname"}

// openGolden opens fixture name with one DuckDB thread, as the golden files
// were made (approximate aggregates then come out the same).
func openGolden(t *testing.T, name string) (*dataset, *golden.File) {
	t.Helper()
	path := golden.Fixture(name + ".parquet")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no fixture %s", path)
	}
	ds, err := Open(path, Options{Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ds.Close() })
	return ds.(*dataset), golden.Load(t, "data_"+name+".json")
}

type goldenView struct {
	Where   string  `json:"where"`
	OrderBy [][]any `json:"order_by"`
	SQL     string  `json:"sql"`
}

func (g goldenView) view() View {
	v := View{Where: g.Where, SQL: g.SQL}
	for _, o := range g.OrderBy {
		c, _ := o[0].(string)
		desc, _ := o[1].(bool)
		v.OrderBy = append(v.OrderBy, Sort{Column: c, Desc: desc})
	}
	return v
}

// sameValue reports whether v is golden value g (as pqx's Python has it:
// timestamps to the microsecond).
func sameValue(g golden.Value, v Value) bool {
	switch g.Kind {
	case golden.KindNull:
		return v == nil
	case golden.KindInt, golden.KindUint:
		switch x := v.(type) {
		case int64:
			return g.Int.Cmp(big.NewInt(x)) == 0
		case uint64:
			return g.Int.Cmp(new(big.Int).SetUint64(x)) == 0
		}
	case golden.KindF64:
		if x, ok := v.(float32); ok { // a FLOAT that Python widened
			return float64(x) == g.Float || x != x && math.IsNaN(g.Float)
		}
		x, ok := v.(float64)
		return ok && (x == g.Float && math.Signbit(x) == math.Signbit(g.Float) || math.IsNaN(x) && math.IsNaN(g.Float))
	case golden.KindF32:
		x, ok := v.(float32)
		return ok && (x == g.Float32 || x != x && g.Float32 != g.Float32)
	case golden.KindBool:
		x, ok := v.(bool)
		return ok && x == g.Bool
	case golden.KindStr:
		x, ok := v.(string)
		return ok && x == g.Str
	case golden.KindBytes:
		x, ok := v.([]byte)
		return ok && bytes.Equal(x, g.Bytes)
	case golden.KindTS:
		x, ok := v.(Timestamp)
		return ok && x.T.Truncate(time.Microsecond).Equal(g.Time.Truncate(time.Microsecond)) && x.Zoned == (g.TZ != "")
	case golden.KindDate:
		x, ok := v.(Date)
		return ok && x.Time().Equal(g.Time)
	case golden.KindTime:
		x, ok := v.(TimeOfDay)
		return ok && int64(x)/1000 == g.Nanos/1000
	case golden.KindDec:
		x, ok := v.(Decimal)
		return ok && x.Unscaled.Cmp(g.Int) == 0 && int(x.Scale) == g.Scale
	case golden.KindUUID:
		x, ok := v.(UUID)
		h := hex.EncodeToString(x[:])
		return ok && h[:8]+"-"+h[8:12]+"-"+h[12:16]+"-"+h[16:20]+"-"+h[20:] == g.Str
	case golden.KindList:
		x, ok := v.(List)
		if !ok || len(x) != len(g.List) {
			return false
		}
		for i := range x {
			if !sameValue(g.List[i], x[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// goldenFloat is a golden number (f64, f32 or int) as a float64.
func goldenFloat(g golden.Value) (float64, bool) {
	switch g.Kind {
	case golden.KindF64, golden.KindF32:
		return g.Float, true
	case golden.KindInt, golden.KindUint:
		f, _ := new(big.Float).SetInt(g.Int).Float64()
		return f, true
	}
	return 0, false
}

func TestGoldenColumnInfo(t *testing.T) {
	for _, name := range goldenFixtures {
		t.Run(name, func(t *testing.T) {
			ds, gf := openGolden(t, name)
			cols := ds.Columns()
			recs := gf.Section(t, "columns")
			if len(recs) != len(cols) {
				t.Fatalf("%d columns, golden %d", len(cols), len(recs))
			}
			for i, r := range recs {
				if r.String(t, "name") != cols[i].Name || r.String(t, "unit") != cols[i].Unit || r.String(t, "description") != cols[i].Description {
					t.Errorf("%s: unit %q description %q, golden %q %q", r.ID(), cols[i].Unit, cols[i].Description, r.String(t, "unit"), r.String(t, "description"))
				}
			}
		})
	}
}

func TestGoldenColumnStats(t *testing.T) {
	for _, name := range goldenFixtures {
		t.Run(name, func(t *testing.T) {
			ds, gf := openGolden(t, name)
			for _, r := range gf.Section(t, "column_stats") {
				var gv goldenView
				r.Decode(t, "view", &gv)
				col := r.String(t, "column")
				var s Sample
				if !r.IsNull("sample") {
					s.Rows = int64(r.Int(t, "sample"))
				}
				st, err := ds.ColumnStats(bg, gv.view(), col, s)
				if msg, ok := r.Err(); ok {
					if strings.Contains(msg, "NUL character") {
						// pqx selects every column of the file, so a column
						// SQL can't name breaks the stats of all the others;
						// here only the column profiled is read.
						if err != nil {
							t.Errorf("%s: %v", r.ID(), err)
						}
					} else if err == nil {
						t.Errorf("%s: no error, golden %s", r.ID(), msg)
					}
					continue
				}
				if err != nil {
					t.Errorf("%s: %v", r.ID(), err)
					continue
				}
				checkStats(t, r, ds, gv.view(), col, st)
			}
		})
	}
}

func checkStats(t *testing.T, r golden.Record, ds *dataset, v View, col string, st ColumnStats) {
	t.Helper()
	var out struct {
		Count, Nulls int64
		NaNs         *int64 `json:"nans"`
		Distinct     *int64
		Min, Max     golden.Value
		Mean, Std    golden.Value
		Quantiles    [][2]json.RawMessage
		Top          [][2]json.RawMessage
		Sampled      bool
	}
	r.Decode(t, "out", &out)
	id := r.ID()
	errorf := func(format string, args ...any) { t.Errorf("%s (%s): %s", id, col, fmt.Sprintf(format, args...)) }
	if st.Count != out.Count || st.Nulls != out.Nulls || st.Sampled != out.Sampled {
		errorf("count %d nulls %d sampled %v, golden %d %d %v", st.Count, st.Nulls, st.Sampled, out.Count, out.Nulls, out.Sampled)
	}
	if !sameValue(out.Min, st.Min) || !sameValue(out.Max, st.Max) {
		errorf("min %#v max %#v, golden %s %s", st.Min, st.Max, out.Min, out.Max)
	}
	// pqx's golden SQL-view stats were made without the result's types (the
	// app passes them): a result column that isn't the file's was a string
	// to it, so it had no numeric aggregates. Here they follow the result,
	// as in the app.
	// For a file column of a different class in DuckDB (a duration is
	// BIGINT, a wide decimal DOUBLE), the same goes.
	if v.IsSQL() {
		j, fileCol := ds.byName[col]
		if !fileCol || arrowClass(ds.cols[j].Arrow) != duckClass(ds.cols[j].Type) {
			return
		}
	}
	switch {
	case out.NaNs == nil && st.NaNs != -1, out.NaNs != nil && st.NaNs != *out.NaNs:
		errorf("nans %d, golden %v", st.NaNs, out.NaNs)
	}
	if (out.Distinct == nil) != (st.Distinct < 0) || out.Distinct != nil && *out.Distinct != st.Distinct {
		errorf("distinct %d, golden %v", st.Distinct, out.Distinct)
	}
	for _, m := range []struct {
		name string
		got  *float64
		want golden.Value
	}{{"mean", st.Mean, out.Mean}, {"std", st.Std, out.Std}} {
		w, ok := goldenFloat(m.want)
		switch {
		case !ok && m.got != nil, ok && m.got == nil:
			errorf("%s %v, golden %s", m.name, m.got, m.want)
		case ok && !relClose(*m.got, w) && !(math.IsNaN(w) && math.IsNaN(*m.got)):
			errorf("%s %v, golden %v", m.name, *m.got, w)
		}
	}
	if len(st.Quantiles) != len(out.Quantiles) {
		errorf("quantiles %v, golden %v", st.Quantiles, out.Quantiles)
	}
	for _, q := range out.Quantiles {
		var k float64
		var gv golden.Value
		if err := errors.Join(json.Unmarshal(q[0], &k), json.Unmarshal(q[1], &gv)); err != nil {
			t.Fatal(err)
		}
		w, _ := goldenFloat(gv)
		if got, ok := st.Quantiles[k]; !ok || !relClose(got, w) {
			errorf("quantile %v: %v, golden %v", k, got, w)
		}
	}
	if len(st.Top) != len(out.Top) {
		errorf("top %v, golden %v", st.Top, out.Top)
		return
	}
	for i, tp := range out.Top {
		var gv golden.Value
		var n int64
		if err := errors.Join(json.Unmarshal(tp[0], &gv), json.Unmarshal(tp[1], &n)); err != nil {
			t.Fatal(err)
		}
		if !sameValue(gv, st.Top[i].Value) || st.Top[i].Count != n {
			errorf("top %d: %#v × %d, golden %s × %v", i, st.Top[i].Value, st.Top[i].Count, gv, n)
		}
	}
}

func TestGoldenHistogramsAndBins(t *testing.T) {
	for _, name := range goldenFixtures {
		t.Run(name, func(t *testing.T) {
			ds, gf := openGolden(t, name)
			for _, r := range gf.Sections["histogram"] {
				var gv goldenView
				r.Decode(t, "view", &gv)
				var o HistOptions
				var kw struct {
					Bins          *int
					Lo, Hi        *float64
					Log, Temporal bool
				}
				if r.Has("bins") {
					r.Decode(t, "bins", &kw.Bins)
				}
				if r.Has("lo") {
					r.Decode(t, "lo", &kw.Lo)
				}
				if r.Has("hi") {
					r.Decode(t, "hi", &kw.Hi)
				}
				if r.Has("log") {
					kw.Log = r.Bool(t, "log")
				}
				if r.Has("temporal") {
					kw.Temporal = r.Bool(t, "temporal")
				}
				if kw.Bins != nil {
					o.Bins = *kw.Bins
				}
				o.Lo, o.Hi, o.Log, o.Temporal = kw.Lo, kw.Hi, kw.Log, kw.Temporal
				h, err := ds.Histogram(bg, gv.view(), r.String(t, "column"), o)
				if msg, ok := r.Err(); ok {
					if err == nil {
						t.Errorf("%s: no error, golden %s", r.ID(), msg)
					}
					continue
				}
				if err != nil {
					t.Errorf("%s: %v", r.ID(), err)
					continue
				}
				var out struct {
					Edges  []string
					Counts []int64
				}
				r.Decode(t, "out", &out)
				var edges []string
				for _, e := range h.Edges {
					edges = append(edges, pyRepr(e))
				}
				if fmt.Sprint(edges) == fmt.Sprint(out.Edges) && fmt.Sprint(h.Counts) == fmt.Sprint(out.Counts) {
					continue
				}
				if exactPlatform || !closeHistogram(t, ds, gv.view(), r.String(t, "column"), o, h, out.Edges, out.Counts) {
					t.Errorf("%s: %v %v\ngolden %v %v", r.ID(), edges, h.Counts, out.Edges, out.Counts)
				}
			}
			for _, r := range gf.Sections["sky_counts"] {
				var gv goldenView
				r.Decode(t, "view", &gv)
				g, err := ds.SkyCounts(bg, gv.view(), r.String(t, "lon"), r.String(t, "lat"), r.Float(t, "res_deg"), Sample{})
				if msg, ok := r.Err(); ok {
					if err == nil {
						t.Errorf("%s: no error, golden %s", r.ID(), msg)
					}
					continue
				}
				var out [][]int64
				r.Decode(t, "out", &out)
				if err != nil || fmt.Sprint(g.Counts) != fmt.Sprint(out) {
					t.Errorf("%s: %v %v\ngolden %v", r.ID(), err, g.Counts, out)
				}
			}
			for _, r := range gf.Sections["xy_counts"] {
				var gv goldenView
				r.Decode(t, "view", &gv)
				var xl, yl *[2]float64
				r.Decode(t, "xlim", &xl)
				r.Decode(t, "ylim", &yl)
				g, err := ds.XYCounts(bg, gv.view(), r.String(t, "x"), r.String(t, "y"), r.Int(t, "nx"), r.Int(t, "ny"), Sample{}, xl, yl)
				if msg, ok := r.Err(); ok {
					if err == nil {
						t.Errorf("%s: no error, golden %s", r.ID(), msg)
					}
					continue
				}
				var out struct {
					Grid       [][]int64
					Xlim, Ylim []string
				}
				r.Decode(t, "out", &out)
				lims := []string{pyRepr(g.X[0]), pyRepr(g.X[1]), pyRepr(g.Y[0]), pyRepr(g.Y[1])}
				if err != nil || fmt.Sprint(g.Counts) != fmt.Sprint(out.Grid) || fmt.Sprint(lims) != fmt.Sprint(append(out.Xlim, out.Ylim...)) {
					t.Errorf("%s: %v %v %v\ngolden %v %v %v", r.ID(), err, g.Counts, lims, out.Grid, out.Xlim, out.Ylim)
				}
			}
		})
	}
}

func TestGoldenSampleCondition(t *testing.T) {
	for _, name := range goldenFixtures {
		t.Run(name, func(t *testing.T) {
			ds, gf := openGolden(t, name)
			for _, r := range gf.Sections["sample_condition"] {
				var n int64
				if !r.IsNull("sample") {
					r.Decode(t, "sample", &n)
				}
				got := strings.ReplaceAll(ds.sampleCondition(n, r.Int(t, "slices")), "file_row_number", "__pqx_row")
				// (pqx renames its row number when the file has a column of that name)
				if want := strings.ReplaceAll(r.String(t, "out"), "__pqx_row__", "__pqx_row"); got != want {
					t.Errorf("%s: %s\ngolden %s", r.ID(), got, want)
				}
			}
		})
	}
}

func TestGoldenFooter(t *testing.T) {
	for _, name := range goldenFixtures {
		t.Run(name, func(t *testing.T) {
			ds, gf := openGolden(t, name)
			summ, err := ds.FooterSummary(bg)
			if err != nil {
				t.Fatal(err)
			}
			recs := gf.Section(t, "column_chunk_summary")
			if len(recs) != len(summ) {
				t.Fatalf("%d leaf paths, golden %d", len(summ), len(recs))
			}
			for i, r := range recs {
				s := summ[i]
				var g struct {
					Path, Physical, Compression, Logical string
					Compressed, Uncompressed, Nulls      int64
					HasStats                             bool `json:"has_stats"`
					Min, Max                             golden.Value
				}
				for k, p := range map[string]any{"path": &g.Path, "physical": &g.Physical, "compression": &g.Compression,
					"logical": &g.Logical, "compressed": &g.Compressed, "uncompressed": &g.Uncompressed, "nulls": &g.Nulls,
					"has_stats": &g.HasStats, "min": &g.Min, "max": &g.Max} {
					r.Decode(t, k, p)
				}
				if s.Path != g.Path || s.Physical != g.Physical || s.Compression != g.Compression || s.Logical != g.Logical ||
					s.Compressed != g.Compressed || s.Uncompressed != g.Uncompressed || s.Nulls != g.Nulls || s.HasStats != g.HasStats {
					t.Errorf("%s: %+v\ngolden %+v", r.ID(), s, g)
				}
				// PyArrow leaves these as their bytes (and for Float16 orders
				// them as bytes); the port decodes them
				if g.Min.Kind == golden.KindBytes && (g.Logical == "Float16" || g.Logical == "UUID" || g.Logical == "JSON") {
					continue
				}
				if !sameValue(g.Min, s.Min) || !sameValue(g.Max, s.Max) {
					t.Errorf("%s (%s): min %#v max %#v, golden %s %s", r.ID(), g.Path, s.Min, s.Max, g.Min, g.Max)
				}
			}
			rgs, err := ds.RowGroupInfo(bg)
			if err != nil {
				t.Fatal(err)
			}
			grs := gf.Section(t, "row_groups")
			if len(grs) != len(rgs) {
				t.Fatalf("%d row groups, golden %d", len(rgs), len(grs))
			}
			for i, r := range grs {
				want := RowGroup{Index: r.Int(t, "index")}
				r.Decode(t, "start", &want.Start)
				r.Decode(t, "rows", &want.Rows)
				r.Decode(t, "compressed", &want.Compressed)
				r.Decode(t, "uncompressed", &want.Uncompressed)
				if rgs[i] != want {
					t.Errorf("%s: %+v, golden %+v", r.ID(), rgs[i], want)
				}
			}
			for _, r := range gf.Section(t, "column_encodings") {
				var want []string
				r.Decode(t, "out", &want)
				if got := ds.Encodings(r.String(t, "path")); fmt.Sprint(got) != fmt.Sprint(want) {
					t.Errorf("%s: %v, golden %v", r.ID(), got, want)
				}
			}
			// pqx lists the key-value metadata sorted by key (PyArrow's
			// order); the port keeps the file's order
			want := map[string]string{}
			for _, r := range gf.Sections["key_value_metadata"] {
				want[r.String(t, "key")] = r.String(t, "value")
			}
			kv := ds.KeyValueMetadata()
			if len(kv) != len(want) {
				t.Errorf("%d key-value entries, golden %d", len(kv), len(want))
			}
			for _, e := range kv {
				if w, ok := want[e.Key]; !ok || w != e.Value {
					t.Errorf("key-value %q: %.80q, golden %.80q", e.Key, e.Value, w)
				}
			}
			info := ds.Info()
			for _, r := range gf.Sections["file"] {
				if info.NumLeaves != r.Int(t, "num_leaf_columns") || info.CreatedBy != r.String(t, "created_by") {
					t.Errorf("info %+v, golden %v", info, r)
				}
			}
		})
	}
}

// exactPlatform is where the golden files were made: there, histograms
// must match them exactly. Elsewhere DuckDB's math library (log10, epoch
// arithmetic) can put a value on the other side of a bin edge.
var exactPlatform = runtime.GOOS == "linux" && runtime.GOARCH == "amd64"

// closeHistogram reports whether h differs from the golden edges and counts
// only as another platform's math can make it: edges within 4 ULPs, and
// counts moved by no more values than lie within 1e-9 (relative) of an edge.
func closeHistogram(t *testing.T, ds *dataset, v View, col string, o HistOptions, h Histogram, gEdges []string, gCounts []int64) bool {
	t.Helper()
	if len(h.Edges) != len(gEdges) || len(h.Counts) != len(gCounts) {
		return false
	}
	var conds []string
	for i, e := range h.Edges {
		g, err := strconv.ParseFloat(gEdges[i], 64)
		if err != nil || math.Abs(e-g) > 4*math.Abs(math.Nextafter(g, math.Inf(1))-g) {
			return false
		}
		conds = append(conds, fmt.Sprintf("abs(v - %s) <= 1e-9 * greatest(1, abs(%s))", pyRepr(e), pyRepr(e)))
	}
	var moved int64
	for i, c := range h.Counts {
		moved += max(c-gCounts[i], gCounts[i]-c)
	}
	rel, err := ds.histRel(v, col, o)
	if err != nil {
		t.Fatal(err)
	}
	var near int64
	err = ds.query(bg, "SELECT count(*) FROM "+rel+" AND ("+strings.Join(conds, " OR ")+")", func(rec arrow.RecordBatch) error {
		near, _ = int64At(rec.Column(0), 0)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("counts differ by %d with %d values next to an edge", moved, near)
	return moved <= 2*near
}
