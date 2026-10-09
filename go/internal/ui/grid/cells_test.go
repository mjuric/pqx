package grid

// tests/test_cells.py's UI tests: cells formatted when drawn, columns that
// only grow and never show a value cut off, raw and format changes.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/cells"
	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// readCells is the number of cells cached for the view.
func readCells(g *Grid) (int, int) {
	n, cols := 0, 0
	for _, col := range g.v.vals {
		if len(col) > 0 {
			cols++
		}
		n += len(col)
	}
	return n, cols
}

func TestWindowLoadFormatsOnlyWhatIsDrawn(t *testing.T) {
	ds := newFake(2000, 120)
	h := newHarness(t, ds, 150, 42)
	g := h.g
	n, cols := readCells(g)
	// what's drawn plus a dozen or so candidates per column, and the
	// reserved widths of the columns not read
	budget := (g.bodyH()+widthSampleRows+4)*cols + len(g.cols)
	if budget >= n {
		t.Fatalf("budget %d isn't below the %d cells read", budget, n)
	}
	if g.formats == 0 || g.formats > budget {
		t.Errorf("formatted %d values for a first screen, budget %d", g.formats, budget)
	}
	for _, key := range []string{"ctrl+end", "f", "f", "pgdown"} {
		g.formats = 0
		h.press(key)
		_, cols := readCells(g)
		budget := (3*g.bodyH()+widthSampleRows+4)*cols + len(g.cols)
		if g.formats > budget {
			t.Errorf("%s: formatted %d values, budget %d", key, g.formats, budget)
		}
	}
}

