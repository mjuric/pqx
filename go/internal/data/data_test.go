package data

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOpenSchema(t *testing.T) {
	_, ds := fixture(t)
	if ds.NumRows() != fixRows {
		t.Fatalf("NumRows = %d", ds.NumRows())
	}
	if got := ds.RowGroups(); !reflect.DeepEqual(got, []int64{300, 300, 300, 100}) {
		t.Fatalf("RowGroups = %v", got)
	}
	want := map[string]string{
		"id": "BIGINT", "i32": "INTEGER", "f32": "FLOAT", "f64": "DOUBLE", "s": "VARCHAR",
		"ts": "TIMESTAMP WITH TIME ZONE", "tsl": "TIMESTAMP", "b": "BOOLEAN", "Name": "BIGINT", "name": "BIGINT",
	}
	for _, c := range ds.Columns() {
		if w, ok := want[c.Name]; ok && c.Type != w {
			t.Errorf("%s: type %s, want %s", c.Name, c.Type, w)
		}
	}
	// and they're DuckDB's own names for them
	d := ds.(*dataset)
	rows, err := d.db.Query("SELECT column_type FROM (DESCRIBE SELECT * FROM read_parquet(" + pathLiteral(d.path) + "))")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		if typ != d.cols[i].Type {
			t.Errorf("%s: type %s, DuckDB says %s", d.cols[i].Name, d.cols[i].Type, typ)
		}
		i++
	}
	if i != len(d.cols) {
		t.Fatalf("DuckDB has %d columns, Open %d", i, len(d.cols))
	}
}

func TestOpenMissing(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope.parquet"), Options{}); err == nil {
		t.Fatal("no error for a missing file")
	}
	p := filepath.Join(t.TempDir(), "junk.parquet")
	os.WriteFile(p, []byte("not parquet at all"), 0o600)
	if _, err := Open(p, Options{}); err == nil {
		t.Fatal("no error for a file that isn't Parquet")
	}
}

// expected cell text of the fixture's columns at file row i
func fixText(col string, i int) string {
	switch col {
	case "id", "Name":
		return strconv.Itoa(i)
	case "name":
		return strconv.Itoa(-i)
	case "i32":
		if i%7 == 3 {
			return Null
		}
		return strconv.Itoa(i * 3)
	case "f32":
		return strconv.FormatFloat(float64(float32(i)/3), 'g', 6, 32)
	case "f64":
		if i%5 == 0 {
			return Null
		}
		return strconv.FormatFloat(float64(i)*1.0000001e10, 'g', 6, 64)
	case "s":
		if i%11 == 0 {
			return Null
		}
		return Sanitize(fmt.Sprintf("%s#%d", hostile[i%len(hostile)], i))
	case "ts", "tsl":
		ts := fixEpoch.Add(time.Duration(i) * 1500 * time.Millisecond).Add(time.Duration(i%3) * time.Microsecond)
		s := ts.Format("2006-01-02 15:04:05")
		if ts.Nanosecond() != 0 {
			s += fmt.Sprintf(".%06d", ts.Nanosecond()/1000)
		}
		if col == "ts" {
			s += "Z"
		}
		return s
	case "b":
		return strconv.FormatBool(i%2 == 0)
	case "with space \"q\"":
		return strconv.Itoa(i % 100)
	}
	panic(col)
}

var windows = []struct {
	start int64
	n     int
}{
	{0, 10},      // start of the first row group
	{120, 50},    // inside one row group, a few pages in
	{295, 10},    // across a row-group boundary
	{250, 400},   // across two boundaries
	{900, 100},   // the whole last row group
	{990, 50},    // short: past the end
	{999, 1},     // the last row
	{1000, 10},   // at the end: empty
	{5000, 10},   // beyond the end: empty
	{0, fixRows}, // everything
	{10, 0},      // nothing asked for
}

