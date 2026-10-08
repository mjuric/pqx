package data

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/mjuric/pqx/go/internal/golden"
)

// Golden comparisons against Python pqx's ParquetDataset on the fixtures
// (go/testdata/golden/data_*.json): fetch windows, fetch_columns, counts,
// find_row, fetch_around and validate, view by view.

var goldenFixtures = []string{"demo", "odd", "types", "hostile", "units", "casedup", "rowcol", "nulname"}

type gView struct {
	Where   string            `json:"where"`
	OrderBy []json.RawMessage `json:"order_by"`
	SQL     string            `json:"sql"`
}

func (g gView) view(t *testing.T) View {
	v := View{Where: g.Where, SQL: g.SQL}
	for _, raw := range g.OrderBy {
		var pair []any
		if err := json.Unmarshal(raw, &pair); err != nil || len(pair) != 2 {
			t.Fatalf("order_by %s: %v", raw, err)
		}
		v.OrderBy = append(v.OrderBy, Sort{Column: pair[0].(string), Desc: pair[1].(bool)})
	}
	return v
}

type gPage struct {
	Offset     int64             `json:"offset"`
	Columns    []string          `json:"columns"`
	RowNumbers []*int64          `json:"row_numbers"`
	Rows       [][]golden.Value  `json:"rows"`
	First      []golden.Value    `json:"first"`
	Types      []json.RawMessage `json:"types"`
}

// goldenValueMatch reports why Value v isn't golden value g, or "". Python's
// values are as Python holds them: timestamps and times cut to microseconds,
// UUIDs as text, durations as DuckDB's counts, wide decimals as doubles (pqx
// Go keeps them exact: an intended difference).
func goldenValueMatch(g golden.Value, v Value) string {
	bad := func() string { return fmt.Sprintf("Python %s, Go %#v", g, v) }
	switch g.Kind {
	case golden.KindNull:
		if v != nil {
			return bad()
		}
	case golden.KindInt, golden.KindUint:
		switch x := v.(type) {
		case int64:
			if g.Int.Cmp(big.NewInt(x)) != 0 {
				return bad()
			}
		case uint64:
			if g.Int.Cmp(new(big.Int).SetUint64(x)) != 0 {
				return bad()
			}
		default:
			return bad()
		}
	case golden.KindF64:
		switch x := v.(type) {
		case float64:
			if math.Float64bits(x) != math.Float64bits(g.Float) && !(math.IsNaN(x) && math.IsNaN(g.Float)) {
				return bad()
			}
		case Decimal:
			// A wide decimal: Go's is exact; Python's is a double, often
			// DuckDB's wrong one (pqx's _fix_wide_decimals gives up when
			// PyArrow can't read some other column of the page). Checked
			// against the fixture's facts in TestWideDecimals instead.
			if x.Precision <= 38 {
				return bad()
			}
		default:
			return bad()
		}
	case golden.KindF32:
		x, ok := v.(float32)
		if !ok || math.Float32bits(x) != math.Float32bits(g.Float32) && !(x != x && g.Float32 != g.Float32) {
			return bad()
		}
	case golden.KindBool:
		if x, ok := v.(bool); !ok || x != g.Bool {
			return bad()
		}
	case golden.KindStr, golden.KindUUID:
		switch x := v.(type) {
		case string:
			if x != g.Str {
				return bad()
			}
		case UUID:
			h := hex.EncodeToString(x[:])
			if h[:8]+"-"+h[8:12]+"-"+h[12:16]+"-"+h[16:20]+"-"+h[20:] != g.Str {
				return bad()
			}
		default:
			return bad()
		}
	case golden.KindBytes:
		if x, ok := v.([]byte); !ok || string(x) != string(g.Bytes) {
			return bad()
		}
	case golden.KindTS:
		x, ok := v.(Timestamp)
		// (Python's values through datetime stop at microseconds)
		if !ok || !(x.T.Equal(g.Time) || x.T.Truncate(time.Microsecond).Equal(g.Time) && g.Time.Nanosecond()%1000 == 0) || x.Zoned != (g.TZ != "") {
			return bad()
		}
		unit := map[time.Duration]string{time.Second: "s", time.Millisecond: "ms", time.Microsecond: "us", time.Nanosecond: "ns"}[x.Unit]
		if unit != g.Unit {
			return fmt.Sprintf("unit %s, Python %s: %s", unit, g.Unit, bad())
		}
	case golden.KindDate:
		if x, ok := v.(Date); !ok || !x.Time().Equal(g.Time) {
			return bad()
		}
	case golden.KindTime:
		if x, ok := v.(TimeOfDay); !ok || int64(x) != g.Nanos && (int64(x)/1000*1000 != g.Nanos || g.Nanos%1000 != 0) {
			return bad()
		}
	case golden.KindDec:
		x, ok := v.(Decimal)
		if !ok || x.Unscaled.Cmp(g.Int) != 0 || int(x.Scale) != g.Scale || int(x.Precision) != g.Precision {
			return bad()
		}
	case golden.KindList:
		x, ok := v.(List)
		if !ok || len(x) != len(g.List) {
			return bad()
		}
		for i := range x {
			if why := goldenValueMatch(g.List[i], x[i]); why != "" {
				return fmt.Sprintf("[%d]: %s", i, why)
			}
		}
	case golden.KindStruct:
		x, ok := v.(Struct)
		if !ok || len(x) != len(g.Fields) {
			return bad()
		}
		for i := range x {
			if x[i].Name != g.Fields[i].Name {
				return bad()
			}
			if why := goldenValueMatch(g.Fields[i].Value, x[i].Value); why != "" {
				return fmt.Sprintf(".%s: %s", x[i].Name, why)
			}
		}
	case golden.KindMap:
		x, ok := v.(Map)
		if !ok || len(x) != len(g.Entries) {
			return bad()
		}
		for i := range x {
			if why := goldenValueMatch(g.Entries[i].Key, x[i].Key); why != "" {
				return why
			}
			if why := goldenValueMatch(g.Entries[i].Value, x[i].Value); why != "" {
				return why
			}
		}
	default:
		return bad()
	}
	return ""
}

