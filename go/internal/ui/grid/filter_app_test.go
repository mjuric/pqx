package grid

// tests/test_app.py's filter, SQL and error parts and tests/test_security.py's
// UI parts (completion quoting, "=" on hostile values, a smuggled statement),
// on the fixtures.

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

func TestFilterSQLAndErrors(t *testing.T) {
	ds := openFixture(t, "demo")
	tr := demoTruth(t, ds, "mag", "band")
	exp := int64(0)
	for i := range tr["mag"] {
		if tr["mag"][i].(float32) < 19 && tr["band"][i] == "g" {
			exp++
		}
	}
	h := newHarness(t, ds, 150, 42)
	st := h.env.State
	h.press("/")
	if !h.f.TypingFocused() {
		t.Fatal("/ didn't focus the filter")
	}
	h.typeText("mag < 19 and band = 'g'") // q, e, m … are text
	h.press("enter")
	if st.Total != exp || !h.g.focused || h.f.TypingFocused() || h.quits != 0 {
		t.Fatalf("total %d (want %d), grid focused %v", st.Total, exp, h.g.focused)
	}

	// the sort cycle on the cursor's column
	h.g.curCol = h.g.byName["mag"]
	h.exec(h.g.moved())
	h.press("s")
	if len(st.View.OrderBy) != 1 || st.View.OrderBy[0] != (data.Sort{Column: "mag"}) || st.View.Where != "mag < 19 and band = 'g'" {
		t.Errorf("sorted: %+v", st.View)
	}
	h.press("s")
	if st.View.OrderBy[0] != (data.Sort{Column: "mag", Desc: true}) {
		t.Errorf("sorted down: %+v", st.View)
	}
	h.press("s")
	if len(st.View.OrderBy) != 0 {
		t.Errorf("unsorted: %+v", st.View)
	}

	// a broken filter: shown, the previous view kept, the box keeps focus
	h.filterWith("mag <")
	if !h.f.BorderError() || h.status.Severity != kit.Error || st.Total != exp || st.View.Where != "mag < 19 and band = 'g'" {
		t.Errorf("broken filter: border %v status %+v total %d view %+v", h.f.BorderError(), h.status, st.Total, st.View)
	}
	if !h.f.TypingFocused() {
		t.Error("the box lost focus after an error")
	}

	// a full query: shown as a table of its own columns
	for len(h.f.Value()) > 0 {
		h.send(kp("backspace"))
	}
	h.typeText("select band, count(*) as n from t group by band order by n desc")
	if !strings.Contains(h.screen(), "sql › select band") {
		t.Errorf("no sql prompt:\n%s", h.screen())
	}
	h.press("enter")
	if st.Total != 6 || len(h.g.cols) != 2 || h.g.cols[0].Name != "band" || h.g.cols[1].Name != "n" {
		t.Fatalf("SQL: total %d, columns %d", st.Total, len(h.g.cols))
	}
	if h.f.BorderError() {
		t.Error("the error border stays")
	}
	// clearing: the whole file again
	h.press("x")
	if !st.View.Plain() || st.Total != 20_000 || len(h.g.cols) != len(ds.Columns()) {
		t.Errorf("cleared: %+v total %d columns %d", st.View, st.Total, len(h.g.cols))
	}
}

func TestValueFilterAndKeepPosition(t *testing.T) {
	ds := openFixture(t, "demo")
	band := demoTruth(t, ds, "band")["band"]
	h := newHarness(t, ds, 150, 42)
	h.g.curRow, h.g.curCol = 3, h.g.byName["band"]
	h.exec(h.g.moved())
	h.settle()
	h.press("=")
	b := band[3].(string)
	if h.env.State.View.Where != "band = '"+b+"'" {
		t.Fatalf("where %q", h.env.State.View.Where)
	}
	for r := int64(0); r < 30; r++ {
		if v, _ := h.g.v.cell("band", r); v != b {
			t.Fatalf("row %d band %v", r, v)
		}
	}
	h.g.curRow = 10
	h.exec(h.g.moved())
	h.settle()
	fr := h.record()
	h.press("x") // clearing keeps you on the same file row
	if h.g.curRow != fr {
		t.Errorf("row %d, want %d", h.g.curRow, fr)
	}
}

