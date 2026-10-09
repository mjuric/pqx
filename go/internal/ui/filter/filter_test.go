package filter

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/apache/arrow-go/v18/arrow"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

type look struct{}

func (look) Render(t styled.Text) string { return t.Plain }
func (look) Style(string) styled.Style   { return styled.Style{} }
func (look) DarkBG() bool                { return true }

// fake: columns x (int64) and s (string); a filter "bad" fails Validate
// with a DuckDB-like message; counts are the length of the WHERE text.
type fake struct {
	data.Unimplemented
	validated []data.View
	counts    int
	row0      map[string]data.Value
}

func (f *fake) NumRows() int64 { return 1000 }
func (f *fake) Columns() []data.Column {
	return []data.Column{{Name: "x", Arrow: arrow.PrimitiveTypes.Int64}, {Name: "s", Arrow: arrow.BinaryTypes.String}}
}
func (f *fake) Validate(ctx context.Context, v data.View) ([]data.Column, error) {
	f.validated = append(f.validated, v)
	if v.Where == "bad" {
		return nil, errors.New("Binder Error: Referenced column \"bad\" not found in FROM clause!\nCandidate bindings: \"t.x\"\nLINE 1: ...")
	}
	if v.IsSQL() {
		return f.Columns()[:1], nil
	}
	return f.Columns(), nil
}
func (f *fake) Count(ctx context.Context, v data.View) (int64, error) {
	f.counts++
	return int64(len(v.Where)), nil
}
func (f *fake) Fetch(ctx context.Context, v data.View, start int64, n int, cols []string) (data.Window, error) {
	w := data.Window{Len: 1, Cols: map[string][]data.Value{}}
	for _, c := range cols {
		w.Cols[c] = []data.Value{f.row0[c]}
	}
	return w, nil
}

type rig struct {
	t    *testing.T
	f    *Filter
	env  *kit.Env
	ds   *fake
	msgs []tea.Msg
}

func newRig(t *testing.T, where string) *rig {
	ds := &fake{row0: map[string]data.Value{"x": int64(42), "s": "  Paris'x  "}}
	env := &kit.Env{DS: ds, Look: look{}, Tasks: kit.NewTasks(), Opts: kit.Options{Where: where},
		State: &kit.State{Total: 1000, Columns: ds.Columns(), Hidden: map[string]bool{}}}
	r := &rig{t: t, f: New(env), env: env, ds: ds}
	r.send(tea.WindowSizeMsg{Width: 80, Height: 20})
	return r
}

var cmdSlice = reflect.TypeOf([]tea.Cmd(nil))

// run runs cmd and what it leads to, synchronously, as the root would:
// task results go back to the filter if current; messages for the root
// are recorded (SetViewMsg is also broadcast back).
func (r *rig) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	switch m := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range m {
			r.run(c)
		}
	case kit.DoneMsg:
		if r.env.Tasks.Done(m) {
			r.send(m)
		}
	case kit.FocusMsg:
		r.msgs = append(r.msgs, m)
		if m.Pane == Part {
			r.run(r.f.Focus())
		} else {
			r.f.Blur()
		}
	case kit.SetViewMsg:
		r.msgs = append(r.msgs, m)
		r.send(m)
	default:
		if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().ConvertibleTo(cmdSlice) {
			for _, c := range v.Convert(cmdSlice).Interface().([]tea.Cmd) {
				r.run(c)
			}
			return
		}
		r.msgs = append(r.msgs, msg)
	}
}

func (r *rig) send(msg tea.Msg) { r.run(r.f.Update(msg)) }

func (r *rig) typeText(s string) {
	for _, c := range s {
		r.send(tea.KeyPressMsg{Code: c, Text: string(c)})
	}
}

func (r *rig) key(code rune, mod tea.KeyMod) { r.send(tea.KeyPressMsg{Code: code, Mod: mod}) }

func (r *rig) count(t any) int {
	n := 0
	for _, m := range r.msgs {
		if reflect.TypeOf(m) == reflect.TypeOf(t) {
			n++
		}
	}
	return n
}

