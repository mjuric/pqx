package grid

// Tests from the independent review of WP7: text from the file never
// reaches the terminal raw; actions waiting on rows; memory; notices; and
// the behaviours the design lists as "carry over exactly".

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/cells"
	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// sgr matches the style sequences the screen is drawn with.
var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// raw reports what in s would reach the terminal as a control: ESC (once
// style sequences are taken out), BEL, other C0 controls, C1 controls.
func rawControls(s string) []string {
	var out []string
	for i, r := range sgr.ReplaceAllString(s, "") {
		if r == '\n' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || (r == utf8.RuneError && !strings.HasPrefix(s[i:], "\uFFFD")) {
			out = append(out, string(r))
		}
	}
	return out
}

const hostile = "\x1b]0;PWN\x07f\u009b31m\x1b[2J\tx\ny"

func hostileFake() *fakeDS {
	ds := newFake(200, 4)
	st := arrow.StructOf(arrow.Field{Name: hostile, Type: arrow.PrimitiveTypes.Int64})
	ds.cols = append(ds.cols,
		data.Column{Name: "s" + hostile, Type: "STRUCT", Arrow: st, Unit: "u" + hostile},
		data.Column{Name: "txt", Type: "VARCHAR", Arrow: arrow.BinaryTypes.String, Unit: hostile})
	ds.special = func(name string, fr int64) (data.Value, bool) {
		switch name {
		case "s" + hostile:
			return data.Struct{{Name: hostile, Value: fr}}, true
		case "txt":
			return "v" + hostile, true
		}
		return nil, false
	}
	return ds
}

func TestHostileTextNeverReachesTheScreen(t *testing.T) {
	ds := hostileFake()
	h := newHarness(t, ds, 220, 30)
	check := func(what string) {
		t.Helper()
		if bad := rawControls(h.raw()); len(bad) > 0 {
			t.Errorf("%s: the screen holds %q", what, bad)
		}
		for _, l := range h.gridRaw() {
			if bad := rawControls(l); len(bad) > 0 {
				t.Errorf("%s: the grid holds %q in %q", what, bad, l)
			}
		}
		if s := h.g.Subtitle().Plain; len(rawControls(s)) > 0 {
			t.Errorf("%s: subtitle %q", what, s)
		}
		for _, n := range h.notes {
			if len(rawControls(n.Text+n.Title)) > 0 && !strings.Contains(n.Text, "\n") {
				t.Errorf("%s: notice %q", what, n.Text)
			}
			if strings.ContainsAny(n.Text+n.Title, "\x1b\x07\u009b") {
				t.Errorf("%s: notice %q", what, n.Text)
			}
		}
		if strings.ContainsAny(h.status.Text, "\x1b\x07\u009b\n\t") {
			t.Errorf("%s: status %q", what, h.status.Text)
		}
	}
	check("first screen")
	h.press("end")
	check("end")
	h.press("f")
	check("raw")
	h.press("y", "-", "left", "-")
	check("copy and hide")
	h.env.State.Current = "s" + hostile
	h.send(kit.ColumnChangedMsg{From: "schema"})
	h.settle()
	check("hidden hint")
	h.send(kit.SetViewMsg{View: data.View{Where: "txt = 'v" + hostile + "'"}, KeepFileRow: -1})
	h.settle()
	check("a filter holding controls")
	ds.failFrom = 150
	h.send(kit.GotoMsg{Row: 199})
	h.settle()
	check("a read error holding controls")
	if !h.noted("broken") {
		t.Errorf("no read error shown: %+v", h.notes)
	}
}

// A waiter for a row whose read was cancelled or failed must not fire when
// the row arrives later for another reason.
func TestWaiterDroppedWhenItsReadStops(t *testing.T) {
	ds := newFake(100_000, 4)
	h := newHarness(t, ds, 120, 30)
	ds.gate = make(chan struct{})
	h.send(kit.GotoMsg{Row: 50_000})
	h.send(kp("y"))
	h.send(kp("esc"))
	h.settle()
	close(ds.gate)
	ds.gate = nil
	h.press("down")
	h.settle()
	if len(h.copies) != 0 {
		t.Errorf("a copy happened after Esc: %q", h.copies)
	}

	// a failed read
	ds2 := newFake(100_000, 4)
	h2 := newHarness(t, ds2, 120, 30)
	ds2.failFrom = 60_000
	h2.send(kit.GotoMsg{Row: 70_000})
	h2.send(kp("y"))
	h2.settle()
	ds2.failFrom = 0
	h2.press("up", "down")
	if len(h2.copies) != 0 {
		t.Errorf("a copy happened after a failed read: %q", h2.copies)
	}
}

