package schema

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/golden"
)

// Where arrow-go's reading of the file differs from PyArrow's: it reads
// arrow.json columns as their storage type.
var arrowNameKnown = map[string]bool{"types.parquet/json": true}

// arrowName spells every fixture column's type as PyArrow does.
func TestArrowNameMatchesPyArrow(t *testing.T) {
	files, err := golden.Files()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, name := range files {
		if !strings.HasPrefix(name, "data_") || name == "data_common.json" {
			continue
		}
		g := golden.Load(t, name)
		ds, err := data.Open(golden.Fixture(g.Header.Fixture), data.Options{Threads: 1})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{}
		for _, r := range g.Section(t, "columns") {
			var c struct {
				Name  string
				Arrow string `json:"arrow_type"`
			}
			b, _ := json.Marshal(r)
			_ = json.Unmarshal(b, &c)
			want[c.Name] = c.Arrow
		}
		for _, c := range ds.Columns() {
			if got := typeName(c.Name, c.Arrow); got != want[c.Name] && !arrowNameKnown[g.Header.Fixture+"/"+c.Name] {
				t.Errorf("%s %q: got %q want %q", g.Header.Fixture, c.Name, got, want[c.Name])
			}
			n++
		}
		ds.Close()
	}
	if n < 100 {
		t.Fatalf("only %d columns", n)
	}
}