func TestErrorHint(t *testing.T) {
	ds := openFixture(t, "demo")
	h := newHarness(t, ds, 150, 42)
	h.filterWith("magg < 21")
	reason, hint, _ := strings.Cut(h.status.Text, "\n")
	if reason != `unknown column "magg"` || hint != `did you mean "mag"?` {
		t.Errorf("status %q", h.status.Text)
	}
	if !strings.HasPrefix(strings.TrimSpace(strings.Split(h.screen(), "\n")[41]), "enter apply") {
		t.Errorf("the filter's keys aren't shown:\n%s", h.screen())
	}
}

func TestCtrlKeysTabsAndClear(t *testing.T) {
	ds := openFixture(t, "demo")
	h := newHarness(t, ds, 150, 42)
	h.filterWith("band = 'g'")
	if h.env.State.View.Plain() {
		t.Fatal("not filtered")
	}
	h.press("/")
	h.send(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl}) // inside the input: a word jump, not a tab switch
	h.settle()
	if h.env.State.Tab != kit.TabData || !h.f.TypingFocused() {
		t.Errorf("ctrl+left in the box: tab %v, focused %v", h.env.State.Tab, h.f.TypingFocused())
	}
	h.press("ctrl+x")
	if !h.env.State.View.Plain() || h.f.Value() != "" || h.env.State.Total != 20_000 {
		t.Errorf("ctrl+x: %+v box %q", h.env.State.View, h.f.Value())
	}
	// typed but never applied: ctrl+x just empties the box
	h.press("/")
	h.typeText("mag <")
	h.press("ctrl+x")
	if h.f.Value() != "" || !h.env.State.View.Plain() {
		t.Errorf("box %q", h.f.Value())
	}
	// x clears from another tab's body too
	h.press("esc")
	h.filterWith("band = 'g'")
	h.press("2")
	h.press("x")
	if !h.env.State.View.Plain() || h.env.State.Tab != kit.TabSchema {
		t.Errorf("x on Schema: %+v tab %v", h.env.State.View, h.env.State.Tab)
	}
}

func TestAFilterWithUnbalancedParenthesesIsRefusedInline(t *testing.T) {
	ds := openFixture(t, "demo")
	h := newHarness(t, ds, 150, 42)
	h.filterWith("detector = 3) OR (band = 'r'")
	if !h.env.State.View.Plain() || h.env.State.Total != 20_000 || !h.f.BorderError() {
		t.Errorf("view %+v total %d border %v", h.env.State.View, h.env.State.Total, h.f.BorderError())
	}
	if !strings.Contains(h.status.Text, "unbalanced parentheses") {
		t.Errorf("status %q", h.status.Text)
	}
}

// Focus leaves the box at once on Enter: keys pressed while the query is
// checked are commands (d opens the pane), not text in the box.
func TestKeysAfterEnterAreNotTyped(t *testing.T) {
	gate := make(chan struct{})
	ds := &gatedValidate{fakeDS: newFake(1000, 4), gate: gate}
	h := newHarness(t, ds, 120, 30)
	h.press("/")
	h.typeText("id % 2 = 0")
	h.press("enter") // (the check waits)
	if !h.env.Tasks.Running("validate") || h.f.TypingFocused() {
		t.Fatalf("validating %v, box focused %v", h.env.Tasks.Running("validate"), h.f.TypingFocused())
	}
	h.press("d")
	close(gate)
	h.settle()
	if h.f.Value() != "id % 2 = 0" || h.env.State.View.Where != "id % 2 = 0" || !h.env.State.DetailOpen {
		t.Errorf("box %q, view %+v, pane open %v", h.f.Value(), h.env.State.View, h.env.State.DetailOpen)
	}
}

