package golden

import (
	"encoding/json"
	"math"
	"math/big"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestEveryFileParses loads every golden file and decodes every typed value and
// every Rich text in it: the encoding is what this package expects.
func TestEveryFileParses(t *testing.T) {
	names, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cells.json", "data_casedup.json", "data_common.json", "data_demo.json", "data_hostile.json",
		"data_nulname.json", "data_odd.json", "data_rowcol.json", "data_types.json", "data_units.json",
		"fmt.json", "plots.json"}
	if !slices.Equal(names, want) {
		t.Fatalf("golden files %v, want %v", names, want)
	}
	kinds := map[string]int{}
	for _, name := range names {
		f := Load(t, name)
		if f.Header.File != name || f.Header.Versions["pyarrow"] == "" || f.Header.Versions["duckdb"] == "" {
			t.Errorf("%s: header %+v", name, f.Header)
		}
		if len(f.Order) == 0 {
			t.Errorf("%s: no sections", name)
		}
		values, texts := 0, 0
		for _, sec := range f.Order {
			for i, r := range f.Sections[sec] {
				if id := r.ID(); id != sec+"/"+itoa(i) && !strings.HasPrefix(id, sec+"/") {
					t.Errorf("%s: record %d of %s has id %q", name, i, sec, id)
				}
				_, hasErr := r.Err()
				if r.Has("out") && hasErr {
					t.Errorf("%s: %s has both out and error", name, r.ID())
				}
				for key, raw := range r {
					var tree any
					if err := json.Unmarshal(raw, &tree); err != nil {
						t.Fatalf("%s: %s.%s: %v", name, r.ID(), key, err)
					}
					walk(t, name+": "+r.ID()+"."+key, tree, kinds, &values, &texts)
				}
			}
		}
		t.Logf("%s: %d sections, %d typed values, %d texts", name, len(f.Order), values, texts)
	}
	for _, k := range Kinds {
		if kinds[k] == 0 {
			t.Errorf("no typed value of kind %q in any file", k)
		}
	}
}

func itoa(i int) string {
	return big.NewInt(int64(i)).String()
}

var kindSet = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range Kinds {
		m[k] = true
	}
	return m
}()

// walk decodes every typed value ({"t": kind, ...}) and Rich text ({"text", "style",
// "spans"}) found in tree, counting them by kind.
func walk(t *testing.T, where string, tree any, kinds map[string]int, values, texts *int) {
	switch x := tree.(type) {
	case map[string]any:
		if k, ok := x["t"].(string); ok {
			if !kindSet[k] {
				t.Errorf("%s: unknown kind %q", where, k)
				return
			}
			b, _ := json.Marshal(x)
			var v Value
			if err := json.Unmarshal(b, &v); err != nil {
				t.Errorf("%s: %v", where, err)
				return
			}
			countKinds(v, kinds)
			*values++
			return // (decoding it decoded what's inside)
		}
		if _, ok := x["spans"]; ok {
			if _, ok := x["text"]; ok {
				b, _ := json.Marshal(x)
				var tx Text
				if err := json.Unmarshal(b, &tx); err != nil {
					t.Errorf("%s: %v", where, err)
					return
				}
				n := len([]rune(tx.Text))
				for _, s := range tx.Spans {
					if s.Start < 0 || s.End > n || s.Start >= s.End {
						t.Errorf("%s: span %+v outside text of %d runes", where, s, n)
					}
				}
				*texts++
				return
			}
		}
		for k, v := range x {
			walk(t, where+"."+k, v, kinds, values, texts)
		}
	case []any:
		for _, v := range x {
			walk(t, where, v, kinds, values, texts)
		}
	}
}

func countKinds(v Value, kinds map[string]int) {
	kinds[v.Kind]++
	for _, x := range v.List {
		countKinds(x, kinds)
	}
	for _, f := range v.Fields {
		countKinds(f.Value, kinds)
	}
	for _, e := range v.Entries {
		countKinds(e.Key, kinds)
		countKinds(e.Value, kinds)
	}
}