// y pressed again while the row loads copies once.
func TestRepeatedActionWhileLoadingHappensOnce(t *testing.T) {
	ds := newFake(100_000, 4)
	h := newHarness(t, ds, 120, 30)
	ds.gate = make(chan struct{})
	h.send(kit.GotoMsg{Row: 50_000})
	for i := 0; i < 4; i++ {
		h.send(kp("y"))
	}
	close(ds.gate)
	h.settle()
	n := 0
	for _, x := range h.notes {
		if strings.Contains(x.Text, "Copied") {
			n++
		}
	}
	if len(h.copies) != 1 || n != 1 {
		t.Errorf("%d copies, %d notices", len(h.copies), n)
	}
}

func heap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// The cache stays bounded whatever the jumps; eviction rebuilds the maps.
func TestEvictionKeepsMemoryBounded(t *testing.T) {
	ds := newFake(10_000_000, 79)
	h := newHarness(t, ds, 400, 60)
	g := h.g
	before := heap()
	peak := before
	for i := 0; i < 400; i++ {
		h.send(kit.GotoMsg{Row: int64(i) * 20_011})
		h.settle()
		h.press("end", "home")
		if i%40 == 39 {
			peak = max(peak, heap())
		}
	}
	n := len(g.v.fileRow)
	if n > cacheLimit+3*g.bodyH() {
		t.Errorf("%d rows cached, limit %d", n, cacheLimit)
	}
	for name, col := range g.v.vals {
		if len(col) > cacheLimit+3*g.bodyH() {
			t.Errorf("%s holds %d values", name, len(col))
		}
	}
	for name, col := range g.v.text {
		if len(col) > cacheLimit+3*g.bodyH() {
			t.Errorf("%s holds %d formatted cells", name, len(col))
		}
	}
	after := heap()
	t.Logf("heap %d MB before, %d MB at the peak, %d MB after, %d rows cached", before>>20, peak>>20, after>>20, n)
	if peak > before+100<<20 {
		t.Errorf("heap grew by %d MB", (peak-before)>>20)
	}
}

// A column that fails says so once per view, not on every page.
func TestFailedColumnsNoticeOnce(t *testing.T) {
	ds := newFake(100_000, 120)
	h := newHarness(t, ds, 150, 42)
	ds.colsHook = func(context.Context, []string) error { return errors.New("boom") }
	h.press("end")
	for i := 0; i < 5; i++ {
		h.press("pgdown")
	}
	n := 0
	for _, x := range h.notes {
		if x.Title == "✗ Columns" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d column notices, want 1", n)
	}
}

// --format formats are used, never written to formats.yaml.
func TestSessionFormatsAreNeverSaved(t *testing.T) {
	ds := openFixture(t, "demo")
	h := newHarness(t, ds, 150, 42, hopts{session: map[string]fmtx.Override{"dec": {Digits: 2, Set: true}}})
	h.env.State.Current = "dec"
	h.send(kit.ColumnChangedMsg{From: "schema"})
	h.settle()
	h.press(">", "<")
	h.send(kit.FormatSetMsg{Column: "dec", Override: fmtx.Override{Spec: ".1f", Set: true}})
	h.settle()
	_ = filepath.WalkDir(h.cfg, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("a session format wrote %s", p)
		}
		return nil
	})
}

// A new view of the same columns keeps the leftmost column; clearing keeps
// the record on its screen row.
func TestViewportKeptAcrossViews(t *testing.T) {
	ds := newFake(10_000, 60)
	h := newHarness(t, ds, 150, 42)
	g := h.g
	h.press("end")
	for i := 0; i < 10; i++ {
		h.press("left")
	}
	left := g.cols[g.left].Name
	if g.left == 0 {
		t.Fatal("not scrolled")
	}
	h.filterWith("id % 3 = 0")
	if g.cols[g.left].Name != left {
		t.Errorf("leftmost column %q after a filter, want %q", g.cols[g.left].Name, left)
	}
	// the record on screen row 5 stays there when the filter is cleared
	h.send(kit.GotoMsg{Row: 700})
	h.settle()
	for g.curRow-g.top != 5 {
		if g.curRow-g.top > 5 {
			h.press("up")
		} else {
			h.press("down")
		}
	}
	fr := g.fileRowAt(g.curRow)
	h.press("x")
	if !h.env.State.View.Plain() || g.curRow != fr || g.curRow-g.top != 5 {
		t.Errorf("after x: row %d (want file row %d), screen row %d", g.curRow, fr, g.curRow-g.top)
	}
	if g.cols[g.left].Name != left {
		t.Errorf("leftmost column %q after clearing, want %q", g.cols[g.left].Name, left)
	}
}

