package grid

// The second review round's cases: "=" twice (queued, or in one batch),
// "=" onto a SQL view on its way, x from the grid during a lookup, the
// Esc mark, keys not kept in a view without file rows, the pane's keys,
// the revert keeping the typed filter, and a filter with a comment.

import (
	"strings"
	"testing"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/sqllit"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// lookingUp: = on band at file row 15,000 of demo, the lookup held.
func lookingUp(t *testing.T) (*harness, *hooked, map[string][]data.Value) {
	ds := hookFixture(t, "demo")
	tr := demoTruth(t, ds, "band", "detector", "mag", "ra")
	ds.holdFind = true
	h := newHarness(t, ds, 150, 42)
	h.send(kit.GotoMsg{Row: 15_000})
	h.settle()
	h.g.curCol = h.g.byName["band"]
	h.exec(h.g.moved())
	h.settle()
	h.send(kp("="))
	h.waitFor("the lookup", func() bool { return ds.nFinds() == 1 && h.g.v.loaded(0) && h.env.State.View.Where != "" })
	h.settle()
	return h, ds, tr
}

func (h *harness) toCol(name string) {
	h.g.curCol = h.g.byName[name]
	h.exec(h.g.moved())
	h.settle()
}

func release(ds *hooked) {
	ds.mu.Lock()
	ds.holdFind = false
	ds.mu.Unlock()
	ds.release(0)
}

func cond(t *testing.T, col string, v data.Value) string {
	c, ok := sqllit.Equals(col, v)
	if !ok {
		t.Fatalf("no condition for %s %v", col, v)
	}
	return c
}

// Two = queued for the record are replayed together: the second adds to
// the view the first asked for.
func TestTwoQueuedEqualsKeepBothConditions(t *testing.T) {
	h, ds, tr := lookingUp(t)
	h.toCol("detector")
	h.press("=")
	h.toCol("mag")
	h.press("=")
	release(ds)
	h.settle()
	want := "((band = '" + tr["band"][15_000].(string) + "') and " + cond(t, "detector", tr["detector"][15_000]) + ") and " + cond(t, "mag", tr["mag"][15_000])
	if h.env.State.View.Where != want || h.record() != 15_000 {
		t.Errorf("where %q, want %q; record %d", h.env.State.View.Where, want, h.record())
	}
}

// Two = in one batch of messages (before the first SetViewMsg arrives).
func TestTwoEqualsInOneBatch(t *testing.T) {
	ds := openFixture(t, "demo")
	tr := demoTruth(t, ds, "band", "detector")
	h := newHarness(t, ds, 150, 42)
	h.toCol("band")
	h.send(kp("="))
	h.g.curCol = h.g.byName["detector"]
	h.exec(h.g.moved())
	h.send(kp("="))
	h.settle()
	want := "(band = '" + tr["band"][0].(string) + "') and " + cond(t, "detector", tr["detector"][0])
	if h.env.State.View.Where != want {
		t.Errorf("where %q, want %q", h.env.State.View.Where, want)
	}
}

// = while a SQL query is on its way: the condition alone (a SQL view has no
// filter to add to).
func TestEqualsOntoASQLViewOnItsWay(t *testing.T) {
	ds := &slowCheck{hooked: hookFixture(t, "demo")}
	tr := demoTruth(t, ds, "band")
	h := newHarness(t, ds, 150, 42)
	h.toCol("band")
	ds.gate = make(chan struct{})
	h.send(kit.SetViewMsg{View: data.View{SQL: "select band from t"}, KeepFileRow: -1})
	h.settle()
	h.press("=")
	close(ds.gate)
	h.settle()
	if want := "band = '" + tr["band"][0].(string) + "'"; h.env.State.View.Where != want || h.env.State.View.IsSQL() {
		t.Errorf("view %+v, want where %q", h.env.State.View, want)
	}
}

// x on the grid during a lookup keeps the record on its way.
func TestXOnTheGridDuringALookup(t *testing.T) {
	h, ds, _ := lookingUp(t)
	h.press("x")
	release(ds)
	h.settle()
	if !h.env.State.View.Plain() || h.g.curRow != 15_000 {
		t.Errorf("view %+v row %d", h.env.State.View, h.g.curRow)
	}
}

// Esc while a sort is checked: the box shows the filter applied, so it
// isn't marked.
func TestEscWhileASortIsCheckedLeavesTheBoxAlone(t *testing.T) {
	ds := &slowCheck{hooked: hookFixture(t, "demo")}
	h := newHarness(t, ds, 150, 42)
	h.filterWith("band = 'r'")
	ds.gate = make(chan struct{})
	h.press("s")
	if !h.env.Tasks.Running("validate") {
		t.Fatal("not checking")
	}
	h.press("esc")
	if h.f.BorderError() || h.f.Value() != "band = 'r'" {
		t.Errorf("border %v box %q", h.f.BorderError(), h.f.Value())
	}
}

// Keys waiting for a record that a view without file rows (a SQL result)
// can't keep are dropped with a notice.
func TestKeysWaitingAreDroppedByAViewWithoutFileRows(t *testing.T) {
	h, ds, _ := lookingUp(t)
	h.press("y")
	h.send(kit.SetViewMsg{View: data.View{SQL: "select band from t"}, KeepFileRow: 15_000})
	release(ds)
	h.settle()
	if !h.noted("y not applied: the record isn't kept in this view") || len(h.copies) != 0 {
		t.Errorf("notes %+v copies %q", h.notes, h.copies)
	}
}

// The pane's keys run on its field's column (the grid's cursor goes there);
// queued from the pane, a digit key's notice still says where it shows.
func TestThePanesKeysOnTheirColumn(t *testing.T) {
	ds := openFixture(t, "demo")
	tr := demoTruth(t, ds, "detector")
	h := newHarness(t, ds, 150, 42)
	h.toCol("band")
	h.exec(h.g.FieldKey("detector", kp("y")))
	h.settle()
	if h.curColumn() != "detector" || len(h.copies) != 1 || h.copies[0] != cond(t, "x", tr["detector"][0])[4:] {
		t.Errorf("column %q copies %q", h.curColumn(), h.copies)
	}

	h2, ds2, _ := lookingUp(t)
	if !h2.g.QueueKey(kp("<"), "ra") {
		t.Fatal("not queued")
	}
	release(ds2)
	h2.settle()
	if !h2.noted("(grid)") {
		t.Errorf("notes %+v", h2.notes)
	}
}

// While a record is looked for, Record has only the columns shown.
func TestThePendingRecordLeavesHiddenColumnsOut(t *testing.T) {
	h, _, _ := lookingUp(t)
	h.toCol("ra")
	h.press("-")
	rec := h.g.Record()
	_, inValues := rec.Values["ra"]
	if !rec.Pending || inValues || strings.Contains(strings.Join(rec.Missing, ","), "ra,") {
		t.Errorf("record %+v", rec)
	}
	for _, m := range rec.Missing {
		if m == "ra" {
			t.Errorf("missing %q", rec.Missing)
		}
	}
}

// A typed filter that fails on its first read: the previous view comes
// back, and the box keeps the filter typed, marked.
func TestARevertKeepsTheTypedFilter(t *testing.T) {
	ds := newFake(1000, 3)
	h := newHarness(t, ds, 80, 20)
	h.filterWith("bad")
	if !h.env.State.View.Plain() || h.f.Value() != "bad" || !h.f.BorderError() {
		t.Errorf("view %+v box %q border %v", h.env.State.View, h.f.Value(), h.f.BorderError())
	}
	if len(h.f.History()) != 1 {
		t.Errorf("history %q", h.f.History())
	}
}

// = onto a filter with a -- comment: the box holds the filter as applied,
// on one line, and it applies again as it is (Enter, and from the history).
func TestAFilterWithACommentRoundTrips(t *testing.T) {
	ds := openFixture(t, "demo")
	h := newHarness(t, ds, 150, 42)
	h.filterWith("band = 'r' -- note")
	h.toCol("detector")
	h.press("=")
	w, n := h.env.State.View.Where, h.env.State.Total
	if h.f.Value() != w || strings.Contains(w, "--") || strings.ContainsAny(w, "\n\r") {
		t.Fatalf("box %q view %q", h.f.Value(), w)
	}
	h.press("/", "enter")
	if h.env.State.View.Where != w || h.env.State.Total != n || h.f.BorderError() {
		t.Errorf("again: %q total %d (was %d)", h.env.State.View.Where, h.env.State.Total, n)
	}
	h.press("/", "up", "enter")
	if h.env.State.View.Where != w || h.env.State.Total != n || h.f.BorderError() {
		t.Errorf("from the history: %q total %d (was %d)", h.env.State.View.Where, h.env.State.Total, n)
	}
}