func (r *rig) last(t any) tea.Msg {
	for i := len(r.msgs) - 1; i >= 0; i-- {
		if reflect.TypeOf(r.msgs[i]) == reflect.TypeOf(t) {
			return r.msgs[i]
		}
	}
	return nil
}

func TestApplyFilter(t *testing.T) {
	r := newRig(t, "")
	r.run(r.f.Focus())
	r.typeText("x > 1")
	r.key(tea.KeyEnter, 0)
	st := r.env.State
	if st.View.Where != "x > 1" || r.count(kit.ViewChangedMsg{}) != 1 {
		t.Fatalf("view %+v, %d ViewChangedMsg", st.View, r.count(kit.ViewChangedMsg{}))
	}
	if st.Total != 5 || r.count(kit.TotalMsg{}) != 1 {
		t.Errorf("total %d after the count", st.Total)
	}
	if fm, _ := r.last(kit.FocusMsg{}).(kit.FocusMsg); fm.Pane != "grid" || r.f.TypingFocused() {
		t.Errorf("focus after applying: %+v", fm)
	}
	// the same filter again: no new view; a count known is reused
	r.run(r.f.Focus())
	r.key(tea.KeyEnter, 0)
	if r.count(kit.ViewChangedMsg{}) != 1 || r.ds.counts != 1 {
		t.Errorf("re-applying: %d views, %d counts", r.count(kit.ViewChangedMsg{}), r.ds.counts)
	}
	// a sort is kept by a new filter, dropped by a full query
	st.View.OrderBy = []data.Sort{{Column: "x", Desc: true}}
	if v := r.f.viewFor("x > 2"); len(v.OrderBy) != 1 {
		t.Errorf("sort dropped: %+v", v)
	}
	r.send(kit.SetViewMsg{View: data.View{Where: "x > 1"}, KeepFileRow: -1}) // the count cached
	if st.Total != 5 || r.ds.counts != 1 {
		t.Errorf("cached count: total %d, %d counts", st.Total, r.ds.counts)
	}
	r.send(kit.SetViewMsg{View: data.View{SQL: "select x from t"}, KeepFileRow: -1})
	if !st.View.IsSQL() || len(st.Columns) != 1 || r.f.Value() != "select x from t" {
		t.Errorf("SQL view: %+v, %d columns, box %q", st.View, len(st.Columns), r.f.Value())
	}
	if v := r.f.viewFor("x > 2"); len(v.OrderBy) != 0 || v.IsSQL() {
		t.Errorf("filter after SQL: %+v", v)
	}
	// on another tab focus goes back to that tab's body
	st.Tab = kit.TabStats
	r.run(r.f.Focus())
	r.f.in.SetValue("")
	r.key(tea.KeyEnter, 0)
	if fm, _ := r.last(kit.FocusMsg{}).(kit.FocusMsg); fm.Pane != "stats" {
		t.Errorf("focus from Stats: %+v", fm)
	}
	if !st.View.Plain() || st.Total != 1000 {
		t.Errorf("empty filter: %+v total %d", st.View, st.Total)
	}
}

func TestFailedFilter(t *testing.T) {
	r := newRig(t, "")
	r.run(r.f.Focus())
	r.typeText("bad")
	r.key(tea.KeyEnter, 0)
	if !r.env.State.View.Plain() || !r.f.TypingFocused() {
		t.Fatal("a failed filter was applied or left the box")
	}
	if r.f.Err() != `unknown column "bad"` {
		t.Errorf("inline error %q", r.f.Err())
	}
	st, _ := r.last(kit.StatusMsg{}).(kit.StatusMsg)
	if st.Severity != kit.Error || st.Text != "unknown column \"bad\"\ndid you mean \"x\"?" {
		t.Errorf("status %q", st.Text)
	}
	if !r.f.BorderError() || strings.Contains(r.f.View(80, 1), "✗") {
		t.Errorf("border error %v, view %q", r.f.BorderError(), r.f.View(80, 1))
	}
	r.typeText(" ")
	if r.f.Err() != "" || r.f.BorderError() {
		t.Error("the error stays while typing")
	}
}