func TestPlainWindows(t *testing.T) {
	_, ds := fixture(t)
	cols := colNames(ds)
	for _, w := range windows {
		got, err := ds.Fetch(context.Background(), View{}, w.start, w.n, cols)
		if err != nil {
			t.Fatalf("%v: %v", w, err)
		}
		n := max(0, min(int64(w.n), fixRows-w.start))
		if got.Start != w.start || got.Len != int(n) || len(got.FileRows) != int(n) {
			t.Fatalf("%v: Start %d Len %d FileRows %d", w, got.Start, got.Len, len(got.FileRows))
		}
		for _, c := range cols {
			if len(got.Cols[c]) != int(n) {
				t.Fatalf("%v %s: %d cells", w, c, len(got.Cols[c]))
			}
			for k := range int(n) {
				i := int(w.start) + k
				if got.FileRows[k] != int64(i) {
					t.Fatalf("%v: FileRows[%d] = %d", w, k, got.FileRows[k])
				}
				if want := fixText(c, i); got.Cols[c][k] != want {
					t.Fatalf("%v %s row %d: %q, want %q", w, c, i, got.Cols[c][k], want)
				}
			}
		}
	}
}

func TestFetchColumnSubsetAndOrder(t *testing.T) {
	_, ds := fixture(t)
	cols := []string{"s", "id", "s", "name"}
	for _, v := range []View{{}, {Where: "true"}} {
		got, err := ds.Fetch(context.Background(), v, 595, 10, cols)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Cols) != 3 || got.Cols["id"][0] != "595" || got.Cols["name"][9] != "-604" || got.Cols["s"][1] != fixText("s", 596) {
			t.Fatalf("%v: %v", v, got.Cols)
		}
	}
	if _, err := ds.Fetch(context.Background(), View{}, 0, 1, []string{"nope"}); err == nil {
		t.Fatal("no error for an unknown column")
	}
}

// The plain view (arrow-go), the same rows through DuckDB, and a filtered
// view that keeps every row agree cell for cell.
func TestPlainAndDuckDBAgree(t *testing.T) {
	_, ds := fixture(t)
	d := ds.(*dataset)
	cols := colNames(ds)
	idx, _ := d.columnIndices(cols)
	ctx := context.Background()
	for _, w := range windows {
		plain, err := ds.Fetch(ctx, View{}, w.start, w.n, cols)
		if err != nil {
			t.Fatal(err)
		}
		all, err := ds.Fetch(ctx, View{Where: "true"}, w.start, w.n, cols)
		if err != nil {
			t.Fatal(err)
		}
		duck, err := d.fetchPlainDuck(ctx, w.start, w.n, cols, idx)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(plain, all) {
			t.Fatalf("%v: plain and filtered differ:\n%v\n%v", w, plain, all)
		}
		if !reflect.DeepEqual(plain, duck) {
			t.Fatalf("%v: plain and DuckDB differ:\n%v\n%v", w, plain, duck)
		}
	}
}

