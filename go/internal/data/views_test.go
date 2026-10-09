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
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/mjuric/pqx/go/internal/golden"
)

// Ports of pqx's tests/test_data.py (views), tests/test_fetch.py and the data
// parts of tests/test_security.py. Each test names the Python test it ports.

func mustFetch(t *testing.T, ds Dataset, v View, start int64, n int, cols []string) Window {
	t.Helper()
	w, err := ds.Fetch(bg, v, start, n, cols)
	if err != nil {
		t.Fatalf("Fetch(%+v, %d, %d): %v", v, start, n, err)
	}
	return w
}

// demoTruth is the demo fixture's columns, read with the plain view.
func demoTruth(t *testing.T, d *dataset, cols ...string) map[string][]Value {
	t.Helper()
	return mustFetch(t, d, View{}, 0, int(d.NumRows()), cols).Cols
}

func f32(v Value) float32 { x, _ := v.(float32); return x }

// test_data.py::test_filter_sort_count
func TestFilterSortCount(t *testing.T) {
	d := openFixture(t, "demo.parquet")
	tr := demoTruth(t, d, "diaSourceId", "mag", "band")
	var exp []int64
	for i := range tr["mag"] {
		if tr["mag"][i] != nil && f32(tr["mag"][i]) < 20 && tr["band"][i] == "r" {
			exp = append(exp, int64(i))
		}
	}
	sort.SliceStable(exp, func(a, b int) bool { return f32(tr["mag"][exp[a]]) > f32(tr["mag"][exp[b]]) })
	v := View{Where: "mag < 20 and band = 'r'", OrderBy: []Sort{{Column: "mag", Desc: true}}}
	if n, err := d.Count(bg, v); err != nil || n != int64(len(exp)) {
		t.Fatalf("Count %d %v, want %d", n, err, len(exp))
	}
	w := mustFetch(t, d, v, 0, 10, []string{"diaSourceId", "mag"})
	if !slices.Equal(w.FileRows, exp[:10]) {
		t.Fatalf("FileRows %v, want %v", w.FileRows, exp[:10])
	}
	for i, r := range exp[:10] {
		if w.Cols["diaSourceId"][i] != tr["diaSourceId"][r] {
			t.Fatalf("row %d: %v", i, w.Cols["diaSourceId"][i])
		}
	}
	if pos, ok, err := d.FindRow(bg, v, w.FileRows[3]); err != nil || !ok || pos != 3 {
		t.Fatalf("FindRow: %d %v %v", pos, ok, err)
	}
	// the whole view: the same rows as sorting by hand (ties in file order)
	all := mustFetch(t, d, v, 0, 1000, []string{"mag"})
	if !slices.Equal(all.FileRows, exp) {
		t.Fatalf("whole view differs")
	}
	cols, err := d.Validate(bg, v)
	if err != nil || len(cols) != len(d.cols) {
		t.Fatalf("Validate: %v %v", cols, err)
	}
}

// Sorts: ascending and descending, NULLs last both ways, ties in file order,
// several keys, windows anywhere in the view.
func TestSortOrder(t *testing.T) {
	d := openFixture(t, "demo.parquet")
	tr := demoTruth(t, d, "trailLength", "detector")
	n := len(tr["detector"])
	for _, desc := range []bool{false, true} {
		v := View{OrderBy: []Sort{{Column: "trailLength", Desc: desc}, {Column: "detector"}}}
		exp := make([]int64, n)
		for i := range exp {
			exp[i] = int64(i)
		}
		key := func(r int64) (bool, float64, int64) {
			x, ok := tr["trailLength"][r].(float64)
			return ok, x, tr["detector"][r].(int64)
		}
		sort.SliceStable(exp, func(a, b int) bool {
			oa, xa, da := key(exp[a])
			ob, xb, db := key(exp[b])
			if oa != ob {
				return oa // NULLs last
			}
			if oa && xa != xb {
				return (xa < xb) != desc
			}
			return da < db
		})
		for _, start := range []int64{0, 7_000, 8_078, 8_079, 19_990} {
			w := mustFetch(t, d, v, start, 25, []string{"trailLength"})
			want := exp[start:min(start+25, int64(n))]
			if !slices.Equal(w.FileRows, want) {
				t.Fatalf("desc=%v start %d: %v, want %v", desc, start, w.FileRows, want)
			}
			for i, r := range want {
				if !sameVal(w.Cols["trailLength"][i], tr["trailLength"][r]) {
					t.Fatalf("value")
				}
			}
		}
		if c, _ := d.Count(bg, v); c != int64(n) {
			t.Fatalf("Count %d", c)
		}
		for _, pos := range []int64{0, 1234, 8078, 8079, int64(n) - 1} {
			if p, ok, err := d.FindRow(bg, v, exp[pos]); err != nil || !ok || p != pos {
				t.Fatalf("FindRow(%d) = %d %v %v, want %d", exp[pos], p, ok, err, pos)
			}
		}
	}
	if _, err := d.Fetch(bg, View{OrderBy: []Sort{{Column: "nope"}}}, 0, 5, []string{"mag"}); err == nil {
		t.Fatal("sort by a missing column")
	}
	if _, err := d.Validate(bg, View{OrderBy: []Sort{{Column: "nope"}}}); err == nil {
		t.Fatal("Validate: sort by a missing column")
	}
}

// test_data.py::test_find_row
func TestFindRow(t *testing.T) {
	d := openFixture(t, "demo.parquet")
	tr := demoTruth(t, d, "band", "mag")
	var r, notR []int64
	for i, b := range tr["band"] {
		if b == "r" {
			r = append(r, int64(i))
		} else {
			notR = append(notR, int64(i))
		}
	}
	v := View{Where: "band = 'r'"}
	for _, pos := range []int{0, 1, 999, 1000, len(r) - 1} {
		if p, ok, err := d.FindRow(bg, v, r[pos]); err != nil || !ok || p != int64(pos) {
			t.Fatalf("FindRow(%d) = %d %v %v", r[pos], p, ok, err)
		}
	}
	if _, ok, err := d.FindRow(bg, v, notR[5]); ok || err != nil {
		t.Fatalf("a row not in the view: %v %v", ok, err)
	}
	sorted := slices.Clone(r)
	sort.SliceStable(sorted, func(a, b int) bool { return f32(tr["mag"][sorted[a]]) < f32(tr["mag"][sorted[b]]) })
	v = View{Where: "band = 'r'", OrderBy: []Sort{{Column: "mag"}}}
	for _, pos := range []int{0, 1234, len(sorted) - 1} {
		if p, ok, err := d.FindRow(bg, v, sorted[pos]); err != nil || !ok || p != int64(pos) {
			t.Fatalf("sorted FindRow(%d) = %d %v %v", sorted[pos], p, ok, err)
		}
	}
	if _, ok, _ := d.FindRow(bg, v, notR[0]); ok {
		t.Fatal("sorted: a row not in the view")
	}
	if p, ok, _ := d.FindRow(bg, View{}, 777); !ok || p != 777 {
		t.Fatal("plain")
	}
	for _, fr := range []int64{-1, 20_000} {
		if _, ok, err := d.FindRow(bg, View{}, fr); ok || err != nil {
			t.Fatalf("plain %d: %v %v", fr, ok, err)
		}
		if _, ok, err := d.FindRow(bg, View{Where: "true"}, fr); ok || err != nil {
			t.Fatalf("filtered %d: %v %v", fr, ok, err)
		}
	}
	if _, ok, err := d.FindRow(bg, View{SQL: "select * from t"}, 5); ok || err != nil {
		t.Fatalf("SQL: %v %v", ok, err)
	}
	// a file DuckDB can't number: not found, no error
	odd := openFixture(t, "odd.parquet")
	if _, ok, err := odd.FindRow(bg, View{Where: "x > 0"}, 3); ok || err != nil {
		t.Fatalf("odd: %v %v", ok, err)
	}
}

