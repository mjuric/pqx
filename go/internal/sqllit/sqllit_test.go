package sqllit

import (
	"context"
	"database/sql"
	"math"
	"math/big"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"

	"github.com/mjuric/pqx/go/internal/data"
)

// The keywords built in are those of the DuckDB pqx is built with.
func TestKeywordsAreDuckDBs(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT lower(keyword_name) FROM duckdb_keywords()")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	live := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		live[k] = true
	}
	if !reflect.DeepEqual(live, Keywords) {
		t.Fatalf("DuckDB has %d keywords, keywords.go %d: regenerate keywords.go", len(live), len(Keywords))
	}
	for _, k := range []string{"select", "from", "order", "asof", "qualify"} {
		if Ident(k) != `"`+k+`"` {
			t.Errorf("%s: %s", k, Ident(k))
		}
	}
	for _, k := range []string{"ra", "band", "detector", "mag", "x_1", "_a"} {
		if Ident(k) != k {
			t.Errorf("%s: %s", k, Ident(k))
		}
	}
	if Ident("a.b") != `"a.b"` || Ident("1a") != `"1a"` {
		t.Error("quoting")
	}
}

// tests/test_security.py::test_sql_helpers.
func TestSQLHelpers(t *testing.T) {
	if Ident("band") != "band" || Ident("select") != `"select"` || Ident("a b") != `"a b"` || Ident(`q"x`) != `"q""x"` {
		t.Error("SQLIdent")
	}
	ds, err := data.Open(filepath.Join("..", "..", "testdata", "fixtures", "hostile.parquet"), data.Options{Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	ctx := context.Background()
	w, err := ds.Fetch(ctx, data.View{}, 0, int(ds.NumRows()), []string{"s"})
	if err != nil {
		t.Fatal(err)
	}
	vals := append([]string{"plain", "", "it''s", "nul\x00x"}, func() []string {
		var out []string
		for _, v := range w.Cols["s"] {
			out = append(out, v.(string))
		}
		return out
	}()...)
	for _, s := range vals {
		lit := TextLiteral(s)
		if HasControls(lit) {
			t.Errorf("TextLiteral(%q) = %q holds controls", s, lit)
			continue
		}
		got, err := ds.Fetch(ctx, data.View{SQL: "select " + lit + " as v"}, 0, 1, []string{"v"})
		if err != nil || got.Len != 1 || got.Cols["v"][0] != s {
			t.Errorf("select %s: %v %+v (want %q)", lit, err, got.Cols["v"], s)
		}
	}
	// a name with control characters, by COLUMNS(...)
	name := "esc\x1b]0;PWNED-TITLE\x07name"
	ref := ColumnRef(name)
	if HasControls(ref) || !strings.HasPrefix(ref, "COLUMNS(c -> c = (") {
		t.Fatalf("ColumnRef = %q", ref)
	}
	if n, err := ds.Count(ctx, data.View{Where: ref + " = 3"}); err != nil || n != 1 {
		t.Errorf("%s = 3: %d, %v", ref, n, err)
	}
	if ColumnRef("bidi\u202ename") != "\"bidi\u202ename\"" {
		t.Errorf("ColumnRef with a bidi control (not C0/C1): %q", ColumnRef("bidi\u202ename"))
	}
}

func TestEqualsLiterals(t *testing.T) {

	ts := time.Date(2026, 1, 2, 3, 4, 5, 678_000_000, time.UTC)
	for _, c := range []struct {
		v    data.Value
		want string
	}{
		{nil, "x IS NULL"},
		{true, "x = true"},
		{false, "x = false"},
		{int64(-3), "x = -3"},
		{uint64(18446744073709551615), "x = 18446744073709551615"},
		{1.5, "x = 1.5"},
		{100.0, "x = 100.0"},
		{1e16, "x = 1e+16"},
		{1.5e-7, "x = 1.5e-07"},
		{0.0001, "x = 0.0001"},
		{math.Copysign(0, -1), "x = -0.0"},
		{float32(0.1), "x = 0.10000000149011612"},
		{math.NaN(), "isnan(x)"},
		{float32(math.NaN()), "isnan(x)"},
		{math.Inf(1), "x = 'inf'::DOUBLE"},
		{math.Inf(-1), "x = '-inf'::DOUBLE"},
		{"it's", "x = 'it''s'"},
		{"a\x1bb", "x = ('a' || chr(27) || 'b')"},
		{data.Date(0), "x = DATE '1970-01-01'"},
		{data.Timestamp{T: ts, Unit: time.Millisecond}, "x = TIMESTAMP '2026-01-02T03:04:05.678000'"},
		{data.Timestamp{T: ts.Truncate(time.Second), Unit: time.Microsecond, Zoned: true}, "x = TIMESTAMPTZ '2026-01-02T03:04:05+00:00'"},
		{data.Timestamp{T: ts.Add(9), Unit: time.Nanosecond}, "x = TIMESTAMP_NS '2026-01-02T03:04:05.678000009'"},
		{data.Timestamp{T: ts.Add(9), Unit: time.Nanosecond, Zoned: true}, "x = TIMESTAMPTZ '2026-01-02T03:04:05.678000009+00:00'"},
		{data.Decimal{Unscaled: big.NewInt(-5), Scale: 2, Precision: 9}, "x = -0.05"},
		{data.Decimal{Unscaled: big.NewInt(12345), Scale: 0, Precision: 9}, "x = 12345"},
	} {
		got, ok := Equals("x", c.v)
		if !ok || got != c.want {
			t.Errorf("Equals(%#v) = %q, %v; want %q", c.v, got, ok, c.want)
		}
	}
	for _, v := range []data.Value{[]byte("x"), data.List{}, data.Struct{}, data.TimeOfDay(5), data.UUID{},
		data.Decimal{Unscaled: big.NewInt(1), Precision: 50}} {
		if got, ok := Equals("x", v); ok {
			t.Errorf("Equals(%#v) = %q: should be refused", v, got)
		}
	}
	// DuckDB's name for a case duplicate; a keyword quoted
	if got, _ := Equals("name_1", "a"); got != "name_1 = 'a'" {
		t.Errorf("case duplicate: %q", got)
	}
	if got, _ := Equals("select", int64(1)); got != `"select" = 1` {
		t.Errorf("keyword: %q", got)
	}
}

func TestAnd(t *testing.T) {
	for _, c := range [][3]string{
		{"", "b = 1", "b = 1"},
		{"  a = 1 ", "b = 1", "a = 1 and b = 1"},
		{"a = 1 OR c = 2", "b = 1", "(a = 1 OR c = 2) and b = 1"},
		{"orbit = 1", "b = 1", "orbit = 1 and b = 1"},
	} {
		if got := And(c[0], c[1]); got != c[2] {
			t.Errorf("And(%q, %q) = %q", c[0], c[1], got)
		}
	}
}
