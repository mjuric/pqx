package filter

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/sqllit"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// tests/test_security.py::test_column_completion_quotes_names (the
// suggester part).
func TestSuggestQuotesNames(t *testing.T) {
	f := &Filter{}
	for _, c := range []string{"random() > 1); drop", "select", "plain_name", "esc\x1bx"} {
		if !sqllit.HasControls(c) {
			f.words = append(f.words, wordSQL{c, sqllit.Ident(c)})
		}
	}
	f.words = append(f.words, wordSQL{"select", "select"})
	for in, want := range map[string]string{
		"ran":           `"random() > 1); drop"`,
		"x > 1 and pla": "x > 1 and plain_name",
		"sel":           `"select"`,
		"es":            "", // never puts a control character in the box
		"plain_name":    "", // (nothing longer)
		"x > ":          "",
		"PLA":           "plain_name",
	} {
		if got := f.suggest(in); got != want {
			t.Errorf("suggest(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSuggestFromTheFile(t *testing.T) {
	r := newRig(t, "")
	r.run(r.f.Focus())
	r.typeText("x > 1 an")
	if r.f.Suggestion() != "x > 1 and" || !strings.Contains(r.f.View(80, 1), "x > 1 and") {
		t.Fatalf("suggestion %q, view %q", r.f.Suggestion(), r.f.View(80, 1))
	}
	r.key(tea.KeyRight, 0)
	if r.f.Value() != "x > 1 and" || r.f.Suggestion() != "" {
		t.Errorf("accepted: %q, then %q", r.f.Value(), r.f.Suggestion())
	}
	// → elsewhere than at the end moves the cursor
	r.typeText(" s")
	r.key(tea.KeyLeft, 0)
	r.key(tea.KeyRight, 0)
	if r.f.Value() != "x > 1 and s" {
		t.Errorf("→ inside the text: %q", r.f.Value())
	}
	// the suggestion isn't shown without focus
	r.f.Blur()
	if strings.Contains(r.f.View(80, 1), "select") {
		t.Errorf("view %q", r.f.View(80, 1))
	}
}

// The hint's example from the fixtures' first rows, as Python pqx shows it.
func TestHintFromTheFixtures(t *testing.T) {
	for file, ex := range map[string]string{
		"demo":    "ssObjectId > 1000133 and band = 'r'",
		"types":   "i8 > -128 and lstr = 'é0漢'",
		"odd":     "file_row_number > 0",
		"hostile": `a > 0 and s = 'it''s "quoted" \ back'`,
	} {
		ds, err := data.Open(filepath.Join("..", "..", "..", "testdata", "fixtures", file+".parquet"), data.Options{Threads: 1})
		if err != nil {
			t.Fatal(err)
		}
		env := &kit.Env{DS: ds, Look: look{}, Tasks: kit.NewTasks(), State: &kit.State{Columns: ds.Columns()}}
		f := New(env)
		done, _ := f.startHint()().(kit.DoneMsg)
		h, _ := done.Msg.(hinted)
		if want := "SQL WHERE expression, e.g. " + ex + " — or a full query: select … from t"; h.hint != want {
			t.Errorf("%s: %q, want %q", file, h.hint, want)
		}
		ds.Close()
	}
}