func TestDecodeValues(t *testing.T) {
	dec := func(s string) Value {
		t.Helper()
		var v Value
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		return v
	}
	if v := dec(`{"t":"null"}`); !v.IsNull() {
		t.Errorf("null: %v", v)
	}
	if v := dec(`{"t":"int","v":"-9223372036854775808"}`); v.Int.Int64() != math.MinInt64 {
		t.Errorf("int: %v", v)
	}
	if v := dec(`{"t":"uint","v":"18446744073709551615"}`); func() bool { u, ok := v.Uint64(); return !ok || u != math.MaxUint64 }() {
		t.Errorf("uint: %v", v)
	}
	if v := dec(`{"t":"f64","v":"-0.0"}`); v.Float != 0 || !math.Signbit(v.Float) {
		t.Errorf("-0.0: %v", v)
	}
	if v := dec(`{"t":"f64","v":"nan"}`); !math.IsNaN(v.Float) {
		t.Errorf("nan: %v", v)
	}
	if v := dec(`{"t":"f64","v":"-inf"}`); !math.IsInf(v.Float, -1) {
		t.Errorf("-inf: %v", v)
	}
	if v := dec(`{"t":"f32","v":"0.10000000149011612"}`); v.Float32 != float32(0.1) {
		t.Errorf("f32: %v", v)
	}
	if v := dec(`{"t":"bytes","v":""}`); v.Bytes == nil || len(v.Bytes) != 0 {
		t.Errorf("empty bytes: %#v", v.Bytes)
	}
	v := dec(`{"t":"ts","v":"2020-09-13T12:26:40.123456789","unit":"ns","tz":null}`)
	if !v.Time.Equal(time.Date(2020, 9, 13, 12, 26, 40, 123456789, time.UTC)) || v.TZ != "" || v.Unit != "ns" {
		t.Errorf("ts: %v", v)
	}
	if v := dec(`{"t":"ts","v":"0001-01-01T00:00:00.000000000","unit":"us","tz":"UTC"}`); v.Time.Year() != 1 || v.TZ != "UTC" {
		t.Errorf("ts year 1: %v", v)
	}
	if v := dec(`{"t":"time","v":"23:59:59.999999999"}`); v.Nanos != 86400e9-1 {
		t.Errorf("time: %v", v)
	}
	if v := dec(`{"t":"dur","v":"-30000000000"}`); func() bool { d, ok := v.Duration(); return !ok || d != -30*time.Second }() {
		t.Errorf("dur: %v", v)
	}
	v = dec(`{"t":"dec","v":"-30","scale":2,"precision":9}`)
	if r, _ := v.Rat(); r.Cmp(big.NewRat(-3, 10)) != 0 || v.Precision != 9 {
		t.Errorf("dec: %v", v)
	}
	v = dec(`{"t":"struct","v":[["a",{"t":"int","v":"1"}],["b",{"t":"list","v":[]}]]}`)
	if len(v.Fields) != 2 || v.Fields[1].Name != "b" || v.Fields[1].Value.List == nil {
		t.Errorf("struct: %v", v)
	}
	v = dec(`{"t":"map","v":[[{"t":"str","v":"k"},{"t":"null"}]]}`)
	if len(v.Entries) != 1 || v.Entries[0].Key.Str != "k" || !v.Entries[0].Value.IsNull() {
		t.Errorf("map: %v", v)
	}
	for _, bad := range []string{`{"t":"what"}`, `{"t":"int","v":"1.5"}`, `{"t":"uint","v":"-1"}`,
		`{"t":"f32","v":"0.1"}`, `{"t":"ts","v":"2020-01-01T00:00:00","unit":"us","tz":null}`,
		`{"t":"ts","v":"2020-01-01T00:00:00.000000000","unit":"us","tz":"Europe/Berlin"}`,
		`{"t":"time","v":"24:00:00.000000000"}`, `{"t":"dec","v":"1"}`, `{"t":"str","v":"x","extra":1}`} {
		var v Value
		if err := json.Unmarshal([]byte(bad), &v); err == nil {
			t.Errorf("%s decoded as %v", bad, v)
		}
	}
}

// TestFixtureFacts checks a few facts the README promises against the golden files.
func TestFixtureFacts(t *testing.T) {
	for _, name := range []string{"demo.parquet", "odd.parquet", "types.parquet", "hostile.parquet",
		"units.parquet", "casedup.parquet", "rowcol.parquet", "nulname.parquet"} {
		if _, err := os.Stat(Fixture(name)); err != nil {
			t.Error(err)
		}
	}
	f := Load(t, "data_types.json")
	cols := map[string][]Value{}
	for _, r := range f.Section(t, "file_table") {
		if r.IsNull("values") {
			continue
		}
		var vals []Value
		r.Decode(t, "values", &vals)
		cols[r.String(t, "name")] = vals
	}
	if len(cols["i8"]) != 60 {
		t.Fatalf("types: %d rows of i8", len(cols["i8"]))
	}
	for name, vals := range cols {
		if name != "null" && !vals[10].IsNull() {
			t.Errorf("types.%s[10] = %v, want NULL", name, vals[10])
		}
	}
	if u, ok := cols["u64"][1].Uint64(); !ok || u != math.MaxUint64 {
		t.Errorf("u64[1] = %v", cols["u64"][1])
	}
	if ts := cols["ts_ns"][0]; ts.Time.Nanosecond() != 123456789 || ts.Unit != "ns" {
		t.Errorf("ts_ns[0] = %v", ts)
	}
	if d := cols["dec76_10"][1]; d.Precision != 76 || d.Scale != 10 || d.Int.String() != "-10000000000000000000000000000000000000000000000000000000000000001" {
		t.Errorf("dec76_10[1] = %v", d)
	}

	fm := Load(t, "fmt.json")
	n := 0
	for _, r := range fm.Section(t, "format_value") {
		var cases [][4]json.RawMessage
		r.Decode(t, "cases", &cases)
		for _, c := range cases {
			if _, err := DecodeOverride(c[2]); err != nil {
				t.Fatalf("%s: override %s: %v", r.ID(), c[2], err)
			}
			n++
		}
	}
	if n < 10000 {
		t.Errorf("only %d format_value cases", n)
	}

	pl := Load(t, "plots.json")
	r := pl.Section(t, "render_histogram")[0]
	tx := r.Text(t, "out")
	if !strings.Contains(tx.Text, "100 ┤") || len(tx.CharStyles()) != len([]rune(tx.Text)) {
		t.Errorf("render_histogram/0: %q", tx.Text)
	}
}