// comparePage checks a Window against Python's page of the same call.
func comparePage(t *testing.T, id string, d *dataset, v View, g gPage, w Window, cols []string) {
	t.Helper()
	if w.Start != g.Offset {
		t.Errorf("%s: Start %d, Python %d", id, w.Start, g.Offset)
	}
	if w.Len != len(g.Rows) {
		t.Errorf("%s: Len %d, Python %d", id, w.Len, len(g.Rows))
		return
	}
	for i, rn := range g.RowNumbers {
		switch {
		case rn == nil && v.IsSQL():
			if w.FileRows != nil {
				t.Errorf("%s: FileRows %v for a SQL view", id, w.FileRows)
			}
		case rn == nil:
			if w.FileRows[i] != -1 {
				t.Errorf("%s: FileRows[%d] = %d, Python None", id, i, w.FileRows[i])
			}
		case w.FileRows == nil || w.FileRows[i] != *rn:
			t.Errorf("%s: FileRows[%d] = %v, Python %d", id, i, w.FileRows, *rn)
			return
		}
	}
	if len(w.Failed) > 0 {
		t.Errorf("%s: failed %v", id, w.Failed)
	}
	// Python can't break ties in a sort of a file DuckDB can't number: compare
	// such windows as sets of rows.
	unordered := len(v.OrderBy) > 0 && !d.hasRowNum
	if unordered {
		// the sort keys are in the same order; rows that tie may differ
		keys := map[string]bool{}
		for _, k := range v.OrderBy {
			keys[k.Column] = true
		}
		for k, c := range g.Columns {
			if !keys[c] {
				continue
			}
			for i := range g.Rows {
				if why := goldenValueMatch(g.Rows[i][k], w.Cols[c][i]); why != "" {
					t.Errorf("%s %s row %d: %s", id, c, i, why)
				}
			}
		}
		return
	}
	for k, c := range g.Columns {
		vals, ok := w.Cols[c]
		if !ok {
			t.Errorf("%s: no column %q (asked %v)", id, c, cols)
			continue
		}
		for i := range g.Rows {
			if why := goldenValueMatch(g.Rows[i][k], vals[i]); why != "" {
				t.Errorf("%s %s row %d: %s", id, c, i, why)
			}
		}
	}
}

// goldenGoBetter lists calls where Python raised and pqx Go answers, on
// purpose: arrow-go reads a plain view of a file with a NUL in a column name
// (SQL can't name it; only views that need SQL fail).
func goldenGoBetter(fixture string, v View) bool {
	return fixture == "nulname" && (v.Plain() || validating)
}

// validating: Validate of a filter of nulname checks the filter alone (with
// count(*)), where Python's validate selects every column.
var validating bool