func TestDescribe(t *testing.T) {
	for _, c := range []struct{ err, first, hint string }{
		{"Parser Error: syntax error at or near \"<\"", `syntax error at or near "<"`, "edit with /"},
		{"Binder Error: Referenced column \"y\" not found in FROM clause!\nCandidate bindings: \"t.x\"", `unknown column "y"`, `did you mean "x"?`},
		{"Candidate bindings: \"a\nb\"", "Candidate bindings: \"a", "edit with /"},
		{"Conversion Error: \x1b[31mbad\x07", "␛[31mbad␇", "edit with /"},
	} {
		first, hint := describe(errors.New(c.err))
		if first != c.first || hint != c.hint {
			t.Errorf("describe(%q) = %q, %q; want %q, %q", c.err, first, hint, c.first, c.hint)
		}
	}
}

func TestHistoryAndCtrlX(t *testing.T) {
	r := newRig(t, "")
	r.run(r.f.Focus())
	for _, s := range []string{"x > 1", "x > 2"} {
		r.f.in.SetValue(s)
		r.key(tea.KeyEnter, 0)
		r.run(r.f.Focus())
	}
	r.typeText("draft")
	r.key(tea.KeyUp, 0)
	r.key(tea.KeyUp, 0)
	if r.f.Value() != "x > 1" {
		t.Errorf("up up: %q", r.f.Value())
	}
	r.key(tea.KeyUp, 0) // at the oldest: stays
	r.key(tea.KeyDown, 0)
	if r.f.Value() != "x > 2" {
		t.Errorf("down: %q", r.f.Value())
	}
	r.key(tea.KeyDown, 0) // past the newest: an empty box, as in Python pqx
	if r.f.Value() != "" {
		t.Errorf("past the newest: %q", r.f.Value())
	}
	// ctrl+x with a filter applied: clears it, keeping the record
	r.env.State.FileRow = 77
	r.key('x', tea.ModCtrl)
	sv, _ := r.last(kit.SetViewMsg{}).(kit.SetViewMsg)
	if !r.env.State.View.Plain() || sv.KeepFileRow != 77 || r.f.Value() != "" {
		t.Errorf("ctrl+x: view %+v, keep %d, box %q", r.env.State.View, sv.KeepFileRow, r.f.Value())
	}
	// typed, never applied: just empties the box
	r.run(r.f.Focus())
	r.typeText("x <")
	n := len(r.msgs)
	r.key('x', tea.ModCtrl)
	if r.f.Value() != "" || len(r.msgs) != n {
		t.Errorf("ctrl+x on typed text: box %q, %d messages", r.f.Value(), len(r.msgs)-n)
	}
}

func TestOpenWithWhere(t *testing.T) {
	r := newRig(t, "x > 3")
	if r.env.State.View.Where != "x > 3" || r.f.Value() != "x > 3" {
		t.Errorf("-w: view %+v, box %q", r.env.State.View, r.f.Value())
	}
}

func TestHint(t *testing.T) {
	r := newRig(t, "")
	want := "SQL WHERE expression, e.g. x > 42 and s = 'Paris''x' — or a full query: select … from t"
	if r.f.hint != want {
		t.Errorf("hint %q", r.f.hint)
	}
	cols := []data.Column{{Name: "select", Arrow: arrow.PrimitiveTypes.Int64}, {Name: "big", Arrow: arrow.PrimitiveTypes.Int64},
		{Name: "f", Arrow: arrow.PrimitiveTypes.Float64}, {Name: "t", Arrow: &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int32, ValueType: arrow.BinaryTypes.String}}}
	got := Placeholder(cols, []data.Value{int64(1), int64(1234567890123), 1234567.0, "a\x1bb"})
	if got != "SQL WHERE expression, e.g. f > 1234567 and t = 'a␛b' — or a full query: select … from t" {
		t.Errorf("placeholder %q", got)
	}
	if got := Placeholder(nil, nil); !strings.Contains(got, example) {
		t.Errorf("no columns: %q", got)
	}
	for v, want := range map[float64]string{0.000123456: "0.000123", 1.5e-7: "1.5e-07", 2.5e20: "2.5e+20", 123.456: "123"} {
		if s, _ := shortNumber(v); s != want {
			t.Errorf("shortNumber(%g) = %q, want %q", v, s, want)
		}
	}
}