// drawnCellsFit checks that every cell on screen fits its column.
func drawnCellsFit(t *testing.T, h *harness, what string) {
	t.Helper()
	g := h.g
	h.grid()
	n := int64(g.bodyH())
	checked := 0
	for _, s := range g.layout() {
		if s.clipped {
			continue
		}
		c := g.cols[s.col]
		for r := g.top; r < g.top+n; r++ {
			v, ok := g.v.cell(c.Name, r)
			if !ok || !g.v.loaded(r) {
				continue
			}
			if _, bad := v.(failedCell); bad {
				continue
			}
			w := cells.Width(fmtx.Format(v, c.kind, g.opts(c)))
			if w > s.w {
				t.Errorf("%s: %s row %d is %d wide in a column of %d", what, c.Name, r, w, s.w)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Errorf("%s: no cells checked", what)
	}
}

func TestDrawnCellsAreNeverCutOff(t *testing.T) {
	sets := map[string]func(t *testing.T) data.Dataset{
		"fake": func(t *testing.T) data.Dataset { return newFake(3000, 120) },
		"demo": func(t *testing.T) data.Dataset { return openFixture(t, "demo") },
		"odd":  func(t *testing.T) data.Dataset { return openFixture(t, "odd") },
	}
	for name, open := range sets {
		h := newHarness(t, open(t), 150, 42)
		for _, keys := range [][]string{{}, {"f"}, {"pgdown", "pgdown", "pgdown"}, {"f"}, {"ctrl+end"},
			{"pgup", "pgup"}, {"end"}, {"f"}, {"ctrl+home"}} {
			h.press(keys...)
			drawnCellsFit(t, h, name+" "+strings.Join(keys, " "))
		}
	}
}

func TestColumnGrowsWhenAWiderCellIsDrawn(t *testing.T) {
	ds := newFake(1000, 4)
	long := strings.Repeat("x", 30)
	ds.special = func(name string, fr int64) (data.Value, bool) {
		if name == "name" && fr == 503 {
			return long, true
		}
		return nil, false
	}
	h := newHarness(t, ds, 120, 30)
	g := h.g
	w0 := g.colWidth("name")
	if w0 >= len(long) {
		t.Fatalf("name is %d wide before row 503 was read", w0)
	}
	h.send(kit.GotoMsg{Row: 503})
	h.settle()
	if got := g.colWidth("name"); got != len(long) {
		t.Errorf("name is %d wide with row 503 on screen, want %d", got, len(long))
	}
	if !strings.Contains(strings.Join(h.grid(), "\n"), long) {
		t.Error("the long value isn't drawn whole")
	}
	h.press("ctrl+home")
	if got := g.colWidth("name"); got != len(long) {
		t.Errorf("name narrowed again: %d", got)
	}
}

func TestRawToggleAndFormatChangeRedraw(t *testing.T) {
	ds := openFixture(t, "demo")
	h := newHarness(t, ds, 150, 42)
	g := h.g
	ra := g.byName["ra"]
	h.send(kit.ColumnChangedMsg{From: "schema"})
	h.env.State.Current = "ra"
	h.send(kit.ColumnChangedMsg{From: "schema"})
	h.settle()
	if g.curCol != ra {
		t.Fatalf("cursor on column %d, want ra (%d)", g.curCol, ra)
	}
	v, _ := g.v.cell("ra", g.curRow)
	c := g.cols[ra]
	want := func() string { return fmtx.Format(v, c.kind, g.opts(c)) }
	cur := func() string { t, _ := g.cellText(ra, g.curRow); return t.plain }
	widths := func() map[string]int {
		m := map[string]int{}
		for _, c := range g.cols {
			m[c.Name] = g.colWidth(c.Name)
		}
		return m
	}
	if cur() != want() || !strings.Contains(h.grid()[headerRows], want()) {
		t.Errorf("ra = %q, want %q", cur(), want())
	}
	w0 := widths()
	h.press("f")
	if !h.env.State.Raw || cur() != fmtx.Format(v, c.kind, fmtx.Opts{Raw: true, Width: fmtx.DefaultWidth}) {
		t.Errorf("raw ra = %q", cur())
	}
	wRaw := widths()
	for n, w := range w0 {
		if wRaw[n] < w {
			t.Errorf("%s narrowed with raw values: %d -> %d", n, w, wRaw[n])
		}
	}
	h.press("f")
	if cur() != want() {
		t.Errorf("ra back from raw = %q, want %q", cur(), want())
	}
	for n, w := range widths() {
		if w != wRaw[n] {
			t.Errorf("%s changed width back from raw: %d -> %d (widths only grow)", n, wRaw[n], w)
		}
	}
	// a format change re-formats the column and lets it shrink to fit
	for _, o := range []fmtx.Override{{Digits: 8, Set: true}, {Digits: 0, Set: true}, {Spec: ".2e", Set: true}, {}} {
		h.send(kit.FormatSetMsg{Column: "ra", Override: o})
		h.settle()
		if h.env.State.Formats["ra"] != o && o.Set {
			t.Errorf("format of ra: %+v, want %+v", h.env.State.Formats["ra"], o)
		}
		if cur() != want() {
			t.Errorf("ra with %+v = %q, want %q", o, cur(), want())
		}
		a, b := g.header(c)
		need := max(cells.Width(a), cells.Width(b))
		n := int64(g.bodyH())
		for r := max(0, g.top-n); r < g.top+2*n; r++ {
			if x, ok := g.v.cell("ra", r); ok {
				need = max(need, cells.Width(fmtx.Format(x, c.kind, g.opts(c))))
			}
		}
		if got := g.colWidth("ra"); got != need {
			t.Errorf("ra with %+v is %d wide, want %d (fitted)", o, got, need)
		}
	}
}

func TestPinningFitsTheColumnsItBringsIntoView(t *testing.T) {
	ds := newFake(1000, 22)
	long := strings.Repeat("y", 40)
	ds.special = func(name string, fr int64) (data.Value, bool) {
		if name == "name" && fr == 503 {
			return long, true
		}
		return nil, false
	}
	h := newHarness(t, ds, 80, 30)
	g := h.g
	h.press("right")
	h.press("end")
	h.send(kit.GotoMsg{Row: 503})
	h.settle()
	if g.fits(1) {
		t.Fatalf("name isn't scrolled off the left: scroll %d", g.sx)
	}
	if g.colWidth("name") >= len(long) {
		t.Logf("name fitted already, from the rows sampled when it was read")
	}
	h.send(kit.ColumnChangedMsg{From: "x"}) // (no-op)
	g.curCol = 1
	h.press("p") // pins id and name: name comes into view without a scroll
	if g.pinned() != 2 {
		t.Fatalf("pinned %d", g.pinned())
	}
	h.grid()
	if g.colWidth("name") != len(long) {
		t.Errorf("name is %d wide when drawn pinned, want %d", g.colWidth("name"), len(long))
	}
	if !strings.Contains(strings.Join(h.grid(), "\n"), long) {
		t.Error("the long value isn't drawn whole")
	}
	h.press("p")
	if g.pinned() != 0 || h.env.State.Pinned != 0 {
		t.Errorf("p didn't unpin: %d", g.pinned())
	}
}

func TestCursorStaysInViewAtTheFarRight(t *testing.T) {
	ds := newFake(1000, 60)
	h := newHarness(t, ds, 150, 42)
	g := h.g
	for _, what := range []string{"s", "-", "c", "filter", "f", "f", ">", "<"} {
		h.press("end")
		if g.sx == 0 || !g.cursorInView() {
			t.Fatalf("%s: End: scroll %d, in view %v", what, g.sx, g.cursorInView())
		}
		switch what {
		case "c":
			h.press("c")
			h.send(kit.ColumnsPickedMsg{Visible: names(ds.cols)})
			h.settle()
		case "filter":
			h.filterWith("id % 3 = 0")
		default:
			h.press(what)
		}
		h.grid()
		if !g.cursorInView() {
			t.Errorf("%s: the cursor's cell is off screen", what)
		}
		if strings.Contains(h.grid()[0], "‹") == (g.sx == 0) {
			t.Errorf("%s: left marker wrong: %q", what, h.grid()[0])
		}
	}
}

func names(cols []data.Column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}

// The cursor's column outgrows its guessed width once its cells are drawn:
// the view follows, whether moved onto the usual way or set directly.
func TestCursorInViewWhenItsColumnWidensOnScreen(t *testing.T) {
	ds := newFake(3000, 31)
	ds.cols = append(ds.cols, data.Column{Name: "blob", Type: "VARCHAR", Arrow: ds.cols[1].Arrow})
	ds.special = func(name string, fr int64) (data.Value, bool) {
		if name != "blob" {
			return nil, false
		}
		if fr%62 == 0 {
			return "", true
		}
		if fr < 1500 {
			return strings.Repeat("b", 8), true
		}
		return strings.Repeat("B", 36), true
	}
	h := newHarness(t, ds, 150, 42)
	g := h.g
	short := g.colWidth("blob")
	h.press("end")
	if g.colWidth("blob") <= short && short < 8 {
		t.Errorf("blob didn't widen: %d", g.colWidth("blob"))
	}
	h.grid()
	if !g.cursorInView() {
		t.Error("the cursor's cell is off screen after End")
	}
	w := g.colWidth("blob")
	h.press("ctrl+end") // rows whose blobs are longer still
	h.grid()
	if g.colWidth("blob") <= w || !g.cursorInView() {
		t.Errorf("after Ctrl+End: blob %d wide (was %d), in view %v", g.colWidth("blob"), w, g.cursorInView())
	}
	for _, l := range h.grid() {
		if ansi.StringWidth(l) != g.w {
			t.Errorf("line %d wide, want %d: %q", ansi.StringWidth(l), g.w, l)
		}
	}
}
