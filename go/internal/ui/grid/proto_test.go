package grid

// The prototype's model tests (internal/ui/model_test.go before WP7),
// re-expressed against the grid and filter parts under the root model.

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

func TestFirstScreen(t *testing.T) {
	ds := newFake(1000, 4)
	h := newHarness(t, ds, 80, 20)
	calls := ds.fetches()
	if len(calls) != 1 {
		t.Fatalf("want one fetch, got %d: %+v", len(calls), calls)
	}
	n := h.g.bodyH()
	c := calls[0]
	if c.start != 0 || c.n != 2*n || !c.view.Plain() {
		t.Errorf("first fetch = %+v, want rows [0, %d) of the plain view", c, 2*n)
	}
	if strings.Join(c.cols, ",") != "id,name,c002,c003" {
		t.Errorf("columns fetched = %v", c.cols)
	}
	s := h.screen()
	for _, want := range []string{"id", "name", "i64", "str", "r0", "3003"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	lines := strings.Split(s, "\n")
	if len(lines) != 20 {
		t.Errorf("screen has %d lines, want 20", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != 80 {
			t.Errorf("line %d is %d cells wide: %q", i, w, l)
		}
	}
	gl := h.grid()
	if len(gl) != h.g.h {
		t.Errorf("grid has %d lines, want %d", len(gl), h.g.h)
	}
	for i, l := range gl {
		if w := ansi.StringWidth(l); w != h.g.w {
			t.Errorf("grid line %d is %d cells wide, want %d: %q", i, w, h.g.w, l)
		}
	}
}

func TestCursorKeys(t *testing.T) {
	ds := newFake(1000, 6)
	h := newHarness(t, ds, 80, 20)
	g := h.g
	n := int64(g.bodyH())
	h.press("down", "down", "right")
	if g.curRow != 2 || g.curCol != 1 {
		t.Fatalf("cursor at (%d,%d), want (2,1)", g.curRow, g.curCol)
	}
	if st := h.env.State; st.Row != 2 || st.FileRow != 2 || st.Current != "name" {
		t.Errorf("state: row %d file row %d current %q", st.Row, st.FileRow, st.Current)
	}
	h.press("up", "up", "up", "left", "left")
	if g.curRow != 0 || g.curCol != 0 {
		t.Fatalf("cursor not clamped at the top-left: (%d,%d)", g.curRow, g.curCol)
	}
	h.press("pgdown")
	if g.curRow != n || g.top != n {
		t.Errorf("after PgDn: row %d top %d, want %d %d", g.curRow, g.top, n, n)
	}
	h.press("pgup")
	if g.curRow != 0 || g.top != 0 {
		t.Errorf("after PgUp: row %d top %d", g.curRow, g.top)
	}
	h.press("end")
	if g.curCol != 5 {
		t.Errorf("End: column %d, want 5", g.curCol)
	}
	h.press("home")
	if g.curCol != 0 {
		t.Errorf("Home: column %d, want 0", g.curCol)
	}
	h.press("ctrl+end")
	if g.curRow != 999 || g.top != 1000-n {
		t.Errorf("Ctrl+End: row %d top %d", g.curRow, g.top)
	}
	if gl := h.grid(); !strings.Contains(gl[len(gl)-1], "999") {
		t.Errorf("last row not shown:\n%s", strings.Join(gl, "\n"))
	}
	h.press("down")
	if g.curRow != 999 {
		t.Errorf("cursor moved past the last row: %d", g.curRow)
	}
	h.press("ctrl+home")
	if g.curRow != 0 || g.top != 0 {
		t.Errorf("Ctrl+Home: row %d top %d", g.curRow, g.top)
	}
	h.press("q")
	if h.quits != 1 {
		t.Errorf("q did not quit")
	}
}

func TestPgDnPastWindowFetches(t *testing.T) {
	ds := newFake(100000, 3)
	h := newHarness(t, ds, 80, 20)
	n := h.g.bodyH()
	before := len(ds.fetches())
	h.press("pgdown") // rows [n, 2n) are read already (the first read took 2n)
	// the new screen was read by the first fetch, but the margin below it
	// was not: PgDn past the read rows starts a fetch
	if calls := ds.fetches(); len(calls) != before+1 || calls[len(calls)-1].start+int64(calls[len(calls)-1].n) <= int64(2*n) {
		t.Errorf("PgDn past the read rows: fetches %+v", calls[before:])
	}
	h.press("pgdown", "pgdown")
	calls := ds.fetches()
	last := calls[len(calls)-1]
	top := h.g.top
	if last.start > top || last.start+int64(last.n) < top+int64(n) {
		t.Errorf("last fetch %+v doesn't cover the screen at row %d", last, top)
	}
	gl := h.grid()
	if !strings.Contains(strings.Join(gl, "\n"), "r"+itoa(top)) {
		t.Errorf("row %d not shown after PgDn:\n%s", top, strings.Join(gl, "\n"))
	}
	if strings.Contains(gl[headerRows], "…") {
		t.Errorf("placeholders remain after the fetch: %q", gl[headerRows])
	}
}

func itoa(n int64) string { return strings.ReplaceAll(commas(n), ",", "") }

func TestSupersededFetchIsCancelled(t *testing.T) {
	ds := newFake(1_000_000, 3)
	ds.gate = make(chan struct{})
	h := newHarness(t, ds, 80, 20)
	if len(ds.fetches()) != 1 || !h.env.Tasks.Running("page") {
		t.Fatalf("want one fetch running, got %d", len(ds.fetches()))
	}
	// rows not read yet show placeholders
	if !strings.Contains(h.grid()[headerRows], "·") {
		t.Errorf("no placeholder while loading: %q", h.grid()[headerRows])
	}
	h.press("ctrl+end")
	h.waitFor("the first fetch to be cancelled", func() bool { return ds.cancels() == 1 })
	if len(ds.fetches()) != 2 {
		t.Fatalf("want a second fetch, got %d", len(ds.fetches()))
	}
	if c := ds.fetches()[1]; c.start+int64(c.n) != 1_000_000 {
		t.Errorf("second fetch %+v doesn't reach the end", c)
	}
	close(ds.gate)
	h.waitFor("the second fetch", func() bool { return !h.env.Tasks.Running("page") })
	h.settle()
	if !strings.Contains(strings.Join(h.grid(), "\n"), "r999999") {
		t.Errorf("last row not shown:\n%s", strings.Join(h.grid(), "\n"))
	}
	if h.g.failed != nil || len(h.notes) > 0 {
		t.Errorf("the cancelled fetch was taken for a failure: %+v", h.notes)
	}
}

func TestOldViewResultsAreDropped(t *testing.T) {
	ds := newFake(1000, 3)
	h := newHarness(t, ds, 80, 20)
	oldGen := h.g.v.gen
	ds.countGate = make(chan struct{})
	h.filterWith("id % 10 = 0")
	if h.g.v.gen == oldGen {
		t.Fatal("the view didn't change")
	}
	// a late result for the plain view must not land in the filtered one
	w, _ := ds.Fetch(t.Context(), data.View{}, 500, 5, []string{"id", "name"})
	h.g.Update(kit.DoneMsg{Tag: "page", Msg: pageResult{req: fetchReq{gen: oldGen, start: 500, n: 5, cols: []string{"id", "name"}}, win: w}})
	if _, ok := h.g.v.cell("name", 500); ok {
		t.Error("a result for the old view was stored")
	}
	if got, _ := h.g.v.cell("name", 1); got != "r10" {
		t.Errorf("row 1 of the filtered view = %v, want r10", got)
	}
	close(ds.countGate)
	h.settle()
}

func TestBadFilterKeepsView(t *testing.T) {
	ds := newFake(1000, 3)
	h := newHarness(t, ds, 80, 20)
	gen := h.g.v.gen
	h.press("/")
	if !h.f.TypingFocused() {
		t.Fatal("/ didn't focus the filter")
	}
	h.typeText("(id > 3")
	h.press("enter")
	if h.g.v.gen != gen || !h.env.State.View.Plain() {
		t.Error("a rejected filter changed the view")
	}
	if !h.f.TypingFocused() {
		t.Error("the filter bar lost focus after an error")
	}
	if s := h.screen(); !strings.Contains(s, "✗ unbalanced parentheses") {
		t.Errorf("no inline error:\n%s", s)
	}
	if h.status.Severity != kit.Error || !strings.Contains(h.status.Text, "unbalanced parentheses") {
		t.Errorf("status: %+v", h.status)
	}
	h.typeText(")")
	if h.f.Err() != "" {
		t.Error("the error stays while typing")
	}
	h.press("esc")
	if h.f.TypingFocused() || !h.g.focused || !h.env.State.View.Plain() {
		t.Error("esc didn't leave the filter bar")
	}
}

func TestGoodFilterSwitchesAndCounts(t *testing.T) {
	ds := newFake(1000, 3)
	ds.countGate = make(chan struct{})
	h := newHarness(t, ds, 80, 20)
	h.press("down", "down", "down")
	h.filterWith("id % 10 = 0")
	g, st := h.g, h.env.State
	if st.View.Where != "id % 10 = 0" || !g.focused || h.f.TypingFocused() {
		t.Fatalf("filter not applied: %q", st.View.Where)
	}
	if g.curRow != 0 {
		t.Errorf("cursor not at the top: %d", g.curRow)
	}
	if !h.env.Tasks.Running("count") || st.Total != -1 {
		t.Fatal("no count running")
	}
	s := strings.Join(h.grid(), "\n")
	for _, want := range []string{"r10", "r20"} {
		if !strings.Contains(s, want) {
			t.Errorf("grid lacks %q:\n%s", want, s)
		}
	}
	// the grid works before the count arrives, as far as rows are read
	h.press("pgdown")
	if g.curRow == 0 {
		t.Error("PgDn didn't move before the count arrived")
	}
	close(ds.countGate)
	h.waitFor("the count", func() bool { return !h.env.Tasks.Running("count") })
	h.settle()
	if st.Total != 100 || g.v.total != 100 {
		t.Errorf("total = %d (grid %d), want 100", st.Total, g.v.total)
	}
	h.press("ctrl+end")
	if g.curRow != 99 || !strings.Contains(strings.Join(h.grid(), "\n"), "r990") {
		t.Errorf("Ctrl+End in the filtered view: row %d", g.curRow)
	}
	// row labels are file row numbers
	if gl := h.grid(); !strings.HasPrefix(strings.TrimSpace(gl[len(gl)-1]), "990 ") {
		t.Errorf("file row number not shown: %q", gl[len(gl)-1])
	}
	if st.FileRow != 990 || st.Row != 99 {
		t.Errorf("state row %d file row %d", st.Row, st.FileRow)
	}
	// ctrl+x in the filter bar clears the filter
	h.press("/")
	h.press("ctrl+x")
	if !st.View.Plain() || h.f.TypingFocused() {
		t.Error("ctrl+x in the filter bar didn't clear the filter")
	}
	if g.curRow != 990 {
		t.Errorf("clearing didn't keep the record: row %d", g.curRow)
	}
	// history: up recalls the filter
	h.press("/", "up")
	if h.f.Value() != "id % 10 = 0" {
		t.Errorf("history: %q", h.f.Value())
	}
	h.press("down")
	if h.f.Value() != "" {
		t.Errorf("history down: %q", h.f.Value())
	}
	h.press("esc")
}

func TestEscCancelsCount(t *testing.T) {
	ds := newFake(1000, 3)
	ds.countGate = make(chan struct{})
	h := newHarness(t, ds, 80, 20)
	h.filterWith("id % 7 = 0")
	if !h.env.Tasks.Running("count") {
		t.Fatal("no count running")
	}
	h.press("esc")
	h.waitFor("the count to be cancelled", func() bool { return ds.countCancels() == 1 })
	h.settle()
	if h.env.Tasks.Running("count") || h.env.State.Total != -1 {
		t.Errorf("count running %v, total %d after Esc", h.env.Tasks.Running("count"), h.env.State.Total)
	}
	if !h.noted("Cancelled running queries") {
		t.Errorf("no notice of the cancel: %+v", h.notes)
	}
	if h.env.State.View.Where != "id % 7 = 0" {
		t.Error("Esc changed the view")
	}
}

func TestFilterReadErrorReverts(t *testing.T) {
	ds := newFake(1000, 3)
	h := newHarness(t, ds, 80, 20)
	h.press("down", "down")
	h.filterWith("bad")
	h.settle()
	g := h.g
	if !h.env.State.View.Plain() || !g.v.view.Plain() {
		t.Errorf("a filter whose read failed was kept: %q", h.env.State.View.Where)
	}
	if g.curRow != 2 {
		t.Errorf("cursor not restored: %d", g.curRow)
	}
	if !h.noted("Binder Error") || !strings.Contains(h.status.Text, "previous view kept") {
		t.Errorf("no error shown: %+v %+v", h.notes, h.status)
	}
	if !strings.Contains(strings.Join(h.grid(), "\n"), "r2") {
		t.Errorf("the old rows aren't shown:\n%s", strings.Join(h.grid(), "\n"))
	}
}

func TestGoTo(t *testing.T) {
	ds := newFake(1000, 3)
	h := newHarness(t, ds, 80, 20)
	h.press("g")
	if len(h.dialogs) != 1 || h.dialogs[0] != "goto" {
		t.Fatalf("g opened %v", h.dialogs)
	}
	for _, row := range []int64{500, 999, 12, 0} {
		h.send(kit.GotoMsg{Row: row})
		h.settle()
		if h.g.curRow != row {
			t.Errorf("go to %d: row %d", row, h.g.curRow)
		}
		if !strings.Contains(strings.Join(h.grid(), "\n"), "r"+itoa(row)) {
			t.Errorf("go to %d: row not shown", row)
		}
	}
	// without the dialogs (until WP10), a notice
	h2 := newHarness(t, newFake(10, 3), 80, 20, hopts{noDlg: true})
	h2.press("g")
	if !h2.noted("not built yet") {
		t.Errorf("no notice: %+v", h2.notes)
	}
}

// While the view's size isn't known, a jump past the rows known goes there
// and finds the end.
func TestGoToBeforeTheCount(t *testing.T) {
	ds := newFake(1000, 3)
	ds.countGate = make(chan struct{})
	h := newHarness(t, ds, 80, 20)
	h.filterWith("id % 2 = 0")
	h.press("ctrl+end")
	if !h.noted("Still counting rows…") || h.g.curRow != 0 {
		t.Errorf("Ctrl+End before the count: row %d, notes %+v", h.g.curRow, h.notes)
	}
	h.send(kit.GotoMsg{Row: 450})
	h.settle()
	if h.g.curRow != 450 || !strings.Contains(strings.Join(h.grid(), "\n"), "r900") {
		t.Errorf("go to 450 before the count: row %d", h.g.curRow)
	}
	h.send(kit.GotoMsg{Row: 5000})
	h.settle()
	if fr := h.g.fileRowAt(h.g.curRow); h.g.curRow >= 500 || fr != 2*h.g.curRow {
		t.Errorf("go to past the end: row %d (file row %d), limit %d", h.g.curRow, fr, h.g.v.limit())
	}
	close(ds.countGate)
	h.settle()
}

func TestMouse(t *testing.T) {
	ds := newFake(1000, 4)
	h := newHarness(t, ds, 80, 20)
	g := h.g
	h.send(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 10, Y: 10})
	h.settle()
	if g.top != 3 || g.curRow != 3 {
		t.Errorf("wheel down: top %d row %d, want 3 3", g.top, g.curRow)
	}
	h.send(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 10, Y: 10})
	h.settle()
	if g.top != 0 {
		t.Errorf("wheel up: top %d", g.top)
	}
	// click on the second body row, in the third column
	slots := g.layout()
	x := g.x + slots[2].x + 1
	y := g.y + headerRows + 1
	h.send(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	h.settle()
	if g.curRow != 1 || g.curCol != 2 {
		t.Errorf("click: cursor (%d,%d), want (1,2)", g.curRow, g.curCol)
	}
	// a second click on the cursor's cell toggles the details pane
	h.send(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	h.settle()
	if !h.env.State.DetailOpen {
		t.Error("a click on the cursor's cell didn't open the details pane")
	}
	h.send(tea.MouseClickMsg{Button: tea.MouseLeft, X: 10, Y: h.f.Pos()})
	h.settle()
	if !h.f.TypingFocused() {
		t.Error("a click on the filter bar didn't focus it")
	}
}

func TestHorizontalScroll(t *testing.T) {
	ds := newFake(1000, 300)
	h := newHarness(t, ds, 100, 20)
	first := ds.fetches()[0]
	if len(first.cols) >= 300 || len(first.cols) < 5 {
		t.Errorf("first fetch asked for %d columns, want those near the screen", len(first.cols))
	}
	hdr := h.grid()[0]
	if !strings.HasSuffix(hdr, "›") || strings.HasPrefix(hdr, "‹") {
		t.Errorf("edge markers at the left: %q", hdr)
	}
	ds.clearLog()
	h.press("end")
	if h.g.left == 0 {
		t.Fatal("End didn't scroll right")
	}
	calls := ds.log()
	if len(calls) == 0 || calls[len(calls)-1].kind != "columns" || !slices.Contains(calls[len(calls)-1].cols, "c299") {
		t.Errorf("columns scrolled in weren't read: %+v", calls)
	}
	hdr = h.grid()[0]
	if !strings.HasPrefix(hdr, "‹") || strings.HasSuffix(hdr, "›") || !strings.Contains(hdr, "c299") {
		t.Errorf("header after End: %q", hdr)
	}
	if !strings.Contains(h.grid()[headerRows], "299") {
		t.Errorf("first row after End: %q", h.grid()[headerRows])
	}
}

func TestViewSmallGrid(t *testing.T) {
	ds := newFake(5, 3)
	h := newHarness(t, ds, 62, 18)
	want := []string{
		"      id  name  c002",
		"     i64  str    i64",
		"  0    0  r0       2",
		"  1    1  r1    1002",
		"  2    2  r2    2002",
		"  3    3  r3    3002",
		"  4    4  r4    4002",
		"                                                        ",
	}
	gl := h.grid()
	for i, w := range want {
		if i >= len(gl) || strings.TrimRight(gl[i], " ") != strings.TrimRight(w, " ") {
			t.Errorf("line %d:\n got %q\nwant %q", i, gl[min(i, len(gl)-1)], w)
		}
	}
	// the cursor cell is in reverse video, numbers right aligned
	raw := h.gridRaw()[headerRows]
	if !strings.Contains(raw, "\x1b[7m") {
		t.Errorf("cursor cell not reversed: %q", raw)
	}
	// without focus it is underlined
	h.g.Blur()
	if raw := h.gridRaw()[headerRows]; !strings.Contains(raw, "4m") {
		t.Errorf("cursor cell not underlined without focus: %q", raw)
	}
	h.g.Focus()
	// secondary text is dim
	if !strings.Contains(h.gridRaw()[1], "\x1b[2m") {
		t.Error("types aren't dim")
	}
}

func TestCommas(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -1234: "-1,234", -999: "-999"} {
		if got := commas(n); got != want {
			t.Errorf("commas(%d) = %q, want %q", n, got, want)
		}
	}
}

// A count cancelled with Esc whose result arrives after a newer count of
// the same view has started must not stop the newer one.
func TestLateCancelledCountIsIgnored(t *testing.T) {
	ds := newFake(1000, 3)
	ds.countGate = make(chan struct{})
	h := newHarness(t, ds, 80, 20)
	h.filterWith("id % 7 = 0")
	h.send(kp("esc")) // cancel the count; its message is still on the way
	if h.env.Tasks.Running("count") {
		t.Fatal("Esc didn't stop the count")
	}
	h.filterWith("id % 7 = 0") // the same filter again: counts again
	if !h.env.Tasks.Running("count") {
		t.Fatal("re-applying didn't count again")
	}
	close(ds.countGate)
	h.waitFor("the new count", func() bool { return !h.env.Tasks.Running("count") })
	h.settle()
	if h.env.State.Total != 143 {
		t.Errorf("total %d, want 143", h.env.State.Total)
	}
}

// Cancelled work that returns its own error (DuckDB's "INTERRUPT Error")
// rather than context.Canceled is still cancelled work, not a failure.
func TestInterruptErrorIsNotAFailure(t *testing.T) {
	ds := newFake(1_000_000, 3)
	ds.countGate = make(chan struct{})
	ds.interruptErr = errors.New("INTERRUPT Error: Interrupted!")
	h := newHarness(t, ds, 80, 20)
	ds.gate = make(chan struct{})
	h.filterWith("id % 3 = 0")
	h.press("ctrl+x") // nothing typed: … the grid has focus; ctrl+x clears
	h.filterWith("id % 3 = 0")
	h.send(kit.GotoMsg{Row: 200000}) // supersedes the first fetch of the filtered view
	h.waitFor("the superseded fetch", func() bool { return ds.cancels() >= 1 })
	h.settle()
	if h.env.State.View.Where != "id % 3 = 0" || h.g.failed != nil || h.noted("Query failed") {
		t.Errorf("a superseded fetch counted as a failure: view %q, notes %+v", h.env.State.View.Where, h.notes)
	}
	h.press("esc") // cancels the count and the fetch
	h.waitFor("the cancelled count", func() bool { return ds.countCancels() >= 1 })
	h.settle()
	if h.noted("Query failed") || h.noted("count failed") || h.status.Severity == kit.Error {
		t.Errorf("a cancelled count counted as a failure: %+v %+v", h.notes, h.status)
	}
	close(ds.gate)
}

// Window positions come from the request: an empty Window{} for rows at 50
// says the view ends at row 50 or before, not that it is empty.
func TestEmptyWindowAtOffset(t *testing.T) {
	ds := newFake(1000, 3)
	ds.countGate = make(chan struct{})
	h := newHarness(t, ds, 80, 20)
	h.filterWith("id % 10 = 0")
	gen := h.g.v.gen
	h.g.Update(kit.DoneMsg{Tag: "page", Msg: pageResult{req: fetchReq{gen: gen, start: 50, n: 30, cols: []string{"id"}}, win: data.Window{}}})
	if read := int64(2 * h.g.bodyH()); h.g.v.limit() != 50 || h.g.v.lastRow() != read-1 {
		t.Errorf("limit %d, last row %d; want 50, %d (the rows read)", h.g.v.limit(), h.g.v.lastRow(), read-1)
	}
	if !strings.Contains(strings.Join(h.grid(), "\n"), "r10") {
		t.Errorf("the rows before 50 are gone:\n%s", strings.Join(h.grid(), "\n"))
	}
	close(ds.countGate)
	h.settle()
}

func TestEscCancelsFetch(t *testing.T) {
	ds := newFake(1000, 3)
	ds.gate = make(chan struct{})
	h := newHarness(t, ds, 80, 20)
	if !h.env.Tasks.Running("page") {
		t.Fatal("no fetch running")
	}
	h.press("esc")
	h.waitFor("the cancelled fetch", func() bool { return ds.cancels() >= 1 })
	h.settle()
	if h.env.Tasks.Running("page") || h.g.failed != nil || h.noted("failed") {
		t.Errorf("after Esc: running %v, failed %v, notes %+v", h.env.Tasks.Running("page"), h.g.failed, h.notes)
	}
	if !strings.Contains(h.grid()[headerRows], "·") {
		t.Errorf("placeholders gone: %q", h.grid()[headerRows])
	}
	close(ds.gate)
	h.press("down") // the next move fetches again
	h.waitFor("the refetch", func() bool { return !h.env.Tasks.Running("page") && len(ds.fetches()) == 2 })
	h.settle()
	if !strings.Contains(strings.Join(h.grid(), "\n"), "r0") {
		t.Errorf("rows not read after Esc and a move:\n%s", strings.Join(h.grid(), "\n"))
	}
}

func TestReapplyFilterRestartsCount(t *testing.T) {
	ds := newFake(1000, 3)
	ds.countGate = make(chan struct{})
	h := newHarness(t, ds, 80, 20)
	h.filterWith("id % 10 = 0")
	h.press("esc")
	if h.env.Tasks.Running("count") {
		t.Fatal("Esc didn't stop the count")
	}
	gen := h.g.v.gen
	h.press("/", "enter")
	if !h.env.Tasks.Running("count") || h.g.v.gen != gen {
		t.Errorf("re-applying the filter: counting %v, gen %d (was %d)", h.env.Tasks.Running("count"), h.g.v.gen, gen)
	}
	close(ds.countGate)
	h.waitFor("the count", func() bool { return !h.env.Tasks.Running("count") })
	h.settle()
	if h.env.State.Total != 100 {
		t.Errorf("total %d", h.env.State.Total)
	}
}

// When a short read finds the end before the count arrives, the grid
// stops there.
func TestShortReadShowsTotal(t *testing.T) {
	ds := newFake(1000, 3)
	ds.countGate = make(chan struct{})
	h := newHarness(t, ds, 80, 20)
	h.filterWith("id % 200 = 0")
	if h.g.v.limit() != 5 {
		t.Errorf("limit with a short read: %d", h.g.v.limit())
	}
	h.press("ctrl+end") // the end found is the end
	if h.g.curRow != 4 || h.env.State.Total != 5 {
		t.Errorf("Ctrl+End after the end was found: row %d, total %d", h.g.curRow, h.env.State.Total)
	}
	h.press("ctrl+home", "down", "down", "down", "down", "down", "down")
	if h.g.curRow != 4 {
		t.Errorf("cursor went past the end found: %d", h.g.curRow)
	}
	h.filterWith("none")
	if h.g.v.limit() != 0 || strings.Contains(strings.Join(h.grid()[headerRows:], ""), "·") {
		t.Errorf("no rows: limit %d\n%s", h.g.v.limit(), strings.Join(h.grid(), "\n"))
	}
	close(ds.countGate)
	h.settle()
}

func TestTinyTerminal(t *testing.T) {
	ds := newFake(1000, 3)
	h := newHarness(t, ds, 80, 20)
	for _, size := range [][2]int{{80, 9}, {20, 20}, {10, 3}, {4, 12}} {
		h.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		h.settle()
		for i, l := range strings.Split(h.screen(), "\n") {
			if ansi.StringWidth(l) > size[0] {
				t.Errorf("%dx%d: line %d too wide: %q", size[0], size[1], i, l)
			}
		}
		h.press("down", "right", "end", "pgdown")
	}
}