func TestFiltered(t *testing.T) {
	_, ds := fixture(t)
	ctx := context.Background()
	v := View{Where: "id % 3 = 0 AND \"Name\" >= 0"}
	n, err := ds.Count(ctx, v)
	if err != nil || n != 334 {
		t.Fatalf("Count = %d, %v", n, err)
	}
	if n, err := ds.Count(ctx, View{}); err != nil || n != fixRows {
		t.Fatalf("plain Count = %d, %v", n, err)
	}
	if n, err := ds.Count(ctx, View{Where: "  "}); err != nil || n != fixRows {
		t.Fatalf("blank filter Count = %d, %v", n, err)
	}
	got, err := ds.Fetch(ctx, v, 98, 5, []string{"id", "name", "s"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Len != 5 || !reflect.DeepEqual(got.FileRows, []int64{294, 297, 300, 303, 306}) {
		t.Fatalf("Len %d FileRows %v", got.Len, got.FileRows)
	}
	for k, r := range got.FileRows {
		for _, c := range []string{"id", "name", "s"} {
			if got.Cols[c][k] != fixText(c, int(r)) {
				t.Fatalf("%s row %d: %q", c, r, got.Cols[c][k])
			}
		}
	}
	// the end of the view: short, then empty
	got, err = ds.Fetch(ctx, v, 330, 10, []string{"id"})
	if err != nil || got.Len != 4 || got.FileRows[3] != 999 {
		t.Fatalf("Len %d %v %v", got.Len, got.FileRows, err)
	}
	got, err = ds.Fetch(ctx, v, 334, 10, []string{"id"})
	if err != nil || got.Len != 0 || len(got.Cols["id"]) != 0 {
		t.Fatalf("Len %d %v", got.Len, err)
	}
	// NULLs and strings with control characters in a filter
	if n, _ := ds.Count(ctx, View{Where: "s IS NULL"}); n != 91 {
		t.Fatalf("NULLs: %d", n)
	}
	if n, _ := ds.Count(ctx, View{Where: "s LIKE chr(27) || '%'"}); n == 0 {
		t.Fatal("no ESC strings found")
	}
}

// Names that differ only in case each show their own data, in both views.
func TestCaseDuplicateNames(t *testing.T) {
	_, ds := fixture(t)
	for _, v := range []View{{}, {Where: "id >= 0"}} {
		got, err := ds.Fetch(context.Background(), v, 7, 2, []string{"name", "Name"})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Cols["Name"], []string{"7", "8"}) || !reflect.DeepEqual(got.Cols["name"], []string{"-7", "-8"}) {
			t.Fatalf("%v: %v", v, got.Cols)
		}
	}
}

func TestCheckWhere(t *testing.T) {
	path, ds := fixture(t)
	pwn := filepath.Join(filepath.Dir(path), "pwn.txt")
	bad := []string{
		"i32 = 3) OR (s = 'b'",
		"(id > 3",
		"id > 3)",
		"id > 0; SELECT 1",
		"id > 0 /* unterminated",
		"s = 'unterminated",
		"\"unterminated > 1",
		"id > 0\x00) OR (true",
		"select * from t",
		"nosuchcolumn > 3",
		"id <",
		"",
	}
	// pqx's smuggled statements: each closes the query's parentheses for one depth
	for k := 1; k < 5; k++ {
		bad = append(bad, fmt.Sprintf("id > 0%s; COPY (SELECT 1) TO '%s'; %sSELECT 1 AS a WHERE (1",
			strings.Repeat(")", k), pwn, strings.Repeat("SELECT * FROM (", k-1)))
	}
	ctx := context.Background()
	for _, w := range bad {
		if w == "" {
			if err := ds.CheckWhere(w); err != nil {
				t.Errorf("blank filter refused: %v", err)
			}
			continue
		}
		err := ds.CheckWhere(w)
		if err == nil {
			t.Errorf("CheckWhere(%q) accepted it", w)
			continue
		}
		if HasControls(err.Error()) {
			t.Errorf("CheckWhere(%q): error with control characters: %q", w, err)
		}
		if _, err := ds.Count(ctx, View{Where: w}); err == nil {
			t.Errorf("Count(%q) ran it", w)
		}
		if _, err := ds.Fetch(ctx, View{Where: w}, 0, 5, []string{"id"}); err == nil {
			t.Errorf("Fetch(%q) ran it", w)
		}
	}
	if _, err := os.Stat(pwn); err == nil {
		t.Fatal("a smuggled COPY ran")
	}
	if err := ds.CheckWhere("i32 = 3) OR (s = 'b'"); !errors.Is(err, ErrFilter) || !strings.Contains(err.Error(), "unbalanced parentheses") {
		t.Errorf("unbalanced: %v", err)
	}
	if err := ds.CheckWhere("nosuchcolumn > 3"); err == nil || !strings.Contains(err.Error(), "nosuchcolumn") || strings.Contains(err.Error(), "LINE") {
		t.Errorf("unknown column: %v", err)
	}
	good := []string{
		"id > 3",
		"s = ')'",
		"s = '('' )'",
		"\"with space \"\"q\"\"\" > 3",
		"\"Name\" > 3 -- a comment with )",
		"id > 0 /* ( nested /* ) */ ( */",
		"s = $$)$$ OR s = $x$($x$",
		"s = E'\\')'",
		"(id > 3) AND (b OR i32 IS NULL)",
		"id IN (SELECT 1)",
	}
	for _, w := range good {
		if err := ds.CheckWhere(w); err != nil {
			t.Errorf("CheckWhere(%q): %v", w, err)
		}
		if _, err := ds.Count(ctx, View{Where: w}); err != nil {
			t.Errorf("Count(%q): %v", w, err)
		}
	}
	// a trailing comment can't swallow the closing parenthesis
	if n, err := ds.Count(ctx, View{Where: "id < 10 -- )"}); err != nil || n != 10 {
		t.Errorf("trailing comment: %d %v", n, err)
	}
}

// pqx's test_glob_characters_in_the_path_name_one_file
func TestGlobCharactersInFileName(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]int64{
		"a*.parquet": {1, 2}, "ab.parquet": {100, 200, 300}, "x[1].parquet": {5}, "x1.parquet": {6, 6},
		"q?.parquet": {7}, "qq.parquet": {8, 8}, "it's.parquet": {9},
	}
	for name, vals := range files {
		writeInts(t, filepath.Join(dir, name), vals...)
	}
	for _, name := range []string{"a*.parquet", "x[1].parquet", "q?.parquet", "ab.parquet", "it's.parquet"} {
		ds, err := Open(filepath.Join(dir, name), Options{})
		if err != nil {
			t.Fatal(err)
		}
		v := View{Where: "a > 0"}
		n, err := ds.Count(context.Background(), v)
		if err != nil || n != int64(len(files[name])) {
			t.Errorf("%s: Count %d %v", name, n, err)
		}
		got, err := ds.Fetch(context.Background(), v, 0, 10, []string{"a"})
		var want []string
		for _, x := range files[name] {
			want = append(want, strconv.FormatInt(x, 10))
		}
		if err != nil || !reflect.DeepEqual(got.Cols["a"], want) {
			t.Errorf("%s: %v %v", name, got.Cols["a"], err)
		}
		ds.Close()
	}
}