// A jump near the end leaves no empty rows at the bottom.
func TestNoEmptyRowsAtTheBottom(t *testing.T) {
	h := newHarness(t, newFake(1000, 4), 120, 30)
	h.send(kit.GotoMsg{Row: 997})
	h.settle()
	g := h.g
	if g.top != 1000-int64(g.bodyH()) {
		t.Errorf("top %d, want %d", g.top, 1000-int64(g.bodyH()))
	}
	gl := h.grid()
	if strings.TrimSpace(gl[len(gl)-1]) == "" {
		t.Error("an empty row at the bottom")
	}
}

// A binary column (widest not guessable) that widens only when drawn, past
// the screen's edge with the cursor on it: the draw scrolls it back.
func TestFitVisibleScrollsTheCursorBack(t *testing.T) {
	ds := newFake(1000, 30)
	ds.cols = append(ds.cols, data.Column{Name: "blob", Type: "BLOB", Arrow: arrow.BinaryTypes.Binary})
	ds.special = func(name string, fr int64) (data.Value, bool) {
		if name != "blob" {
			return nil, false
		}
		if fr == 7 {
			return []byte(strings.Repeat("z", 40)), true
		}
		return []byte{}, true
	}
	h := newHarness(t, ds, 150, 42)
	g := h.g
	h.press("end")
	h.grid()
	if !g.cursorInView() {
		t.Errorf("cursor off screen: left %d, blob %d wide", g.left, g.colWidth("blob"))
	}
	drawnCellsFit(t, h, "binary")
}

// Widths come from a sample of the rows read when the widest can't be
// guessed: rows read below the screen widen the column before they show.
func TestSampledWidthsForUnguessableColumns(t *testing.T) {
	ds := newFake(1000, 3)
	ds.cols = append(ds.cols, data.Column{Name: "blob", Type: "BLOB", Arrow: arrow.BinaryTypes.Binary})
	h0 := newHarness(t, newFake(1000, 3), 120, 30)
	n := int64(h0.g.bodyH())
	ds.special = func(name string, fr int64) (data.Value, bool) {
		if name != "blob" {
			return nil, false
		}
		if fr >= n {
			return []byte(strings.Repeat("q", 30)), true
		}
		return []byte{1}, true
	}
	h := newHarness(t, ds, 120, 30)
	long := cells.Width(fmtx.Format([]byte(strings.Repeat("q", 30)), fmtx.KindBinary, fmtx.Opts{Width: fmtx.DefaultWidth}))
	if got := h.g.colWidth("blob"); got < long {
		t.Errorf("blob %d wide, want %d from the rows read below the screen", got, long)
	}
}

// A column cut by the right edge counts as hidden, and is drawn cut, its
// numbers in place (right-justified).
func TestClippedColumn(t *testing.T) {
	ds := newFake(1000, 40)
	h := newHarness(t, ds, 100, 30)
	g := h.g
	h.grid()
	slots := g.layout()
	last := slots[len(slots)-1]
	if !last.clipped {
		t.Skip("no column cut at this width")
	}
	_, lastFull, _, hr := g.colWindow()
	if lastFull != last.col-1 || hr != len(g.cols)-last.col {
		t.Errorf("last visible %d, hidden right %d; clipped column %d", lastFull, hr, last.col)
	}
	c := g.cols[last.col]
	tx, _ := g.cellText(last.col, g.top)
	full := strings.Repeat(" ", pad+g.colWidth(c.Name)-tx.w) + tx.plain + " "
	if !c.right {
		full = " " + tx.plain + strings.Repeat(" ", g.colWidth(c.Name)-tx.w+pad)
	}
	line := h.grid()[headerRows]
	got := ansi.Cut(line, last.x, last.x+last.sw)
	if got != full[:last.sw] {
		t.Errorf("clipped cell %q, want %q", got, full[:last.sw])
	}
}

// A read that failed isn't retried for the same rows; a short read isn't
// read again either.
func TestFailedAndShortReadsAreNotRetried(t *testing.T) {
	ds := newFake(1000, 4)
	h := newHarness(t, ds, 120, 30)
	ds.failFrom = 500
	h.send(kit.GotoMsg{Row: 600})
	h.settle()
	n := len(ds.fetches())
	h.press("down", "up", "down")
	if len(ds.fetches()) != n {
		t.Errorf("a failed read was retried: %d -> %d reads", n, len(ds.fetches()))
	}

	ds2 := newFake(1000, 4)
	ds2.shortAt = 900 // the view says 1,000 rows but has 900
	h2 := newHarness(t, ds2, 120, 30)
	h2.send(kit.GotoMsg{Row: 990})
	h2.settle()
	n = len(ds2.fetches())
	h2.press("up", "down")
	if len(ds2.fetches()) != n {
		t.Errorf("a short read was read again: %d -> %d reads", n, len(ds2.fetches()))
	}
}

