package fmtx

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"

	"github.com/mjuric/pqx/go/internal/golden"
)

// TestFixtureColumns: KindFor and ShortType of every fixture column, as
// arrow-go reads its type, against Python's (data_<fixture>.json).
// Differences that come from the reader, not from fmtx, are logged.
func TestFixtureColumns(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(golden.Dir(), "data_*.json"))
	for _, gf := range files {
		g := golden.Load(t, filepath.Base(gf))
		if g.Header.Fixture == "" {
			continue
		}
		pf, err := file.OpenParquetFile(golden.Fixture(g.Header.Fixture), false)
		if err != nil {
			t.Fatal(err)
		}
		sc, err := pqarrow.FromParquet(pf.MetaData().Schema, &pqarrow.ArrowReadProperties{}, pf.MetaData().KeyValueMetadata())
		pf.Close()
		if err != nil {
			t.Fatal(err)
		}
		for i, r := range g.Section(t, "columns") {
			name := r.String(t, "name")
			if i >= sc.NumFields() || sc.Field(i).Name != name {
				t.Errorf("%s: column %d is not %q", g.Header.Fixture, i, name)
				continue
			}
			typ := sc.Field(i).Type
			pyType := r.String(t, "arrow_type")
			if got := pyTypeString(typ); got != arrowGoType(pyType) {
				// the reader's type differs (seconds as ms, names of list items, …): check fmtx on Python's type
				t.Logf("%s %q: arrow-go reads %s, PyArrow %s", g.Header.Fixture, name, got, pyType)
				typ = parseType(t, pyType)
			}
			if got, want := string(KindFor(name, typ, r.String(t, "unit"))), r.String(t, "kind"); got != want {
				t.Errorf("%s %q: kind %q, want %q", g.Header.Fixture, name, got, want)
			}
			if got, want := ShortType(typ), arrowGoType(r.String(t, "short_type")); got != want && !strings.Contains(want, "('") {
				t.Errorf("%s %q: short type %q, want %q", g.Header.Fixture, name, got, want)
			}
		}
	}
}