// test_data.py::test_sql_mode
func TestSQLView(t *testing.T) {
	d := openFixture(t, "demo.parquet")
	tr := demoTruth(t, d, "band")
	counts := map[string]int64{}
	for _, b := range tr["band"] {
		counts[b.(string)]++
	}
	v := View{SQL: "select band, count(*) as n from t group by band order by band"}
	cols, err := d.Validate(bg, v)
	if err != nil || len(cols) != 2 || cols[0].Name != "band" || cols[1].Name != "n" {
		t.Fatalf("Validate: %+v %v", cols, err)
	}
	if cols[0].Type != "VARCHAR" || cols[1].Type != "BIGINT" || cols[1].Arrow.ID() != arrow.INT64 || cols[0].SQLName != "band" {
		t.Fatalf("types: %+v", cols)
	}
	w := mustFetch(t, d, v, 0, 100, []string{"band", "n"})
	if w.Len != 6 || w.FileRows != nil {
		t.Fatalf("Len %d FileRows %v", w.Len, w.FileRows)
	}
	for i := range w.Len {
		if counts[w.Cols["band"][i].(string)] != w.Cols["n"][i].(int64) {
			t.Fatalf("%v", w.Cols)
		}
	}
	if n, err := d.Count(bg, v); err != nil || n != 6 {
		t.Fatalf("Count %d %v", n, err)
	}
	w = mustFetch(t, d, v, 2, 10, []string{"n"})
	if len(w.Cols) != 1 || w.Len != 4 || w.Start != 2 {
		t.Fatalf("%+v", w)
	}
	w = mustFetch(t, d, v, 1, 2, nil) // no columns: still the rows
	if w.Len != 2 {
		t.Fatalf("no columns: %+v", w)
	}
	// a trailing ; and a trailing comment are fine
	for _, q := range []string{"select 1 as one;", "select 1 as one -- a comment", "  FROM t LIMIT 1 ;; "} {
		if _, err := d.Validate(bg, View{SQL: q}); err != nil {
			t.Errorf("%q: %v", q, err)
		}
		if n, err := d.Count(bg, View{SQL: q}); err != nil || n != 1 {
			t.Errorf("%q: Count %d %v", q, n, err)
		}
	}
	// types of a SQL result, as DuckDB names them (DESCRIBE)
	q := `select {'select': 1, 'Name': 2, 'x1': [1.5], 'Ab': 1, 'B': 2, 'xY': 3, 'Type': 4, '1a': 5, 'é': 6, 'ÉA': 7} s, uuid() u, [uuid()] lu, 1::DECIMAL(5,1) dd,
		'x'::ENUM('x', 'y''z') e, [1,2]::INTEGER[2] a, MAP {'k': 1} m, NULL n, TIMESTAMPTZ '2020-01-01' tz,
		'{}'::JSON j, 1::HUGEINT h, INTERVAL 1 DAY i, TIMESTAMP_NS '2020-01-01' tns, TIME '01:02:03' tm`
	cols, err = d.Validate(bg, View{SQL: q})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	err = d.query(bg, "SELECT column_name, column_type FROM (DESCRIBE "+q+")", func(rec arrow.RecordBatch) error {
		for i := range int(rec.NumRows()) {
			want[ValueAt(rec.Column(0), i).(string)] = ValueAt(rec.Column(1), i).(string)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cols {
		if c.Type != want[c.Name] {
			t.Errorf("%s: %q, DESCRIBE says %q", c.Name, c.Type, want[c.Name])
		}
	}
	w = mustFetch(t, d, View{SQL: q}, 0, 1, []string{"u", "lu", "s", "tz", "e"})
	if _, ok := w.Cols["u"][0].(UUID); !ok {
		t.Errorf("uuid: %#v", w.Cols["u"][0])
	}
	if l, ok := w.Cols["lu"][0].(List); !ok || len(l) != 1 {
		t.Errorf("list of uuid: %#v", w.Cols["lu"][0])
	} else if _, ok := l[0].(UUID); !ok {
		t.Errorf("list of uuid: %#v", l[0])
	}
	if ts, ok := w.Cols["tz"][0].(Timestamp); !ok || !ts.Zoned || ts.Unit != time.Microsecond {
		t.Errorf("tz: %#v", w.Cols["tz"][0])
	}
	if w.Cols["e"][0] != "x" {
		t.Errorf("enum: %#v", w.Cols["e"][0])
	}
	// the file's own names, DuckDB's for case duplicates
	cd := openFixture(t, "casedup.parquet")
	w = mustFetch(t, cd, View{SQL: "select name_1, Name from t order by x desc"}, 0, 5, []string{"name_1", "Name"})
	if w.Cols["name_1"][0] != "c" || w.Cols["Name"][0] != "C" {
		t.Fatalf("%v", w.Cols)
	}
}

// test_data.py::test_bad_filter_raises
func TestBadFilters(t *testing.T) {
	d := openFixture(t, "demo.parquet")
	for _, v := range []View{
		{Where: "nosuchcolumn > 3"}, {Where: "mag <"},
		{Where: "nosuchcolumn > 3", OrderBy: []Sort{{Column: "mag"}}},
		{SQL: "select nosuchcolumn from t"}, {SQL: "select from where"},
		{SQL: "select 1\x00"},
	} {
		_, err := d.Validate(bg, v)
		if err == nil || HasControls(err.Error()) || strings.Contains(err.Error(), "LINE") {
			t.Errorf("%+v: %v", v, err)
		}
		if _, err := d.Fetch(bg, v, 0, 5, []string{"mag"}); err == nil {
			t.Errorf("Fetch(%+v) ran", v)
		}
		if _, err := d.Count(bg, v); err == nil {
			t.Errorf("Count(%+v) ran", v)
		}
	}
}

// test_data.py::test_odd_file (views)
func TestOddFileViews(t *testing.T) {
	d := openFixture(t, "odd.parquet")
	if d.hasRowNum {
		t.Fatal("odd has its own file_row_number")
	}
	w := mustFetch(t, d, View{}, 950, 100, colNames(d))
	if w.Len != 50 || w.FileRows[0] != 950 || w.Cols["file_row_number"][0] != int64(9500) {
		t.Fatalf("%d %v", w.Len, w.FileRows[:3])
	}
	if n, err := d.Count(bg, View{Where: `"weird name" = 1`}); err != nil || n == 0 {
		t.Fatalf("Count %d %v", n, err)
	}
	w = mustFetch(t, d, View{Where: "len(tags) = 2"}, 0, 5, colNames(d))
	if !sameVal(w.Cols["tags"][0], List{"a", "b"}) || w.FileRows[0] != -1 {
		t.Fatalf("%v %v", w.Cols["tags"][0], w.FileRows)
	}
	// DuckDB sorts NaN above +inf (as Python pqx shows it): 20 NaN, then +inf
	w = mustFetch(t, d, View{OrderBy: []Sort{{Column: "x", Desc: true}}}, 19, 3, []string{"x"})
	if !math.IsNaN(w.Cols["x"][0].(float64)) || w.Cols["x"][1] != math.Inf(1) || w.FileRows[0] != -1 {
		t.Fatalf("%v %v", w.Cols["x"], w.FileRows)
	}
	// rows without numbers can't be read by number (test_fetch_columns_needs_row_ids)
	if _, err := d.FetchColumns(bg, w.FileRows, []string{"tags"}); err == nil || !errors.Is(err, errNoRowNumbers) {
		t.Fatalf("FetchColumns of unnumbered rows: %v", err)
	}
	// FetchAround falls back to Fetch
	a, err := d.FetchAround(bg, View{Where: "x > 0"}, 3, 2, 0, 10, []string{"x"})
	b := mustFetch(t, d, View{Where: "x > 0"}, 0, 10, []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	sameWindow(t, "odd FetchAround", a, b)
}

// test_data.py::test_fetch_around_matches_fetch
func TestFetchAroundMatchesFetch(t *testing.T) {
	d := openFixture(t, "demo.parquet")
	tr := demoTruth(t, d, "band", "detector", "mag")
	cols := []string{"diaSourceId", "mag"}
	for _, c := range []struct {
		where string
		match func(i int) bool
	}{
		{"band = 'r'", func(i int) bool { return tr["band"][i] == "r" }},
		{"detector = 7", func(i int) bool { return tr["detector"][i] == int64(7) }},
		{"mag < 30", func(i int) bool { return f32(tr["mag"][i]) < 30 }},
	} {
		v := View{Where: c.where}
		var rows []int64
		for i := range tr["band"] {
			if c.match(i) {
				rows = append(rows, int64(i))
			}
		}
		for _, pos := range []int{0, 3, 400, len(rows) / 2, len(rows) - 1} {
			pos = min(pos, len(rows)-1)
			for _, off := range []int{pos, max(0, pos-150), max(0, pos-299), max(0, pos-999)} {
				got, err := d.FetchAround(bg, v, rows[pos], int64(pos), int64(off), 300, cols)
				if err != nil {
					t.Fatal(err)
				}
				want := mustFetch(t, d, v, int64(off), 300, cols)
				sameWindow(t, fmt.Sprintf("%s pos %d off %d", c.where, pos, off), got, want)
			}
		}
	}
	// a record with fewer rows before it than asked for: the window starts later
	got, err := d.FetchAround(bg, View{Where: "detector = 7"}, 100_000, 50, 10, 100, cols)
	if err == nil {
		t.Logf("a file row past the end: Start %d Len %d", got.Start, got.Len)
	}
}

// test_data.py::test_filters_cannot_escape_their_parentheses
func TestFiltersCannotEscapeParentheses(t *testing.T) {
	d := openFixture(t, "demo.parquet")
	for _, bad := range []string{"detector = 3) OR (band = 'r'", "(detector = 3", "detector = 3)", "detector = 3)) OR ((band = 'r'"} {
		v := View{Where: bad}
		for name, call := range map[string]func() error{
			"Validate":    func() error { _, err := d.Validate(bg, v); return err },
			"Count":       func() error { _, err := d.Count(bg, v); return err },
			"Fetch":       func() error { _, err := d.Fetch(bg, v, 0, 10, []string{"mag"}); return err },
			"FindRow":     func() error { _, _, err := d.FindRow(bg, v, 5); return err },
			"FetchAround": func() error { _, err := d.FetchAround(bg, v, 5, 0, 0, 150, []string{"mag"}); return err },
			"sorted": func() error {
				_, err := d.Fetch(bg, View{Where: bad, OrderBy: []Sort{{Column: "mag"}}}, 0, 10, []string{"mag"})
				return err
			},
			"sortedFind": func() error {
				_, _, err := d.FindRow(bg, View{Where: bad, OrderBy: []Sort{{Column: "mag"}}}, 5)
				return err
			},
		} {
			if err := call(); err == nil || !strings.Contains(err.Error(), "unbalanced parentheses") {
				t.Errorf("%s(%q): %v", name, bad, err)
			}
		}
	}
}

// test_data.py::test_parentheses_in_literals_and_comments_are_fine
func TestParenthesesInLiteralsAreFine(t *testing.T) {
	d := openFixture(t, "demo.parquet")
	for _, where := range []string{"band = ')' or detector = 3", "detector = 3 -- )", "detector = 3 /* ( */",
		"(detector = 3) or (band = 'r')", `"detector" = 3 or "band" = '('`} {
		v := View{Where: where}
		if _, err := d.Validate(bg, v); err != nil {
			t.Fatalf("%q: %v", where, err)
		}
		n, err := d.Count(bg, v)
		if err != nil {
			t.Fatal(err)
		}
		rows := mustFetch(t, d, v, 0, int(n), []string{"diaSourceId"}).FileRows
		for _, pos := range []int{0, len(rows) / 2, len(rows) - 1} {
			got, err := d.FetchAround(bg, v, rows[pos], int64(pos), int64(max(0, pos-75)), 150, []string{"diaSourceId"})
			if err != nil || got.Len > 150 || !slices.Equal(got.FileRows, rows[got.Start:min(int(got.Start)+150, len(rows))]) {
				t.Fatalf("%q pos %d: %v", where, pos, err)
			}
			if p, ok, err := d.FindRow(bg, v, rows[pos]); err != nil || !ok || p != int64(pos) {
				t.Fatalf("%q FindRow: %d %v %v", where, p, ok, err)
			}
		}
	}
}

// test_fetch.py::test_decimal256_values_are_right: decimals wider than 38
// digits are exact in every view, though DuckDB reads them wrong.
func TestWideDecimals(t *testing.T) {
	d := openFixture(t, "types.parquet")
	// the fixture: dec50_2 = (row - 30) / 100, NULL where row % 11 == 10
	want := func(r int64) Value {
		if r%11 == 10 {
			return nil
		}
		return Decimal{Unscaled: bigInt(r - 30), Scale: 2, Precision: 50}
	}
	check := func(what string, w Window) {
		t.Helper()
		if len(w.Failed) > 0 || w.Len == 0 {
			t.Fatalf("%s: %+v", what, w)
		}
		for i, r := range w.FileRows {
			if !sameVal(w.Cols["dec50_2"][i], want(r)) {
				t.Fatalf("%s: row %d: %#v, want %#v", what, r, w.Cols["dec50_2"][i], want(r))
			}
		}
	}
	// DuckDB still reads them wrong (if this fails, DuckDB is fixed: drop the special case)
	var raw []Value
	d.query(bg, "SELECT dec50_2 FROM "+d.src+" LIMIT 3", func(rec arrow.RecordBatch) error {
		raw = append(raw, valueColumn(rec.Column(0), 0, int(rec.NumRows()), nil)...)
		return nil
	})
	if f, ok := raw[0].(float64); !ok || f == -0.30 {
		t.Fatalf("DuckDB reads decimal256 right now (%#v): drop wideCols and its special cases", raw)
	}
	check("plain", mustFetch(t, d, View{}, 0, 60, []string{"i8", "dec50_2"}))
	for _, v := range []View{{Where: "i8 > 10"}, {OrderBy: []Sort{{Column: "i8", Desc: true}}},
		{Where: "i8 % 3 = 0", OrderBy: []Sort{{Column: "f32"}}}} {
		check(fmt.Sprintf("%+v", v), mustFetch(t, d, v, 2, 20, []string{"i8", "dec50_2"}))
	}
	w, err := d.FetchColumns(bg, []int64{5, 3, 59}, []string{"dec50_2"})
	if err != nil {
		t.Fatal(err)
	}
	check("FetchColumns", w)
	forceDuck = true
	defer func() { forceDuck = false }()
	w, err = d.FetchColumns(bg, []int64{5, 3, 59, 3}, []string{"dec50_2", "i8"})
	if err != nil {
		t.Fatal(err)
	}
	check("FetchColumns through DuckDB", w)
	a, err := d.FetchAround(bg, View{Where: "i8 > 0"}, 40, 5, 0, 20, []string{"dec50_2"})
	if err != nil {
		t.Fatal(err)
	}
	check("FetchAround", a)
	// nested: the dec76_10 column is exact too
	w = mustFetch(t, d, View{Where: "true"}, 0, 5, []string{"dec76_10"})
	if x, ok := w.Cols["dec76_10"][1].(Decimal); !ok || x.Precision != 76 || x.Scale != 10 {
		t.Fatalf("%#v", w.Cols["dec76_10"][1])
	}
}

// test_fetch.py::test_types_are_duckdbs: values are DuckDB's in every reader.
func TestTypesAreDuckDBs(t *testing.T) {
	d := openFixture(t, "types.parquet")
	cols := []string{"dict", "lstr", "list", "ts_ms", "ts_off", "f16", "fsl", "null", "uuid", "json", "ts_ns", "dur_ms"}
	for _, v := range []View{{}, {Where: "true"}} {
		w := mustFetch(t, d, v, 0, 3, cols)
		row := func(c string) Value { return w.Cols[c][1] }
		if _, ok := row("dict").(string); !ok {
			t.Errorf("dict: %#v", row("dict"))
		}
		if ts := row("ts_ms").(Timestamp); ts.Unit != time.Microsecond || ts.Zoned {
			t.Errorf("ts_ms: %#v", ts)
		}
		if ts := row("ts_off").(Timestamp); ts.Unit != time.Microsecond || !ts.Zoned || ts.T.Location() != time.UTC {
			t.Errorf("ts_off: %#v", ts)
		}
		if ts := row("ts_ns").(Timestamp); ts.Unit != time.Nanosecond || ts.T.Nanosecond()%1000 == 0 {
			t.Errorf("ts_ns: %#v", ts)
		}
		if _, ok := row("f16").(float32); !ok {
			t.Errorf("f16: %#v", row("f16"))
		}
		if l, ok := row("fsl").(List); !ok || len(l) != 2 || l[0] != int64(1) {
			t.Errorf("fsl: %#v", row("fsl"))
		}
		if row("null") != nil {
			t.Errorf("null: %#v", row("null"))
		}
		if _, ok := row("uuid").(UUID); !ok {
			t.Errorf("uuid: %#v", row("uuid"))
		}
		if row("json") != `{"a": 1}` {
			t.Errorf("json: %#v", row("json"))
		}
		if row("dur_ms") == nil {
			t.Errorf("dur_ms: %#v", row("dur_ms"))
		} else if _, ok := row("dur_ms").(int64); !ok {
			t.Errorf("dur_ms: %#v", row("dur_ms"))
		}
	}
}

// manyRowGroups writes test_fetch.py's many_path: row groups of 100, 37, 0,
// 250, 1, 99 and 513 rows (one empty), columns id, x, s.
func manyRowGroups(t *testing.T) *dataset {
	t.Helper()
	p := filepath.Join(t.TempDir(), "many.parquet")
	sc := arrow.NewSchema([]arrow.Field{{Name: "id", Type: arrow.PrimitiveTypes.Int64}, {Name: "x", Type: arrow.PrimitiveTypes.Float64},
		{Name: "s", Type: arrow.BinaryTypes.String, Nullable: true}}, nil)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	fw, err := pqarrow.NewFileWriter(sc, f, parquet.NewWriterProperties(parquet.WithMaxRowGroupLength(1000)), pqarrow.DefaultWriterProps())
	if err != nil {
		t.Fatal(err)
	}
	start := 0
	for _, n := range []int{100, 37, 0, 250, 1, 99, 513} {
		b := array.NewRecordBuilder(memory.DefaultAllocator, sc)
		for i := start; i < start+n; i++ {
			b.Field(0).(*array.Int64Builder).Append(int64(i))
			b.Field(1).(*array.Float64Builder).Append(math.Sin(float64(i)))
			b.Field(2).(*array.StringBuilder).Append(fmt.Sprintf("r%d", i))
		}
		rec := b.NewRecordBatch()
		if err := fw.Write(rec); err != nil {
			t.Fatal(err)
		}
		rec.Release()
		b.Release()
		start += n
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	ds, err := Open(p, Options{Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ds.Close() })
	d := ds.(*dataset)
	if !slices.Equal(d.rgRows, []int64{100, 37, 0, 250, 1, 99, 513}) {
		t.Fatalf("row groups %v", d.rgRows)
	}
	return d
}

// directVsDuck compares the plain view (arrow-go) with DuckDB's file_row_number
// read of the same rows.
func directVsDuck(t *testing.T, d *dataset, start int64, n int, cols []string) {
	t.Helper()
	idx, _ := d.columnIndices(cols)
	a := mustFetch(t, d, View{}, start, n, cols)
	var b Window
	var err error
	if d.hasRowNum {
		b, err = d.fetchPlainDuck(bg, start, n, cols, idx)
	} else {
		forceDuck = true
		b, err = d.Fetch(bg, View{}, start, n, cols)
		forceDuck = false
	}
	if err != nil {
		t.Fatal(err)
	}
	sameWindow(t, fmt.Sprintf("%d+%d", start, n), a, b)
}

// test_fetch.py::test_demo_windows, test_odd_file, test_many_row_groups
func TestWindowsDirectVsDuck(t *testing.T) {
	demo := openFixture(t, "demo.parquet")
	for _, off := range []int64{0, 2_499, 2_500, 2_450, 12_345, 19_990, 19_999, 20_000, 25_000} {
		for _, n := range []int{1, 50, 3_000} {
			directVsDuck(t, demo, off, n, colNames(demo))
		}
	}
	directVsDuck(t, demo, 4_990, 20, []string{"mag", "diaSourceId", "band"})
	odd := openFixture(t, "odd.parquet")
	for _, w := range [][2]int{{0, 1_000}, {290, 20}, {950, 100}, {999, 1}} {
		directVsDuck(t, odd, int64(w[0]), w[1], colNames(odd))
	}
	many := manyRowGroups(t)
	for _, off := range []int64{0, 99, 100, 136, 137, 138, 386, 387, 388, 486, 487, 999, 1_000} {
		for _, n := range []int{1, 2, 50, 400, 2_000} {
			directVsDuck(t, many, off, n, colNames(many))
		}
	}
	directVsDuck(t, many, 120, 30, []string{"s", "id"})
}

// test_fetch.py::test_reads_only_needed_row_groups
func TestReadsOnlyNeededRowGroups(t *testing.T) {
	d := manyRowGroups(t)
	var mu sync.Mutex
	var calls [][2]int
	readHook = func(rg int, leaves []int) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, [2]int{rg, leaves[0]})
		return nil
	}
	defer func() { readHook = nil }()
	w := mustFetch(t, d, View{}, 130, 10, []string{"x"})
	if !slices.Equal(w.FileRows, []int64{130, 131, 132, 133, 134, 135, 136, 137, 138, 139}) {
		t.Fatalf("%v", w.FileRows)
	}
	if !slices.Equal(calls, [][2]int{{1, 1}, {3, 1}}) { // not the empty one
		t.Fatalf("reads %v", calls)
	}
	calls = nil
	mustFetch(t, d, View{Where: "id > 3"}, 0, 10, []string{"id"})
	if len(calls) != 0 {
		t.Fatalf("a filtered view read with arrow-go: %v", calls)
	}
}

// test_fetch.py::test_io_errors_fall_back_without_excluding and
// test_io_error_count_resets
func TestIOErrorsFallBack(t *testing.T) {
	d := openFixture(t, "demo.parquet")
	ioErr := &os.PathError{Op: "read", Path: "x", Err: syscall.EIO}
	readHook = func(rg int, leaves []int) error { return ioErr }
	defer func() { readHook = nil }()
	for k := 1; k <= 2; k++ {
		w := mustFetch(t, d, View{}, 100, 10, []string{"mag", "band"})
		if w.FileRows[0] != 100 || w.Cols["mag"][0] == nil || len(w.Failed) > 0 {
			t.Fatalf("%+v", w)
		}
		if d.fb.ioErrors != k || len(d.fb.excluded) > 0 {
			t.Fatalf("after %d: %d errors, excluded %v", k, d.fb.ioErrors, d.fb.excluded)
		}
	}
	readHook = nil
	mustFetch(t, d, View{}, 0, 10, []string{"mag"})
	if d.fb.ioErrors != 0 {
		t.Fatalf("count not reset: %d", d.fb.ioErrors)
	}
}

// test_fetch.py::test_read_errors_exclude_only_the_bad_column
func TestReadErrorExcludesOnlyTheBadColumn(t *testing.T) {
	d := openFixture(t, "demo.parquet")
	band := d.leaves[d.byName["band"]][0]
	readHook = func(rg int, leaves []int) error {
		if leaves[0] == band {
			return errors.New("arrow-go can't decode this")
		}
		return nil
	}
	defer func() { readHook = nil }()
	want := mustFetch(t, d, View{Where: "true"}, 100, 10, []string{"mag", "band"})
	w := mustFetch(t, d, View{}, 100, 10, []string{"mag", "band"})
	sameWindow(t, "bad column", w, want)
	if !d.fb.excluded[d.byName["band"]] || d.fb.excluded[d.byName["mag"]] {
		t.Fatalf("excluded %v", d.fb.excluded)
	}
	var mu sync.Mutex
	var reads []int
	readHook = func(rg int, leaves []int) error {
		mu.Lock()
		defer mu.Unlock()
		reads = append(reads, leaves[0])
		return nil
	}
	mustFetch(t, d, View{}, 100, 10, []string{"mag", "band"})
	if !slices.Equal(reads, []int{d.leaves[d.byName["mag"]][0]}) {
		t.Fatalf("arrow-go read %v; band should be DuckDB's", reads)
	}
}

// test_fetch.py::test_unpinned_read_error_remembers_row_groups
func TestUnpinnedReadErrorRemembersRowGroups(t *testing.T) {
	d := manyRowGroups(t)
	var mu sync.Mutex
	var calls []int
	defer func() { readHook, readRGHook = nil, nil }()
	readHook = func(rg int, leaves []int) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, rg)
		return nil
	}
	// row group 3 fails when its columns are read together, never alone
	readRGHook = func(rg int, fields []int) error {
		if rg == 3 && len(fields) > 1 {
			return errors.New("bad page deep in row group 3")
		}
		return nil
	}
	want := mustFetch(t, d, View{Where: "true"}, 130, 200, colNames(d))
	w := mustFetch(t, d, View{}, 130, 200, colNames(d)) // row groups 1 and 3
	sameWindow(t, "row group 3", w, want)
	if !d.fb.badRGs[3] || d.fb.badRGs[1] || len(d.fb.badRGs) != 1 || len(d.fb.excluded) > 0 {
		t.Fatalf("bad %v excluded %v", d.fb.badRGs, d.fb.excluded)
	}
	calls = nil
	mustFetch(t, d, View{}, 300, 20, colNames(d))
	if len(calls) != 0 {
		t.Fatalf("read row group 3 again: %v", calls)
	}
	mustFetch(t, d, View{}, 0, 20, []string{"id"})
	if !slices.Equal(calls, []int{0}) {
		t.Fatalf("other row groups: %v", calls)
	}
}

// A column that fails in one row group only is found there, and only that
// column goes to DuckDB (not the row group read before it).
func TestReadErrorInALaterRowGroup(t *testing.T) {
	d := manyRowGroups(t)
	x := d.leaves[d.byName["x"]][0]
	readHook = func(rg int, leaves []int) error {
		if rg == 3 && leaves[0] == x {
			return errors.New("corrupt chunk of x in row group 3")
		}
		return nil
	}
	defer func() { readHook = nil }()
	want := mustFetch(t, d, View{Where: "true"}, 120, 100, colNames(d))
	w := mustFetch(t, d, View{}, 120, 100, colNames(d)) // row groups 1 and 3
	sameWindow(t, "x in row group 3", w, want)
	if !d.fb.excluded[d.byName["x"]] || len(d.fb.excluded) != 1 || len(d.fb.badRGs) != 0 {
		t.Fatalf("bad %v excluded %v", d.fb.badRGs, d.fb.excluded)
	}
}

// Columns neither reader can read are Failed, not an error for the window.
func TestFailedColumns(t *testing.T) {
	d := openFixture(t, "demo.parquet")
	band := d.byName["band"]
	readHook = func(rg int, leaves []int) error {
		if leaves[0] == d.leaves[band][0] {
			return errors.New("arrow-go can't decode this")
		}
		return nil
	}
	defer func() { readHook = nil }()
	// DuckDB can't read it either: make DuckDB's name for it wrong
	saved := d.duckNames[band]
	d.duckNames[band] = "no such column"
	defer func() { d.duckNames[band] = saved }()
	w := mustFetch(t, d, View{}, 10, 5, []string{"mag", "band"})
	if w.Failed["band"] == nil || w.Cols["mag"] == nil || w.Cols["band"] != nil {
		t.Fatalf("%+v", w)
	}
	f, err := d.FetchColumns(bg, []int64{9, 3}, []string{"band", "mag"})
	if err != nil || f.Failed["band"] == nil || len(f.Cols["mag"]) != 2 {
		t.Fatalf("FetchColumns: %+v %v", f, err)
	}
	// a filtered view: the rows, then each column by file row
	w = mustFetch(t, d, View{Where: "mag < 20"}, 0, 5, []string{"mag", "band"})
	if w.Failed["band"] == nil || len(w.Cols["mag"]) != 5 {
		t.Fatalf("filtered: %+v", w)
	}
}

// test_fetch.py::test_concurrent_fetches
func TestConcurrentFetches(t *testing.T) {
	d := openFixture(t, "demo.parquet")
	want := map[int64]Window{}
	for _, off := range []int64{0, 2_400, 9_999, 17_000} {
		want[off] = mustFetch(t, d, View{Where: "true"}, off, 120, colNames(d))
	}
	errs := make(chan error, 8)
	for off := range want {
		for range 2 {
			go func() {
				for range 5 {
					w, err := d.Fetch(bg, View{}, off, 120, colNames(d))
					if err != nil {
						errs <- err
						return
					}
					for _, c := range colNames(d) {
						for i := range w.Cols[c] {
							if !sameVal(w.Cols[c][i], want[off].Cols[c][i]) {
								errs <- fmt.Errorf("%d %s %d", off, c, i)
								return
							}
						}
					}
				}
				errs <- nil
			}()
		}
	}
	for range 8 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

// test_fetch.py::test_fetch_columns (both readers)
func TestFetchColumns(t *testing.T) {
	many := manyRowGroups(t)
	for _, duck := range []bool{false, true} {
		forceDuck = duck
		for _, c := range []struct {
			d    *dataset
			cols []string
		}{
			{many, []string{"s", "x"}},
			{openFixture(t, "demo.parquet"), []string{"band", "mag", "diaSourceId"}},
			{openFixture(t, "odd.parquet"), []string{"tags", "file_row_number", "pos"}},
			{openFixture(t, "types.parquet"), colNames(openFixture(t, "types.parquet"))},
		} {
			n := c.d.numRows
			var tail []int64
			for r := n - 1; r >= n-7; r-- {
				tail = append(tail, r)
			}
			for _, rows := range [][]int64{{5, 3, 3, n - 1, 0}, rangeRows(min(90, n-50), min(140, n)), {n / 2}, {}, tail} {
				w, err := c.d.FetchColumns(bg, rows, c.cols)
				if err != nil {
					t.Fatal(err)
				}
				if w.Start != 0 || w.Len != len(rows) || !slices.Equal(w.FileRows, rows) || len(w.Failed) > 0 {
					t.Fatalf("%+v", w)
				}
				for i, r := range rows {
					ref := mustFetch(t, c.d, View{}, r, 1, c.cols)
					for _, col := range c.cols {
						if !sameVal(w.Cols[col][i], ref.Cols[col][0]) {
							t.Fatalf("duck=%v %s row %d: %#v vs %#v", duck, col, r, w.Cols[col][i], ref.Cols[col][0])
						}
					}
				}
			}
			if _, err := c.d.FetchColumns(bg, []int64{n}, c.cols); err == nil {
				t.Fatal("a row out of range")
			}
		}
	}
	forceDuck = false
}

func bigInt(x int64) *big.Int { return big.NewInt(x) }

func goldenLoad(t *testing.T, name string) *golden.File { return golden.Load(t, name) }

func rangeRows(a, b int64) []int64 {
	var out []int64
	for r := a; r < b; r++ {
		out = append(out, r)
	}
	return out
}

// test_fetch.py::test_fetch_columns_for_a_sorted_page
func TestFetchColumnsForASortedPage(t *testing.T) {
	d := openFixture(t, "demo.parquet")
	v := View{Where: "mag < 21", OrderBy: []Sort{{Column: "mag", Desc: true}}}
	page := mustFetch(t, d, v, 30, 40, []string{"mag"})
	more, err := d.FetchColumns(bg, page.FileRows, []string{"diaSourceId", "mag"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range page.Len {
		if !sameVal(more.Cols["mag"][i], page.Cols["mag"][i]) {
			t.Fatalf("row %d", i)
		}
	}
}

// test_fetch.py::test_leaves_with_flat_dotted_name
func TestFlatDottedName(t *testing.T) {
	d := openFixture(t, "types.parquet")
	w := mustFetch(t, d, View{}, 0, 5, []string{"a.b", "struct", "map", `q"uote`})
	sameWindow(t, "dotted", w, mustFetch(t, d, View{Where: "true"}, 0, 5, []string{"a.b", "struct", "map", `q"uote`}))
	if w.Cols["a.b"][3] != int64(3) || w.Cols[`q"uote`][3] != int64(6) {
		t.Fatalf("%v", w.Cols)
	}
}

// test_fetch.py::test_extreme_timestamps_dont_raise
func TestExtremeTimestamps(t *testing.T) {
	p := filepath.Join(t.TempDir(), "extreme.parquet")
	sc := arrow.NewSchema([]arrow.Field{{Name: "t", Type: &arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "UTC"}}, {Name: "i", Type: arrow.PrimitiveTypes.Int64}}, nil)
	b := array.NewRecordBuilder(memory.DefaultAllocator, sc)
	for i, v := range []int64{0, 9_223_372_036_854_775_000, -9_223_372_036_854_775_000, 1} {
		b.Field(0).(*array.TimestampBuilder).Append(arrow.Timestamp(v))
		b.Field(1).(*array.Int64Builder).Append(int64(i))
	}
	rec := b.NewRecordBatch()
	tbl := array.NewTableFromRecords(sc, []arrow.RecordBatch{rec})
	writeTable(t, p, tbl, 100, 1<<20)
	ds, err := Open(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	a := mustFetch(t, ds, View{}, 0, 4, []string{"t", "i"})
	sameWindow(t, "extreme", a, mustFetch(t, ds, View{Where: "true"}, 0, 4, []string{"t", "i"}))
	if a.Cols["t"][0].(Timestamp).T.Year() != 1970 || a.Cols["i"][3] != int64(3) {
		t.Fatalf("%v", a.Cols)
	}
}

// test_security.py::test_case_duplicate_columns_each_show_their_own_data
func TestCaseDuplicates(t *testing.T) {
	d := openFixture(t, "casedup.parquet")
	cols := d.Columns()
	if cols[0].SQLName != "Name" || cols[1].SQLName != "name_1" || cols[1].Name != "name" {
		t.Fatalf("%+v", cols)
	}
	for _, v := range []View{{}, {Where: "x > 1"}, {OrderBy: []Sort{{Column: "name", Desc: true}}}} {
		w := mustFetch(t, d, v, 0, 10, []string{"Name", "name", "x"})
		for i := range w.Len {
			if strings.ToLower(w.Cols["Name"][i].(string)) != w.Cols["name"][i] {
				t.Fatalf("%+v: %v", v, w.Cols)
			}
		}
		vc, err := d.Validate(bg, v)
		if err != nil || vc[1].Name != "name" {
			t.Fatalf("Validate: %v", err)
		}
	}
	w := mustFetch(t, d, View{OrderBy: []Sort{{Column: "name", Desc: true}}}, 0, 10, []string{"Name", "name"})
	if w.Cols["Name"][0] != "C" || w.Cols["name"][0] != "c" {
		t.Fatalf("%v", w.Cols)
	}
	f, err := d.FetchColumns(bg, []int64{2, 0}, []string{"name"})
	if err != nil || f.Cols["name"][0] != "c" || f.Cols["name"][1] != "a" {
		t.Fatalf("%v %v", f.Cols, err)
	}
	forceDuck = true
	f, err = d.FetchColumns(bg, []int64{2, 0}, []string{"name"})
	forceDuck = false
	if err != nil || f.Cols["name"][0] != "c" {
		t.Fatalf("DuckDB: %v %v", f.Cols, err)
	}
	if p, ok, _ := d.FindRow(bg, View{OrderBy: []Sort{{Column: "name", Desc: true}}}, 0); !ok || p != 2 {
		t.Fatalf("FindRow %d", p)
	}
}

// test_security.py::test_row_number_column_names_do_not_collide
func TestRowNumberColumnNames(t *testing.T) {
	d := openFixture(t, "rowcol.parquet")
	if d.SetupErr() != nil {
		t.Fatal(d.SetupErr())
	}
	if n, err := d.Count(bg, View{Where: "b > 0"}); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	w := mustFetch(t, d, View{Where: "b > 0", OrderBy: []Sort{{Column: "b", Desc: true}}}, 0, 10, colNames(d))
	if !slices.Equal(w.FileRows, []int64{1, 0}) || w.Cols["__pqx_row"][0] != int64(8) || w.Cols["__PQX_ROW_"][0] != int64(1) {
		t.Fatalf("%+v", w)
	}
}

// test_security.py::test_nul_in_a_column_name_fails_with_a_clear_message
func TestNULInColumnName(t *testing.T) {
	d := openFixture(t, "nulname.parquet")
	_, err := d.Fetch(bg, View{Where: "b > 0"}, 0, 10, colNames(d))
	if err == nil || !strings.Contains(err.Error(), "NUL character") || strings.ContainsRune(err.Error(), 0) {
		t.Fatalf("%v", err)
	}
	// (the plain view reads it with arrow-go; other columns of other views work)
	if w := mustFetch(t, d, View{}, 0, 10, colNames(d)); w.Len != 2 {
		t.Fatalf("%+v", w)
	}
	if w := mustFetch(t, d, View{Where: "b > 1"}, 0, 10, []string{"b"}); w.Len != 1 {
		t.Fatalf("%+v", w)
	}
}

// test_security.py::test_queries_must_be_one_select (data parts) and the
// check_select golden cases.
func TestQueriesMustBeOneSelect(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t0.parquet")
	writeInts(t, p, 1, 2, 3)
	ds0, err := Open(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	p = filepath.Join(dir, "t.parquet") // pqx's: a = 1, 2, 3; b = x, y, x
	duckExec(t, ds0, "COPY (SELECT a, ['x', 'y', 'x'][a] AS b FROM read_parquet("+pathLiteral(ds0.Path())+")) TO "+quoteStr(p)+" (FORMAT parquet)")
	ds0.Close()
	ds, err := Open(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	d := ds.(*dataset)
	pwn := filepath.Join(dir, "pwn.txt")
	for k := 1; k < 5; k++ {
		where := fmt.Sprintf("a > 0%s; COPY (SELECT 1) TO '%s'; %sSELECT 1 AS a WHERE (1", strings.Repeat(")", k), pwn, strings.Repeat("SELECT * FROM (", k-1))
		for _, v := range []View{{Where: where}, {Where: where, OrderBy: []Sort{{Column: "a"}}}} {
			for name, call := range map[string]func() error{
				"Validate":    func() error { _, err := d.Validate(bg, v); return err },
				"Count":       func() error { _, err := d.Count(bg, v); return err },
				"Fetch":       func() error { _, err := d.Fetch(bg, v, 0, 10, []string{"a"}); return err },
				"FindRow":     func() error { _, _, err := d.FindRow(bg, v, 1); return err },
				"FetchAround": func() error { _, err := d.FetchAround(bg, v, 1, 0, 0, 10, []string{"a"}); return err },
			} {
				err := call()
				if err == nil || !strings.Contains(err.Error(), "unbalanced parentheses") && !strings.Contains(err.Error(), "single SELECT") {
					t.Errorf("%s(%q): %v", name, where, err)
				}
			}
		}
	}
	for _, q := range []string{"select * from t; COPY (SELECT 1) TO '" + pwn + "'", "select * from t) TO '" + pwn + "' (FORMAT csv) --",
		"select 1; select 2", "COPY (SELECT 1) TO '" + pwn + "'", "CREATE TYPE x AS ENUM ('a'); SELECT 1",
		"pivot t on a using sum(a); COPY (SELECT 1) TO '" + pwn + "'"} {
		if _, err := d.Validate(bg, View{SQL: q}); err == nil {
			t.Errorf("Validate(%q) ran", q)
		}
		if _, err := d.Fetch(bg, View{SQL: q}, 0, 5, nil); err == nil {
			t.Errorf("Fetch(%q) ran", q)
		}
		if _, err := d.Count(bg, View{SQL: q}); err == nil {
			t.Errorf("Count(%q) ran", q)
		}
	}
	if _, err := os.Stat(pwn); err == nil {
		t.Fatal("a smuggled COPY ran")
	}
	for _, q := range []string{"select a, count(*) from t group by 1", "with x as (select * from t) select * from x",
		"from t", "pivot t on a using sum(a)", "summarize t", "SELECT * FROM (PIVOT t ON a USING count(*))"} {
		cols, err := d.Validate(bg, View{SQL: q})
		if err != nil || len(cols) == 0 {
			t.Errorf("Validate(%q): %v", q, err)
			continue
		}
		if _, err := d.Fetch(bg, View{SQL: q}, 0, 5, []string{cols[0].Name}); err != nil {
			t.Errorf("Fetch(%q): %v", q, err)
		}
	}
	if n, _ := d.Count(bg, View{Where: "b = 'x'"}); n != 2 {
		t.Fatal(n)
	}
	// check_select itself, as pqx's golden cases: one SELECT (or DuckDB's own
	// statements for a PIVOT) is allowed, anything else refused, nothing runs
	f := goldenLoad(t, "data_common.json")
	for _, r := range f.Section(t, "check_select") {
		q := r.String(t, "sql")
		q = strings.ReplaceAll(q, "/tmp/pqx-golden-pwned.txt", pwn)
		err := d.withConn(bg, func(c *duckdbConn) error {
			st, err := prepareSelect(bg, c, q)
			if st != nil {
				st.Close()
			}
			return err
		})
		_, pyErr := r.Err()
		switch {
		case pyErr && err == nil:
			t.Errorf("%s %q: allowed, Python refuses", r.ID(), q)
		case !pyErr && errors.Is(err, errNotOneSelect):
			t.Errorf("%s %q: refused, Python allows", r.ID(), q)
		case !pyErr && err != nil:
			t.Errorf("%s %q: allowed by Python, doesn't bind here: %v", r.ID(), q, err)
		}
	}
	if _, err := os.Stat(pwn); err == nil {
		t.Fatal("a smuggled COPY ran")
	}
}

// File names with glob characters next to backslashes (a known gap of the
// prototype), and glob characters in directory names.
func TestGlobBackslashNames(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "d*[x]")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]int64{
		`a\*.parquet`: {1, 2}, `a\b.parquet`: {3}, `b\[1].parquet`: {4, 4}, `e\?.parquet`: {5},
		`c\.parquet`: {6}, `g\\*.parquet`: {7, 7, 7}, `sub/x?.parquet`: {8}, `sub/x1.parquet`: {9, 9},
	}
	paths := map[string][]int64{}
	for name, vals := range files {
		p := filepath.Join(dir, name)
		if strings.HasPrefix(name, "sub/") {
			p = filepath.Join(sub, strings.TrimPrefix(name, "sub/"))
		}
		writeInts(t, p, vals...)
		paths[p] = vals
	}
	for p, vals := range paths {
		ds, err := Open(p, Options{})
		if err != nil {
			t.Fatal(err)
		}
		d := ds.(*dataset)
		if ds.SetupErr() != nil {
			t.Errorf("%s: %v", p, ds.SetupErr())
		}
		if n, err := ds.Count(bg, View{Where: "a > 0"}); err != nil || n != int64(len(vals)) {
			t.Errorf("%s: Count %d %v", p, n, err)
		}
		if n, err := ds.Count(bg, View{SQL: "select * from t"}); err != nil || n != int64(len(vals)) {
			t.Errorf("%s: SQL Count %d %v", p, n, err)
		}
		link := d.linkDir
		ds.Close()
		if link != "" {
			if _, err := os.Stat(link); err == nil {
				t.Errorf("%s: link dir left behind", p)
			}
		}
	}
}

// A query whose table function blocks in the OS (read_csv of a FIFO): the call
// returns at the cancel, the dataset keeps working, and the stuck goroutine
// finishes (giving back its connection) once the OS call returns. DuckDB
// can't be interrupted inside open(2), and closing a connection another
// goroutine is using isn't safe, so that's as far as it can be bounded.
func TestBlockingTableFunction(t *testing.T) {
	_, ds := fixture(t)
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no FIFOs here:", err)
	}
	ctx, cancel := context.WithTimeout(bg, 300*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	_, err := ds.Count(ctx, View{Where: "id > (SELECT count(*) FROM read_csv(" + quoteStr(fifo) + "))"})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(t0) > 2*time.Second {
		t.Fatalf("%v after %v", err, time.Since(t0))
	}
	if n, err := ds.Count(bg, View{Where: "id < 5"}); err != nil || n != 5 {
		t.Fatalf("after: %d %v", n, err)
	}
	// unblock it: open the FIFO for writing and close it
	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	d := ds.(*dataset)
	deadline := time.Now().Add(5 * time.Second)
	for d.db.Stats().InUse > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := d.db.Stats().InUse; n > 0 {
		t.Fatalf("%d connections still in use", n)
	}
}

// Every new call stops on a cancelled context, and a slow sorted or SQL
// view stops soon after the cancel.
func TestCancelViews(t *testing.T) {
	_, ds := fixture(t)
	done, cancel := context.WithCancel(bg)
	cancel()
	calls := map[string]func(ctx context.Context) error{
		"Validate SQL": func(ctx context.Context) error { _, err := ds.Validate(ctx, View{SQL: "select 1"}); return err },
		"Validate sort": func(ctx context.Context) error {
			_, err := ds.Validate(ctx, View{OrderBy: []Sort{{Column: "id"}}})
			return err
		},
		"Fetch sorted": func(ctx context.Context) error {
			_, err := ds.Fetch(ctx, View{OrderBy: []Sort{{Column: "id"}}}, 0, 5, []string{"id"})
			return err
		},
		"Fetch SQL": func(ctx context.Context) error {
			_, err := ds.Fetch(ctx, View{SQL: "select 1 a"}, 0, 5, []string{"a"})
			return err
		},
		"Count SQL": func(ctx context.Context) error { _, err := ds.Count(ctx, View{SQL: "select 1"}); return err },
		"FetchColumns": func(ctx context.Context) error {
			_, err := ds.FetchColumns(ctx, []int64{5, 3}, []string{"id"})
			return err
		},
		"FindRow": func(ctx context.Context) error { _, _, err := ds.FindRow(ctx, View{Where: "id > 3"}, 5); return err },
		"FetchAround": func(ctx context.Context) error {
			_, err := ds.FetchAround(ctx, View{Where: "id > 3"}, 5, 1, 0, 5, []string{"id"})
			return err
		},
		"FindRow sorted": func(ctx context.Context) error {
			_, _, err := ds.FindRow(ctx, View{OrderBy: []Sort{{Column: "id"}}}, 5)
			return err
		},
	}
	for name, call := range calls {
		if err := call(done); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, v := range map[string]View{
		"sorted": {Where: slow, OrderBy: []Sort{{Column: "id"}}},
		"SQL":    {SQL: "select sum(r) from range(100000000000) t(r)"},
	} {
		ctx, cancel := context.WithTimeout(bg, 100*time.Millisecond)
		t0 := time.Now()
		_, err := ds.Fetch(ctx, v, 0, 5, nil)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(t0) > time.Second {
			t.Errorf("%s: %v after %v", name, err, time.Since(t0))
		}
	}
}

// A sort of a file DuckDB can't number pages consistently through ties:
// every row once across the windows (pqx Python can repeat or skip rows here).
func TestSortTiesWithoutRowNumbers(t *testing.T) {
	_, ds0 := fixture(t)
	p := filepath.Join(t.TempDir(), "ties.parquet")
	duckExec(t, ds0, "COPY (SELECT i AS id, i % 3 AS k, i * 10 AS file_row_number, CASE WHEN i % 7 = 0 THEN NULL ELSE i % 5 END AS m, "+
		"[i % 2] AS l, {'a': i % 4} AS s FROM range(1706) r(i) ORDER BY hash(i)) TO "+quoteStr(p)+" (FORMAT parquet, ROW_GROUP_SIZE 300)")
	ds, err := Open(p, Options{Threads: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	if ds.(*dataset).hasRowNum {
		t.Fatal("the file has its own file_row_number")
	}
	for _, v := range []View{{OrderBy: []Sort{{Column: "k"}}}, {OrderBy: []Sort{{Column: "m", Desc: true}}}, {Where: "k > 0", OrderBy: []Sort{{Column: "l"}}}} {
		n, _ := ds.Count(bg, v)
		seen := map[int64]int{}
		for start := int64(0); start < n; start += 37 {
			w := mustFetch(t, ds, v, start, 37, []string{"id"})
			for _, id := range w.Cols["id"] {
				seen[id.(int64)]++
			}
		}
		if int64(len(seen)) != n {
			t.Errorf("%+v: %d distinct rows of %d", v, len(seen), n)
		}
		for id, c := range seen {
			if c != 1 {
				t.Errorf("%+v: row %d %d times", v, id, c)
			}
		}
	}
}

// The guard for DuckDB's own PIVOT statements: a query that DuckDB splits
// into several statements is allowed only with no ; of its own and a PIVOT.
func TestPivotGuard(t *testing.T) {
	dir := t.TempDir()
	_, ds0 := fixture(t)
	p := filepath.Join(dir, "t.parquet")
	duckExec(t, ds0, "COPY (SELECT i AS a, ['x', 'y'][i % 2 + 1] AS s FROM range(5) r(i)) TO "+quoteStr(p)+" (FORMAT parquet)")
	ds, err := Open(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	pwn := filepath.Join(dir, "pwn.csv")
	for _, q := range []string{
		"select 1) ; COPY (SELECT 1) TO " + quoteStr(pwn) + " ; PIVOT t ON s USING count(*",
		"select 1) ;; COPY (SELECT 1) TO " + quoteStr(pwn) + " ;; PIVOT t ON s USING count(*",
		"select xe'\\') ; COPY (SELECT 1) TO " + quoteStr(pwn) + " ; PIVOT t ON s USING count(*",
	} {
		v := View{SQL: q}
		if _, err := ds.Validate(bg, v); err == nil {
			t.Errorf("Validate(%q) accepted", q)
		}
		if _, err := ds.Fetch(bg, v, 0, 5, nil); err == nil {
			t.Errorf("Fetch(%q) ran", q)
		}
		if _, err := ds.Count(bg, v); err == nil {
			t.Errorf("Count(%q) ran", q)
		}
	}
	if _, err := os.Stat(pwn); err == nil {
		t.Fatal("a smuggled COPY ran")
	}
	if n, err := ds.Count(bg, View{SQL: "PIVOT t ON s USING count(*)"}); err != nil || n != 5 {
		t.Fatalf("PIVOT: %d %v", n, err)
	}
	for q, want := range map[string]bool{
		"pivot t on s using count(*)":           true,
		"PIVOT t ON s USING count(*)":           true,
		"select 1":                              false, // no PIVOT
		"select 'pivot'":                        false, // (in a literal)
		"pivot t on s; select 1":                false,
		"pivot t on s;; select 1":               false,
		"select xe'\\' ; pivot t on s":          false, // xe is a name, not E'': the ; is outside
		"select e'\\' ; x' from (pivot t on s)": true,  // E'': \' is a quote, the ; is inside
		"select 1 -- ; \n pivot t on s":         true,
		"select \"a;\" from (pivot t on s)":     true,
	} {
		if got := implicitStatementsOnly(q); got != want {
			t.Errorf("implicitStatementsOnly(%q) = %v", q, got)
		}
	}
	// the same lexer guards filters: xe'\' is a name and a one-character string
	if _, err := whereSQL("s = xe'\\' ) OR (1"); err == nil || !strings.Contains(err.Error(), "closes nothing") {
		t.Errorf("xe'\\': %v", err)
	}
}
