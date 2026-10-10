package data

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ported from tests/test_data.py::test_export_roundtrip.
func TestExportRoundtrip(t *testing.T) {
	dm, ds := demoDataset(t)
	dir := t.TempDir()
	var want int64
	for _, b := range dm.band {
		if b == "i" {
			want++
		}
	}
	for _, desc := range []bool{false, true} {
		out := filepath.Join(dir, "sub.parquet")
		n, err := ds.Export(bg, View{Where: "band = 'i'", OrderBy: []Sort{{Column: "mag", Desc: desc}}}, out, ExportParquet, []string{"diaSourceId", "mag"})
		if err != nil {
			t.Fatal(err)
		}
		got, err := Open(out, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if n != want || got.NumRows() != want || len(got.Columns()) != 2 || got.Columns()[0].Name != "diaSourceId" || got.Columns()[1].Name != "mag" {
			t.Fatalf("exported %d rows (want %d): %d rows, columns %v", n, want, got.NumRows(), colNames(got))
		}
		w, err := got.Fetch(bg, View{}, 0, int(want), []string{"diaSourceId", "mag"})
		if err != nil {
			t.Fatal(err)
		}
		mags := w.Cols["mag"]
		nulls := false
		for i := 1; i < len(mags); i++ {
			if mags[i] == nil {
				nulls = true
				continue
			}
			if nulls {
				t.Fatal("a value after the NULLs (NULLS LAST)")
			}
			a, b := mags[i-1].(float64), mags[i].(float64)
			if desc && a < b || !desc && a > b {
				t.Fatalf("not sorted at %d: %v %v", i, a, b)
			}
		}
		if mags[len(mags)-1] != nil {
			t.Error("no NULLs at the end")
		}
		// NULLs (ties) in file order
		var last int64 = -1
		for i, m := range mags {
			if m == nil {
				id := w.Cols["diaSourceId"][i].(int64)
				if id < last {
					t.Fatal("ties not in file order")
				}
				last = id
			}
		}
		got.Close()
	}

	csv := filepath.Join(dir, "agg.csv")
	n, err := ds.Export(bg, View{SQL: "select band, count(*) n from t group by 1 order by 1 -- trailing comment"}, csv, ExportCSV, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(csv)
	if n != 6 || !strings.HasPrefix(string(b), "band,n\n") || strings.Count(string(b), "\n") != 7 {
		t.Errorf("csv: %d rows\n%s", n, b)
	}

	js := filepath.Join(dir, "rows.json")
	n, err = ds.Export(bg, View{Where: "diaSourceId < 5"}, js, ExportJSON, []string{"diaSourceId", "band"})
	if err != nil || n != 5 {
		t.Fatalf("json: %d rows, %v", n, err)
	}
	f, _ := os.Open(js)
	defer f.Close()
	sc := bufio.NewScanner(f)
	lines := 0
	for sc.Scan() {
		var row map[string]any
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil || row["band"] != dm.band[lines] {
			t.Errorf("json line %d: %s (%v)", lines, sc.Text(), err)
		}
		lines++
	}
	if lines != 5 {
		t.Errorf("%d json lines", lines)
	}

	// a name with quotes, brackets and glob characters
	odd := filepath.Join(dir, `we'ird [x]*?"q".parquet`)
	n, err = ds.Export(bg, View{SQL: "SELECT 1 AS one"}, odd, ExportParquet, nil)
	if err != nil || n != 1 {
		t.Fatalf("odd name: %d %v", n, err)
	}
	if _, err := os.Stat(odd); err != nil {
		t.Error(err)
	}
}

func TestExportRefusesTheSource(t *testing.T) {
	_, ds := demoDataset(t)
	before, _ := os.Stat(ds.path)
	dir := filepath.Dir(ds.path)
	link := filepath.Join(t.TempDir(), "link.parquet")
	if err := os.Symlink(ds.path, link); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(dir, "hard.parquet")
	if err := os.Link(ds.path, hard); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	rel, _ := filepath.Rel(wd, ds.path)
	for _, p := range []string{ds.path, filepath.Join(dir, "x", "..", "demo.parquet"), dir + "/./demo.parquet", link, hard, rel} {
		if _, err := ds.Export(bg, View{}, p, ExportCSV, nil); !errors.Is(err, ErrOverwriteSource) {
			t.Errorf("%s: %v", p, err)
		}
	}
	after, _ := os.Stat(ds.path)
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Error("the source changed")
	}
}

// A query can't break out of COPY's parentheses, nor be more than one.
func TestExportOneStatement(t *testing.T) {
	_, ds := demoDataset(t)
	dir := t.TempDir()
	evil := filepath.Join(dir, "evil.csv")
	for _, q := range []string{
		"select 1) TO '" + evil + "' (FORMAT csv) --",
		"select 1) TO '" + evil + "' (FORMAT csv); select (1",
		"select 1; select 2",
		"copy (select 1) to '" + evil + "'",
	} {
		if _, err := ds.Export(bg, View{SQL: q}, filepath.Join(dir, "out.csv"), ExportCSV, nil); err == nil {
			t.Errorf("%q: no error", q)
		}
	}
	if _, err := ds.Export(bg, View{Where: "1=1) TO '" + evil + "' (FORMAT csv) --"}, filepath.Join(dir, "out.csv"), ExportCSV, nil); err == nil {
		t.Error("filter: no error")
	}
	if _, err := os.Stat(evil); err == nil {
		t.Error("wrote", evil)
	}
	if _, err := ds.Export(bg, View{}, filepath.Join(dir, "x.csv"), ExportCSV, []string{"nope"}); err == nil {
		t.Error("missing column: no error")
	}
}