func TestGoldenData(t *testing.T) {
	ctx := context.Background()
	for _, fx := range goldenFixtures {
		t.Run(fx, func(t *testing.T) {
			f := golden.Load(t, "data_"+fx+".json")
			d := openFixture(t, fx+".parquet")
			all := colNames(d)

			for _, r := range f.Section(t, "count") {
				var gv gView
				r.Decode(t, "view", &gv)
				v := gv.view(t)
				n, err := d.Count(ctx, v)
				if msg, ok := r.Err(); ok {
					if err == nil && !goldenGoBetter(fx, v) {
						t.Errorf("%s: Count %d, Python %s", r.ID(), n, msg)
					}
					continue
				}
				if err != nil || n != int64(r.Int(t, "out")) {
					t.Errorf("%s %+v: Count %d %v, Python %d", r.ID(), v, n, err, r.Int(t, "out"))
				}
			}

			for _, r := range f.Section(t, "validate") {
				var gv gView
				r.Decode(t, "view", &gv)
				v := gv.view(t)
				got, err := d.Validate(ctx, v)
				if msg, ok := r.Err(); ok {
					validating = !v.IsSQL() && len(v.OrderBy) == 0
					if err == nil && !goldenGoBetter(fx, v) {
						t.Errorf("%s: Validate ok, Python %s", r.ID(), msg)
					}
					if err != nil && HasControls(err.Error()) {
						t.Errorf("%s: error with control characters: %q", r.ID(), err)
					}
					continue
				}
				var want [][2]string
				r.Decode(t, "out", &want)
				if err != nil || len(got) != len(want) {
					t.Errorf("%s %+v: %d columns %v, Python %d", r.ID(), v, len(got), err, len(want))
					continue
				}
				for i := range want {
					if got[i].Name != want[i][0] {
						t.Errorf("%s: column %d %q, Python %q", r.ID(), i, got[i].Name, want[i][0])
					}
				}
			}

			for _, r := range f.Section(t, "fetch") {
				var gv gView
				r.Decode(t, "view", &gv)
				v := gv.view(t)
				var cols []string
				r.Decode(t, "columns", &cols)
				if cols == nil {
					if v.IsSQL() {
						vc, err := d.Validate(ctx, v)
						if err == nil {
							for _, c := range vc {
								cols = append(cols, c.Name)
							}
						}
					} else {
						cols = all
					}
				}
				w, err := d.Fetch(ctx, v, int64(r.Int(t, "offset")), r.Int(t, "limit"), cols)
				if msg, ok := r.Err(); ok {
					if err == nil && !goldenGoBetter(fx, v) {
						t.Errorf("%s: Fetch ok, Python %s", r.ID(), msg)
					}
					continue
				}
				if err != nil {
					t.Errorf("%s %+v: %v", r.ID(), v, err)
					continue
				}
				var g gPage
				r.Decode(t, "out", &g)
				comparePage(t, r.ID(), d, v, g, w, cols)
			}

			for _, r := range f.Section(t, "fetch_columns") {
				var rows []int64
				var cols []string
				r.Decode(t, "file_rows", &rows)
				r.Decode(t, "columns", &cols)
				w, err := d.FetchColumns(ctx, rows, cols)
				if msg, ok := r.Err(); ok {
					if err == nil {
						t.Errorf("%s: FetchColumns ok, Python %s", r.ID(), msg)
					}
					continue
				}
				if err != nil {
					t.Errorf("%s: %v", r.ID(), err)
					continue
				}
				var g gPage
				r.Decode(t, "out", &g)
				comparePage(t, r.ID(), d, View{}, g, w, cols)
			}

			for _, r := range f.Section(t, "find_row") {
				var gv gView
				r.Decode(t, "view", &gv)
				v := gv.view(t)
				fr := int64(r.Int(t, "file_row"))
				pos, found, err := d.FindRow(ctx, v, fr)
				if msg, ok := r.Err(); ok {
					if err == nil {
						t.Errorf("%s: FindRow ok, Python %s", r.ID(), msg)
					}
					continue
				}
				if err != nil {
					t.Errorf("%s: %v", r.ID(), err)
					continue
				}
				if r.IsNull("out") {
					if found {
						t.Errorf("%s %+v row %d: found at %d, Python None", r.ID(), v, fr, pos)
					}
				} else if !found || pos != int64(r.Int(t, "out")) {
					t.Errorf("%s %+v row %d: %d %v, Python %d", r.ID(), v, fr, pos, found, r.Int(t, "out"))
				}
			}

			for _, r := range f.Section(t, "fetch_around") {
				var gv gView
				r.Decode(t, "view", &gv)
				v := gv.view(t)
				var cols []string
				r.Decode(t, "columns", &cols)
				if cols == nil {
					cols = all
				}
				w, err := d.FetchAround(ctx, v, int64(r.Int(t, "file_row")), int64(r.Int(t, "pos")), int64(r.Int(t, "offset")), r.Int(t, "limit"), cols)
				if msg, ok := r.Err(); ok {
					if err == nil {
						t.Errorf("%s: FetchAround ok, Python %s", r.ID(), msg)
					}
					continue
				}
				if err != nil {
					t.Errorf("%s: %v", r.ID(), err)
					continue
				}
				var g gPage
				r.Decode(t, "out", &g)
				if w.Start != g.Offset || len(w.FileRows) != len(g.RowNumbers) {
					t.Errorf("%s: Start %d, %d rows; Python %d, %d", r.ID(), w.Start, len(w.FileRows), g.Offset, len(g.RowNumbers))
					continue
				}
				for i, rn := range g.RowNumbers {
					if rn == nil || w.FileRows[i] != *rn {
						t.Errorf("%s: FileRows[%d] = %d, Python %v", r.ID(), i, w.FileRows[i], rn)
						break
					}
				}
				for k, c := range cols {
					if k < len(g.First) && len(w.Cols[c]) > 0 {
						if why := goldenValueMatch(g.First[k], w.Cols[c][0]); why != "" {
							t.Errorf("%s %s first row: %s", r.ID(), c, why)
						}
					}
				}
			}
		})
	}
}