// Columns read for rows evicted (or renumbered) meanwhile aren't stored;
// a failure marks only rows read.
func TestColumnResultsAreCheckedAgainstTheCache(t *testing.T) {
	h := newHarness(t, newFake(1000, 4), 120, 30)
	g := h.g
	gen := g.v.gen
	w := data.Window{Len: 2, FileRows: []int64{900, 901}, Cols: map[string][]data.Value{"zz": {int64(1), int64(2)}}}
	g.byName["zz"] = 0
	g.onCols(colsResult{req: colsReq{gen: gen, tag: "cols", rows: []int64{0, 1}, fileRows: []int64{900, 901}, cols: []string{"zz"}}, win: w})
	if _, ok := g.v.cell("zz", 0); ok {
		t.Error("values for other file rows were stored")
	}
	delete(g.byName, "zz")
	g.markFailed(colsReq{rows: []int64{0, 999}}, map[string]error{"q": nil})
	if _, ok := g.v.cell("q", 999); ok {
		t.Error("a row not read was marked failed")
	}
	if v, _ := g.v.cell("q", 0); v != (failedCell{}) {
		t.Error("a row read wasn't marked failed")
	}
}

// After a jump past the end of a view of unknown size the cursor goes back
// to the rows known; the end found is announced.
func TestJumpPastTheEnd(t *testing.T) {
	ds := newFake(1000, 3)
	ds.countGate = make(chan struct{})
	h := newHarness(t, ds, 80, 20)
	h.filterWith("id % 2 = 0")
	known := h.g.v.known
	h.send(kit.GotoMsg{Row: 5000})
	h.settle()
	if h.g.v.hope != 0 || h.g.curRow != known-1 {
		t.Errorf("hope %d, row %d (known %d)", h.g.v.hope, h.g.curRow, known)
	}
	totals := h.totals
	h.send(kit.GotoMsg{Row: 490})
	h.settle()
	if h.env.State.Total != 500 || h.totals == totals {
		t.Errorf("end found: total %d, TotalMsg %d -> %d", h.env.State.Total, totals, h.totals)
	}
	close(ds.countGate)
	h.settle()
}

// The lazy columns read reaches two screens right of the view.
func TestColumnsReadTwoScreensAhead(t *testing.T) {
	ds := newFake(lazyRows, lazyCols)
	h := newHarness(t, ds, 150, 42)
	var cols []string
	for _, c := range ds.log() {
		cols = append(cols, c.cols...)
	}
	two := h.g.nearNames(2)
	for _, n := range two {
		if !strings.Contains(strings.Join(cols, ","), n) {
			t.Errorf("%s (within two screens) not read", n)
		}
	}
	if len(h.g.nearNames(1)) >= len(two) {
		t.Error("two screens hold no more columns than one")
	}
}

// Reserved widths are at most reserveCap.
func TestReservedWidthCap(t *testing.T) {
	h := newHarness(t, newFake(100, 3), 120, 30)
	g := h.g
	h.env.State.Raw = true
	g.footer = map[string][2]data.Value{"name": {strings.Repeat("w", 100), "a"}}
	if w := g.reservedWidth(g.cols[g.byName["name"]]); w != reserveCap {
		t.Errorf("reserved %d, want %d", w, reserveCap)
	}
}

// A click on ‹ pages left so the old first column is the last one shown;
// a click below the last row does nothing.
func TestPageLeftAndClickBelowTheRows(t *testing.T) {
	h := newHarness(t, newFake(5, 60), 120, 30)
	g := h.g
	h.press("end")
	first, _, _, _ := g.colWindow()
	h.send(tea.MouseClickMsg{Button: tea.MouseLeft, X: g.x, Y: g.y + 3})
	h.settle()
	if _, last, _, _ := g.colWindow(); last != first-1 || !g.fits(g.left, first-1) || (g.left > 0 && g.fits(g.left-1, first-1)) {
		t.Errorf("after ‹: left %d, last shown %d, want %d", g.left, last, first-1)
	}
	row, col := g.curRow, g.curCol
	h.send(tea.MouseClickMsg{Button: tea.MouseLeft, X: g.x + g.layout()[0].x + 1, Y: g.y + headerRows + 8})
	h.settle()
	if g.curRow != row || g.curCol != col || h.env.State.DetailOpen {
		t.Errorf("a click below the rows moved the cursor to (%d,%d)", g.curRow, g.curCol)
	}
}