// slow is a filter that keeps DuckDB busy for many seconds.
const slow = "id >= 0 AND (SELECT sum(r) FROM range(100000000000) t(r)) > 0"

func TestCancelCount(t *testing.T) {
	_, ds := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	t0 := time.Now()
	_, err := ds.Count(ctx, View{Where: slow})
	el := time.Since(t0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if el > time.Second {
		t.Fatalf("took %v to stop", el)
	}
	t.Logf("cancelled after 100 ms, returned after %v", el)
	// and the dataset still works
	if n, err := ds.Count(context.Background(), View{Where: "id < 5"}); err != nil || n != 5 {
		t.Fatalf("after cancel: %d %v", n, err)
	}
}

func TestCancelFetch(t *testing.T) {
	_, ds := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	_, err := ds.Fetch(ctx, View{Where: slow}, 0, 10, []string{"id"})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(t0) > time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(t0))
	}
	// a cancelled context stops the plain view before it reads
	done, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := ds.Fetch(done, View{}, 0, 10, []string{"id"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("plain: err = %v", err)
	}
	if _, err := ds.Count(done, View{Where: "id > 0"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("count: err = %v", err)
	}
}

// Cancelling one query leaves another running; calls don't wait for each other.
func TestConcurrentCallsIndependent(t *testing.T) {
	_, ds := fixture(t)
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	errA, errB := make(chan error, 1), make(chan error, 1)
	go func() { _, err := ds.Count(ctxA, View{Where: slow}); errA <- err }()
	go func() { _, err := ds.Count(ctxB, View{Where: slow}); errB <- err }()
	time.Sleep(100 * time.Millisecond)
	// both are running: other calls still answer at once
	t0 := time.Now()
	if _, err := ds.Fetch(context.Background(), View{Where: "id > 5"}, 0, 10, []string{"id", "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.Fetch(context.Background(), View{}, 500, 10, []string{"id", "s"}); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(t0); el > time.Second {
		t.Fatalf("fetches took %v while counts ran", el)
	}
	cancelA()
	select {
	case err := <-errA:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("A: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("A didn't stop")
	}
	select {
	case err := <-errB:
		t.Fatalf("B stopped with A: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	cancelB()
	if err := <-errB; !errors.Is(err, context.Canceled) {
		t.Fatalf("B: %v", err)
	}
}

func TestFileRowNumberColumn(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "frn.parquet")
	writeInts(t, p, 5, 6, 7)
	// rename the column to file_row_number by rewriting with DuckDB
	ds, err := Open(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	q := filepath.Join(dir, "frn2.parquet")
	if _, err := ds.(*dataset).db.Exec("COPY (SELECT a AS file_row_number, a * 10 AS b FROM read_parquet(" + pathLiteral(p) + ")) TO " + quoteStr(q) + " (FORMAT parquet)"); err != nil {
		t.Fatal(err)
	}
	ds.Close()
	ds, err = Open(q, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	got, err := ds.Fetch(context.Background(), View{Where: "b > 55"}, 0, 10, []string{"file_row_number", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Cols["b"], []string{"60", "70"}) || !reflect.DeepEqual(got.FileRows, []int64{-1, -1}) {
		t.Fatalf("%+v", got)
	}
}