func TestIsSQL(t *testing.T) {
	for s, want := range map[string]bool{"select 1": true, "  WITH a as (select 1) select * from a": true,
		"from t": true, "x > 1": false, "selection > 1": false, "describe": true} {
		if IsSQL(s) != want {
			t.Errorf("IsSQL(%q) = %v", s, !want)
		}
	}
}

func TestControlsInTheBoxAreShownAsSymbols(t *testing.T) {
	r := newRig(t, "")
	r.send(kit.SetViewMsg{View: data.View{Where: "s = 'a\x1b]0;x\x07'"}, KeepFileRow: -1})
	if v := r.f.View(80, 1); strings.ContainsAny(v, "\x1b\x07") {
		t.Errorf("the box holds controls: %q", v)
	}
}

func TestCursorInTheSanitizedBox(t *testing.T) {
	r := newRig(t, "")
	r.send(kit.SetViewMsg{View: data.View{Where: "s = '\x1b\x1b\x1b'"}, KeepFileRow: -1})
	r.run(r.f.Focus()) // the cursor at the end
	c := r.f.Cursor()
	shown := r.f.View(80, 1)
	end := len([]rune(strings.TrimRight(shown, " ")))
	if c == nil || c.Position.X != end {
		t.Errorf("cursor at %+v, the text shown ends at %d: %q", c, end, shown)
	}
}

// History: Enter adds a filter unless it is the last one; a view "=" made
// is added once it applies (so ↑ brings it back first); the filter
// reverted to, or the one shown, isn't added.
func TestHistoryEntries(t *testing.T) {
	r := newRig(t, "")
	r.run(r.f.Focus())
	for _, s := range []string{"x > 1", "x > 1", "x > 2"} {
		r.f.in.SetValue(s)
		r.key(tea.KeyEnter, 0)
		r.run(r.f.Focus())
	}
	if h := r.f.History(); len(h) != 2 || h[0] != "x > 1" || h[1] != "x > 2" {
		t.Fatalf("history %q", h)
	}
	r.send(kit.SetViewMsg{View: data.View{Where: "(x > 2) and s = 'a'"}, KeepFileRow: 3}) // "="
	r.send(kit.SetViewMsg{View: data.View{Where: "(x > 2) and s = 'a'"}, KeepFileRow: 3}) // the same view again
	r.send(kit.SetViewMsg{View: data.View{Where: "x > 2"}, KeepFileRow: -1})              // a revert
	r.send(kit.SetViewMsg{View: data.View{Where: "bad"}, KeepFileRow: 3})                 // fails: not added
	if h := r.f.History(); len(h) != 3 || h[2] != "(x > 2) and s = 'a'" {
		t.Fatalf("history %q", h)
	}
	r.run(r.f.Focus())
	r.key(tea.KeyUp, 0)
	if r.f.Value() != "(x > 2) and s = 'a'" {
		t.Errorf("↑ after =: %q", r.f.Value())
	}
}

func TestOpenWithAQueryOrABadFilter(t *testing.T) {
	r := newRig(t, "select x from t")
	if !r.env.State.View.IsSQL() || len(r.env.State.Columns) != 1 || r.f.Value() != "select x from t" {
		t.Errorf("-w select: %+v", r.env.State.View)
	}
	r = newRig(t, "bad")
	if !r.env.State.View.Plain() || !r.f.BorderError() || r.f.Value() != "bad" || r.f.TypingFocused() {
		t.Errorf("-w bad: %+v border %v box %q", r.env.State.View, r.f.BorderError(), r.f.Value())
	}
	if len(r.f.History()) != 0 {
		t.Errorf("history %q", r.f.History())
	}
}

// Completion offers DuckDB's names: name_1 for the second of Name and name.
func TestCompletionByDuckDBsNames(t *testing.T) {
	ds, err := data.Open(filepath.Join("..", "..", "..", "testdata", "fixtures", "casedup.parquet"), data.Options{Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	f := New(&kit.Env{DS: ds, Look: look{}, Tasks: kit.NewTasks(), State: &kit.State{Columns: ds.Columns()}})
	if got := f.suggest("x > 1 and name_"); got != "x > 1 and name_1" {
		t.Errorf("suggest %q", got)
	}
}