// gatedValidate is the fake whose Validate waits for gate.
type gatedValidate struct {
	*fakeDS
	gate chan struct{}
}

func (g *gatedValidate) Validate(ctx context.Context, v data.View) ([]data.Column, error) {
	if err := g.hold(ctx, g.gate); err != nil {
		return nil, err
	}
	return g.fakeDS.Validate(ctx, v)
}

// A filter that would close its parentheses to add a statement is refused
// before DuckDB runs it.
func TestSmuggledStatementIsRefused(t *testing.T) {
	ds := openFixture(t, "hostile")
	pwn := t.TempDir() + "/pwn_typed.txt"
	h := newHarness(t, ds, 200, 50)
	h.filterWith("a > 0)); COPY (SELECT 1) TO '" + pwn + "'; SELECT * FROM (SELECT 1 AS x WHERE (1")
	if !strings.Contains(h.status.Text, "unbalanced parentheses") && !strings.Contains(h.status.Text, "single SELECT") {
		t.Errorf("status %q", h.status.Text)
	}
	if _, err := os.Stat(pwn); err == nil {
		t.Error("the smuggled COPY ran")
	}
}

const fixturePwn = "/tmp/pqx-fixture-pwned.txt" // where hostile.parquet's SQL name would write

func TestColumnCompletionQuotesNames(t *testing.T) {
	_, before := os.Stat(fixturePwn)
	ds := openFixture(t, "hostile")
	h := newHarness(t, ds, 200, 50)
	h.press("/")
	h.typeText("ran")
	if s := h.f.Suggestion(); !strings.HasPrefix(s, `"random() > -1))`) {
		t.Fatalf("suggestion %q", s)
	}
	h.press("right")
	completed := h.f.Value()
	h.press("enter")
	if !strings.HasPrefix(completed, `"random() > -1))`) || h.quits != 0 {
		t.Errorf("completed %q", completed)
	}
	if _, err := os.Stat(fixturePwn); err == nil && before != nil {
		t.Error("the name's COPY ran")
	}
	// a name with control characters is never put in the box
	h.press("/")
	for len(h.f.Value()) > 0 {
		h.send(kp("backspace"))
	}
	h.typeText("es")
	if h.f.Suggestion() != "" {
		t.Errorf("suggested %q", h.f.Suggestion())
	}
}

// "=" on every hostile string of every hostile column matches exactly its
// row, with no control character in the box.
func TestEqualsFilterMatchesHostileValuesExactly(t *testing.T) {
	_, before := os.Stat(fixturePwn)
	ds := openFixture(t, "hostile")
	h := newHarness(t, ds, 200, 50)
	names := []string{"s", "select", "[bold]mk[/] [@click=app.quit]x[/]", "esc\x1b]0;PWNED-TITLE\x07name", "c1\u009b2Jname",
		"bidi\u202ename\u200b", "dir\\"}
	for _, name := range names {
		for row := int64(0); row < ds.NumRows(); row++ {
			h.press("ctrl+x")
			h.g.Focus()
			h.g.curRow, h.g.curCol = row, h.g.byName[name]
			h.exec(h.g.moved())
			h.settle()
			h.press("=")
			box := h.f.Value()
			if hasControlChars(box) {
				t.Errorf("%q row %d: controls in the box %q", name, row, box)
			}
			if h.f.BorderError() || h.env.State.Total != 1 {
				t.Errorf("%q row %d: %q → total %d, status %q", name, row, box, h.env.State.Total, h.status.Text)
				continue
			}
			if v, _ := h.g.v.cell("a", 0); v != row {
				t.Errorf("%q row %d: the row matched is %v", name, row, v)
			}
		}
	}
	if _, err := os.Stat(fixturePwn); err == nil && before != nil {
		t.Error("a value's COPY ran")
	}
}

func hasControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return true
		}
	}
	return false
}

// "=" on each type of types.parquet: the condition matches the row it came
// from (and, for most, only rows with that value).
func TestEqualsOnEveryType(t *testing.T) {
	ds := openFixture(t, "types")
	h := newHarness(t, ds, 200, 50)
	refused := map[string]bool{}
	for _, c := range ds.Columns() {
		rows := []int64{2, 9} // (NaN and ±inf among them)
		if c.Name == "i64" || c.Name == "str" {
			rows = append(rows, 10) // NULL
		}
		for _, row := range rows {
			h.press("ctrl+x")
			h.g.Focus()
			h.g.curRow, h.g.curCol = row, h.g.byName[c.Name]
			h.exec(h.g.moved())
			h.settle()
			n := len(h.notes)
			h.press("=")
			if len(h.notes) > n && strings.Contains(h.notes[len(h.notes)-1].Text, "Can't filter on this value type") {
				refused[c.Name] = true
				continue
			}
			where := h.env.State.View.Where
			if h.f.BorderError() || where == "" {
				t.Errorf("%s row %d: %q failed: %q", c.Name, row, where, h.status.Text)
				continue
			}
			pos, found, err := ds.FindRow(context.Background(), h.env.State.View, row)
			if err != nil || !found || h.record() != row || h.g.curRow != pos {
				t.Errorf("%s row %d: %q doesn't keep the row (found %v, %v; record %d)", c.Name, row, where, found, err, h.record())
			}
		}
	}
	var names []string
	for n := range refused {
		names = append(names, n)
	}
	t.Logf("refused: %s", strings.Join(names, " "))
	for _, n := range []string{"i64", "u64", "f32", "f64", "bool", "str", "date", "ts_us", "ts_ms_utc", "dec9_2", "dec38_3"} {
		if refused[n] {
			t.Errorf("%s refused", n)
		}
	}
}

// Columns DuckDB renames (case duplicates): "=" refers to them by DuckDB's
// name.
func TestEqualsOnOddNames(t *testing.T) {
	for _, c := range []struct{ file, col, want string }{
		{"casedup", "name", "name_1 = 'b'"},
		{"casedup", "Name", "\"Name\" = 'B'"},
	} {
		ds := openFixture(t, c.file)
		h := newHarness(t, ds, 150, 30)
		h.g.curRow, h.g.curCol = 1, h.g.byName[c.col]
		h.exec(h.g.moved())
		h.settle()
		h.press("=")
		if h.env.State.View.Where != c.want {
			t.Errorf("%s %q: where %q, want %q (status %q)", c.file, fmtx.Sanitize(c.col, false), h.env.State.View.Where, c.want, h.status.Text)
			continue
		}
		if h.f.BorderError() || h.env.State.Total != 1 || h.record() != 1 {
			t.Errorf("%s %q: total %d record %d status %q", c.file, fmtx.Sanitize(c.col, false), h.env.State.Total, h.record(), h.status.Text)
		}
	}
}

// "=" ANDs onto the filter, parenthesizing one with "or"; every filter
// goes into the history once.
func TestEqualsAddsToTheFilter(t *testing.T) {
	ds := newFake(1000, 4)
	h := newHarness(t, ds, 150, 30)
	h.filterWith("id < 500 or id > 900")
	h.g.curCol = h.g.byName["c002"]
	h.g.curRow = 3
	h.exec(h.g.moved())
	h.settle()
	fr := h.record()
	h.press("=")
	want := "(id < 500 or id > 900) and c002 = " + strconv.FormatInt(truth("c002", fr).(int64), 10)
	if h.env.State.View.Where != want || h.f.Value() != want {
		t.Errorf("where %q box %q", h.env.State.View.Where, h.f.Value())
	}
	if hist := h.f.History(); len(hist) != 2 || hist[1] != want {
		t.Errorf("history %q", hist)
	}
}
