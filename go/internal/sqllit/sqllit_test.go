package sqllit

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"math/big"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"strconv"
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
		{float32(0.1), "x = 0.10000000149011612e0"},
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
	for _, v := range []data.Value{[]byte("x"), data.List{}, data.Struct{}, data.TimeOfDay(5),
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
		{"  a = 1 ", "b = 1", "(a = 1) and b = 1"},
		{"a = 1 OR c = 2", "b = 1", "(a = 1 OR c = 2) and b = 1"},
		{"band = 'r' or(band = 'g')", "b = 1", "(band = 'r' or(band = 'g')) and b = 1"},
		{"(band = 'r')or(band = 'g')", "b = 1", "((band = 'r')or(band = 'g')) and b = 1"},
		{"band = 'r' -- note", "b = 1", "(band = 'r' -- note\n) and b = 1"},
	} {
		if got := And(c[0], c[1]); got != c[2] {
			t.Errorf("And(%q, %q) = %q", c[0], c[1], got)
		}
	}
}

// Every double "=" writes is read back by DuckDB as exactly that double
// (a DECIMAL literal of 16–17 digits isn't: its cast is off by an ulp), for
// DOUBLE and REAL columns: brute force over random doubles of every scale.
func TestDoubleLiteralsAreExact(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rng := rand.New(rand.NewPCG(1, 2))
	var vals []float64
	for len(vals) < 1500 {
		f := math.Float64frombits(rng.Uint64())
		if math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		switch len(vals) % 3 { // and plenty of ordinary magnitudes
		case 1:
			f = (rng.Float64() - 0.5) * math.Pow(10, float64(rng.IntN(30)-12))
		case 2:
			f = float64(float32(rng.Float64() * 1000))
		}
		vals = append(vals, f)
	}
	for _, typ := range []string{"DOUBLE", "REAL"} {
		for lo := 0; lo < len(vals); lo += 500 {
			batch := vals[lo:min(lo+500, len(vals))]
			var rows, conds []string
			for i, f := range batch {
				if typ == "REAL" {
					f = float64(float32(f))
					if math.IsInf(f, 0) {
						f = 1
					}
				}
				// the exact value, from its shortest text (strtod, correctly rounded)
				rows = append(rows, fmt.Sprintf("(%d, '%s')", i, strconv.FormatFloat(f, 'g', -1, 64)))
				cond, _ := Equals("x", f)
				if typ == "REAL" {
					cond, _ = Equals("x", float32(f))
				}
				conds = append(conds, fmt.Sprintf("(i = %d AND %s)", i, cond))
			}
			q := fmt.Sprintf("SELECT count(*) FROM (SELECT i, s::%s AS x FROM (VALUES %s) v(i, s)) WHERE %s",
				typ, strings.Join(rows, ", "), strings.Join(conds, " OR "))
			var n int
			if err := db.QueryRow(q).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != len(batch) {
				t.Errorf("%s: %d of %d values matched", typ, n, len(batch))
			}
		}
	}
	// the value the review found (demo trailLength): a DECIMAL(17,16) literal
	if got, _ := Equals("x", 1.9101520992509673); got != "x = 1.9101520992509673e0" {
		t.Errorf("got %q", got)
	}
}

func TestIsControl(t *testing.T) {
	want := map[rune]bool{0x7f: true}
	for r := rune(0); r < 0x20; r++ {
		want[r] = true
	}
	for r := rune(0x80); r <= 0x9f; r++ {
		want[r] = true
	}
	for r := rune(0); r < 0x300; r++ {
		if IsControl(r) != want[r] {
			t.Errorf("IsControl(%U) = %v", r, !want[r])
		}
	}
	if HasControls("tab\tx") != true || HasControls("é ✓ \u202e") {
		t.Error("HasControls")
	}
	for f, want := range map[float64]string{1e-05: "1e-05", 0.0001: "0.0001", 123456789012345.0: "123456789012345.0", 1234567890123456.0: "1234567890123456.0"} {
		if got := pyRepr(f); got != want {
			t.Errorf("pyRepr(%v) = %q, want %q", f, got, want)
		}
	}
}

// The conditions "=" builds (WP12's pane cases) are SQL DuckDB runs,
// matching the value: the infinities, a UUID, controls (C0 and C1) in a
// value and a name, a keyword and a name with a space.
func TestConditionsRunInDuckDB(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t AS SELECT 'inf'::DOUBLE AS pinf, '-inf'::DOUBLE AS ninf, 'inf'::FLOAT AS f32inf,
		'nan'::DOUBLE AS nan, 0.1::FLOAT AS f, '01234567-89ab-cdef-0123-456789abcdef'::UUID AS u,
		'a' || chr(27) || '[31m' || chr(155) || 'b' AS s, 1 AS "bell` + "\x07" + `", TIMESTAMPTZ '2026-01-02 03:04:05.678901+00' AS ts,
		DATE '2026-01-01' AS "day", true AS flag, NULL::INT AS n, 's' AS "select", 2 AS "weird name"`); err != nil {
		t.Fatal(err)
	}
	uuid := data.UUID{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	cases := map[string]data.Value{"pinf": math.Inf(1), "ninf": math.Inf(-1), "f32inf": float32(math.Inf(1)),
		"nan": math.NaN(), "f": float32(0.1), "u": uuid, "s": "a\x1b[31m\u009bb", "bell\x07": int64(1),
		"ts":  data.Timestamp{T: time.Date(2026, 1, 2, 3, 4, 5, 678901000, time.UTC), Zoned: true, Unit: time.Microsecond},
		"day": data.Date(20454), "flag": true, "n": nil, "select": "s", "weird name": int64(2)}
	for name, v := range cases {
		cond, ok := Equals(name, v)
		if !ok || HasControls(cond) {
			t.Fatalf("%s: %q", name, cond)
		}
		var n int
		if err := db.QueryRow("SELECT count(*) FROM t WHERE " + cond).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s: %q: %d rows, %v", name, cond, n, err)
		}
	}
	for name, want := range map[string]string{
		"u":          "u = '01234567-89ab-cdef-0123-456789abcdef'",
		"bell\x07":   "COLUMNS(c -> c = ('bell' || chr(7))) = 1",
		"day":        `"day" = DATE '2026-01-01'`,
		"weird name": `"weird name" = 2`,
		"select":     `"select" = 's'`,
	} {
		if got, _ := Equals(name, cases[name]); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}
