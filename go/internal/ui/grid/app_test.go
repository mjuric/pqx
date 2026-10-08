package grid

// tests/test_app.py's grid parts, on the fixtures (go/testdata) and the
// fake dataset.

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// openFixture opens go/testdata/fixtures/<name>.parquet.
func openFixture(t *testing.T, name string) data.Dataset {
	t.Helper()
	ds, err := data.Open(filepath.Join("..", "..", "..", "testdata", "fixtures", name+".parquet"), data.Options{Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ds.Close() })
	return ds
}

func TestStartupAndNavigation(t *testing.T) {
	ds := openFixture(t, "demo")
	h := newHarness(t, ds, 150, 42)
	g := h.g
	if !g.focused || h.env.State.Total != 20_000 || h.env.State.Current != "diaSourceId" {
		t.Fatalf("focused %v, total %d, current %q", g.focused, h.env.State.Total, h.env.State.Current)
	}
	h.press("ctrl+end")
	if g.curRow != 19_999 {
		t.Errorf("Ctrl+End: row %d", g.curRow)
	}
	h.press("down")
	if g.curRow != 19_999 {
		t.Errorf("down at the end moved: %d", g.curRow)
	}
	h.press("ctrl+home")
	if g.curRow != 0 || g.top != 0 {
		t.Errorf("Ctrl+Home: row %d top %d", g.curRow, g.top)
	}
	for i := 0; i < 3*g.bodyH(); i++ {
		h.send(kp("down")) // across what was read first, seamlessly
	}
	h.settle()
	r := int64(3 * g.bodyH())
	if v, _ := g.v.cell("diaSourceId", r); g.curRow != r || v != int64(170_000_000_000_000_000)+r {
		t.Errorf("row %d: diaSourceId %v", g.curRow, v)
	}
	h.press("g")
	h.send(kit.GotoMsg{Row: 12_345})
	h.settle()
	if v, _ := g.v.cell("diaSourceId", 12_345); g.curRow != 12_345 || v != int64(170_000_000_000_000_000+12_345) {
		t.Errorf("g 12345: row %d, diaSourceId %v", g.curRow, v)
	}
	if !strings.Contains(strings.Join(h.grid(), "\n"), "12,345") {
		t.Error("row label 12,345 not shown")
	}
}

func TestSortCycle(t *testing.T) {
	ds := newFake(1000, 5)
	h := newHarness(t, ds, 120, 30)
	g := h.g
	h.press("right", "right")
	h.press("s")
	if ob := h.env.State.View.OrderBy; len(ob) != 1 || ob[0] != (data.Sort{Column: "c002"}) {
		t.Fatalf("s: order %+v", ob)
	}
	if a, _ := g.header(g.cols[2]); a != "c002 ↑" || g.curCol != 2 || g.curRow != 0 {
		t.Errorf("header %q, cursor (%d,%d)", a, g.curRow, g.curCol)
	}
	h.press("s")
	if ob := h.env.State.View.OrderBy; len(ob) != 1 || !ob[0].Desc {
		t.Fatalf("s s: order %+v", ob)
	}
	if v, _ := g.v.cell("id", 0); v != int64(999) {
		t.Errorf("descending: first id %v", v)
	}
	if a, _ := g.header(g.cols[2]); a != "c002 ↓" {
		t.Errorf("header %q", a)
	}
	h.press("s")
	if !h.env.State.View.Plain() {
		t.Errorf("s s s: view %+v", h.env.State.View)
	}
	// a click on a header sorts by its column
	s := g.layout()[1]
	h.send(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2 + s.x + 1, Y: 5})
	h.settle()
	if ob := h.env.State.View.OrderBy; len(ob) != 1 || ob[0].Column != "name" {
		t.Errorf("header click: order %+v", ob)
	}
	// the sort stays with a filter, and goes with x
	h.filterWith("id % 2 = 0")
	if v := h.env.State.View; v.Where != "id % 2 = 0" || len(v.OrderBy) != 1 {
		t.Errorf("filter after sort: %+v", v)
	}
	h.press("x")
	if !h.env.State.View.Plain() {
		t.Errorf("x: view %+v", h.env.State.View)
	}
	// SQL results are sorted in the query
	h.filterWith("select * from t")
	h.press("s")
	if !h.noted("Sort SQL results with ORDER BY") || !h.env.State.View.IsSQL() {
		t.Errorf("s on a SQL result: %+v", h.notes)
	}
}

func TestHideAndPickColumns(t *testing.T) {
	ds := openFixture(t, "demo")
	h := newHarness(t, ds, 150, 42)
	g := h.g
	n := len(g.cols)
	h.press("-")
	if len(g.cols) != n-1 || g.byName["diaSourceId"] != 0 && h.env.State.Hidden["diaSourceId"] != true {
		t.Fatalf("- left %d columns, hidden %v", len(g.cols), h.env.State.Hidden)
	}
	if _, ok := g.byName["diaSourceId"]; ok {
		t.Error("diaSourceId still shown")
	}
	if !h.noted("Hid diaSourceId · c brings it back") || h.env.State.Current != "ssObjectId" {
		t.Errorf("notes %+v, current %q", h.notes, h.env.State.Current)
	}
	h.press("c")
	if len(h.dialogs) == 0 || h.dialogs[len(h.dialogs)-1] != "columns ssObjectId" {
		t.Fatalf("c opened %v", h.dialogs)
	}
	h.send(kit.ColumnsPickedMsg{Visible: names(ds.Columns())})
	h.settle()
	if len(g.cols) != n || len(h.env.State.Hidden) != 0 {
		t.Errorf("picking all: %d columns, hidden %v", len(g.cols), h.env.State.Hidden)
	}
	if g.curName() != "ssObjectId" {
		t.Errorf("cursor on %q after picking", g.curName())
	}
	// never the last column
	h.send(kit.ColumnsPickedMsg{Visible: []string{"ra"}})
	h.settle()
	h.press("-")
	if len(g.cols) != 1 || g.curName() != "ra" {
		t.Errorf("hid the last column: %d", len(g.cols))
	}
	// without the dialogs (until WP10), a notice
	h2 := newHarness(t, newFake(10, 3), 80, 20, hopts{noDlg: true})
	h2.press("c")
	if !h2.noted("not built yet") {
		t.Errorf("c without dialogs: %+v", h2.notes)
	}
}

func TestHiddenColumnHints(t *testing.T) {
	ds := newFake(2000, 200)
	h := newHarness(t, ds, 150, 42)
	g := h.g
	first, last, hl, hr := g.colWindow()
	sub := g.Subtitle().Plain
	if first != 0 || hl != 0 || hr != 200-(last+1) {
		t.Fatalf("window %d %d %d %d", first, last, hl, hr)
	}
	if want := "columns 1–" + itoa(int64(last+1)) + " of 200  ·  " + itoa(int64(hr)) + " ›"; sub != want {
		t.Errorf("subtitle %q, want %q", sub, want)
	}
	hdr := h.grid()[0]
	if !strings.HasSuffix(hdr, "›") || strings.HasPrefix(hdr, "‹") {
		t.Errorf("markers: %q", hdr)
	}
	h.press("end")
	first, last, hl, hr = g.colWindow()
	if last != 199 || hr != 0 || hl != first {
		t.Errorf("after End: %d %d %d %d", first, last, hl, hr)
	}
	if sub := g.Subtitle().Plain; !strings.HasPrefix(sub, "‹ "+itoa(int64(hl))+"  ·  columns") || strings.Contains(sub, "›") {
		t.Errorf("subtitle after End %q", sub)
	}
	if hdr := h.grid()[0]; !strings.HasPrefix(hdr, "‹") || strings.HasSuffix(hdr, "›") {
		t.Errorf("markers after End: %q", hdr)
	}
	// a click on a marker pages that way (the pane starts at x=2, y=5)
	h.send(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: 8})
	h.settle()
	if _, _, _, hr := g.colWindow(); hr == 0 {
		t.Error("a click on ‹ didn't page left")
	}
	before := g.left
	h.send(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2 + g.w - 1, Y: 8})
	h.settle()
	if g.left <= before {
		t.Errorf("a click on › didn't page right: left %d (was %d)", g.left, before)
	}
	// pinned columns show in the readout
	h.press("home", "right", "p")
	if sub := g.Subtitle().Plain; !strings.Contains(sub, "· 2 pinned") {
		t.Errorf("subtitle with pins %q", sub)
	}

	// everything fits: no hints at all
	h2 := newHarness(t, newFake(100, 3), 150, 42)
	if sub := h2.g.Subtitle().Plain; sub != "" {
		t.Errorf("narrow subtitle %q", sub)
	}
	if hdr := h2.grid()[0]; strings.Contains(hdr, "›") || strings.Contains(hdr, "‹") {
		t.Errorf("narrow markers %q", hdr)
	}
}

func TestQuitKeyIsTextInFilter(t *testing.T) {
	h := newHarness(t, newFake(100, 3), 80, 20)
	h.press("/")
	h.typeText("qemx1?")
	if h.quits != 0 || h.f.Value() != "qemx1?" {
		t.Errorf("quits %d, filter %q", h.quits, h.f.Value())
	}
}

func TestCtrlXInTheFilterBar(t *testing.T) {
	h := newHarness(t, newFake(100, 3), 80, 20)
	h.filterWith("id % 2 = 0")
	if h.env.State.View.Plain() {
		t.Fatal("filter not applied")
	}
	h.press("/")
	h.press("ctrl+x")
	if !h.env.State.View.Plain() || h.f.Value() != "" || h.env.State.Total != 100 {
		t.Errorf("ctrl+x: view %+v, box %q, total %d", h.env.State.View, h.f.Value(), h.env.State.Total)
	}
	// typed but never applied: ctrl+x just empties the box
	h.press("/")
	h.typeText("id <")
	h.press("ctrl+x")
	if h.f.Value() != "" || !h.env.State.View.Plain() {
		t.Errorf("typed ctrl+x: box %q", h.f.Value())
	}
}

func TestColumnFormats(t *testing.T) {
	ds := openFixture(t, "demo")
	h := newHarness(t, ds, 150, 42)
	g := h.g
	h.env.State.Current = "ra"
	h.send(kit.ColumnChangedMsg{From: "schema"})
	h.settle()
	ra := g.cols[g.curCol]
	h.press("<")
	want := fmtx.StepOverride(fmtx.Override{}, ra.kind, -1)
	if !want.Set {
		if !h.noted("ra has no digits to change · F sets a format spec") {
			t.Errorf("< without digits: %+v", h.notes)
		}
	} else if h.env.State.Formats["ra"] != want {
		t.Errorf("< : %+v, want %+v", h.env.State.Formats["ra"], want)
	}
	// F asks with a sample value of the column
	h.press("F")
	v, _ := g.v.cell("ra", g.curRow)
	if d := h.dialogs[len(h.dialogs)-1]; d != "format ra "+fmtx.Format(v, fmtx.KindStr, fmtx.Opts{}) {
		t.Errorf("F opened %q", d)
	}
	o := fmtx.Override{Spec: ".2e", Set: true}
	h.notes = nil
	h.send(kit.FormatSetMsg{Column: "ra", Override: o})
	h.settle()
	if h.env.State.Formats["ra"] != o {
		t.Errorf("format %+v", h.env.State.Formats["ra"])
	}
	// saved, or said why not (formats.yaml is WP3's)
	if !h.noted("✓ ra: ") && !h.noted("for this session only — not saved") {
		t.Errorf("notes %+v", h.notes)
	}
	if a, b := g.header(ra); a != "ra" || !strings.Contains(b, fmtx.DescribeOverride(o, ra.kind)) {
		t.Errorf("header %q %q", a, b)
	}
	// back to automatic
	h.send(kit.FormatSetMsg{Column: "ra"})
	h.settle()
	if _, ok := h.env.State.Formats["ra"]; ok {
		t.Error("the format stays after a reset")
	}

	// a --format of this session is used, never saved
	h2 := newHarness(t, ds, 150, 42, hopts{session: map[string]fmtx.Override{"dec": {Digits: 2, Set: true}}})
	h2.send(kit.FormatSetMsg{Column: "dec", Override: fmtx.Override{Digits: 3, Set: true}})
	h2.settle()
	if !h2.noted("✓ dec: ") || !h2.noted("(this session)") {
		t.Errorf("session format: %+v", h2.notes)
	}
	// on a string column nothing to step
	h2.env.State.Current = "band"
	h2.send(kit.ColumnChangedMsg{From: "schema"})
	h2.settle()
	h2.press(">")
	if _, ok := h2.env.State.Formats["band"]; ok {
		t.Error("> on a string column set a format")
	}
}

func TestCopyCell(t *testing.T) {
	ds := openFixture(t, "hostile")
	h := newHarness(t, ds, 150, 42)
	g := h.g
	for i, c := range g.cols {
		if c.kind != fmtx.KindStr {
			continue
		}
		for r := int64(0); r < 20; r++ {
			v, ok := g.v.cell(c.Name, r)
			s, isStr := v.(string)
			if !ok || !isStr || !strings.Contains(s, "\x1b") {
				continue
			}
			g.curCol, g.curRow = i, r
			h.copies, h.notes = nil, nil
			h.press("y")
			if len(h.copies) != 1 || h.copies[0] != s {
				t.Fatalf("y copied %q, want the raw %q", h.copies, s)
			}
			if !h.noted("✓ Copied") || !h.noted("copied as visible symbols") {
				t.Errorf("notes %+v", h.notes)
			}
			for _, n := range h.notes {
				if strings.Contains(n.Text, "\x1b") {
					t.Errorf("a notice holds ESC: %q", n.Text)
				}
			}
			return
		}
	}
	t.Fatal("no string with ESC in the hostile fixture's first rows")
}

func TestOddFileOpens(t *testing.T) {
	ds := openFixture(t, "odd")
	h := newHarness(t, ds, 150, 42)
	h.press("ctrl+end")
	if h.g.curRow != 999 {
		t.Errorf("Ctrl+End: %d", h.g.curRow)
	}
	h.press("end", "home", "f", "pgup")
	if h.noted("failed") || h.noted("Couldn't") {
		t.Errorf("notes %+v", h.notes)
	}
	// a filtered view of a file with its own file_row_number has no row
	// numbers: every column is read with the rows
	h.filterWith("x > 0")
	if h.g.v.ids {
		t.Error("the view is taken to have file rows")
	}
}

// The grid follows the current column set elsewhere; a hidden one leaves
// it put with a hint until the cursor moves (test_linked_columns_*).
func TestLinkedColumns(t *testing.T) {
	ds := openFixture(t, "demo")
	h := newHarness(t, ds, 150, 42)
	g, st := h.g, h.env.State
	h.press("down", "down", "down", "down", "down", "right")
	if st.Current != "ssObjectId" {
		t.Errorf("current %q", st.Current)
	}
	st.Current = "mag"
	h.send(kit.ColumnChangedMsg{From: "schema"})
	h.settle()
	if g.curName() != "mag" || g.curRow != 5 {
		t.Errorf("following mag: on %q row %d", g.curName(), g.curRow)
	}
	// hide mag: the cursor lands on the next column, which becomes current
	h.press("-")
	if st.Current != "snr" || g.curName() != "snr" || g.curRow != 5 {
		t.Errorf("after hiding mag: current %q, cursor %q row %d", st.Current, g.curName(), g.curRow)
	}
	st.Current = "mag"
	h.send(kit.ColumnChangedMsg{From: "schema"})
	h.settle()
	if g.curName() != "snr" || h.status.Text != "mag is hidden · c to show" || h.status.Severity != kit.Warning {
		t.Errorf("hidden current: cursor %q, status %+v", g.curName(), h.status)
	}
	h.press("right") // moving clears the hint
	if h.status.Text != "" || st.Current != g.curName() {
		t.Errorf("after moving: status %+v, current %q", h.status, st.Current)
	}
	st.Current = "mag"
	h.send(kit.ColumnChangedMsg{From: "schema"})
	h.settle()
	h.send(kit.ColumnsPickedMsg{Visible: names(ds.Columns())}) // bringing it back lands on it
	h.settle()
	if g.curName() != "mag" || g.curRow != 5 {
		t.Errorf("picked back: on %q row %d", g.curName(), g.curRow)
	}
	// i: Stats on the column
	h.press("i")
	if st.Current != "mag" {
		t.Errorf("i: current %q", st.Current)
	}
	// views keep the current column; a SQL result without it leaves it
	h.filterWith("mag > 18")
	if g.curName() != "mag" || st.Current != "mag" {
		t.Errorf("filter: on %q, current %q", g.curName(), st.Current)
	}
	h.filterWith("select ra, dec from t")
	if st.Current != "mag" {
		t.Errorf("SQL view changed the current column to %q", st.Current)
	}
	h.press("ctrl+x")
	if !st.View.Plain() || g.curName() != "mag" {
		t.Errorf("cleared: view %+v, on %q", st.View, g.curName())
	}
}

// d, Enter and a click on the cursor's cell toggle the details pane.
func TestDetailToggle(t *testing.T) {
	h := newHarness(t, newFake(100, 3), 120, 30)
	h.press("d")
	if !h.env.State.DetailOpen {
		t.Fatal("d didn't open the details pane")
	}
	if h.g.w != 120-detailW-sideCells {
		t.Errorf("grid width with the pane: %d", h.g.w)
	}
	h.press("enter")
	if h.env.State.DetailOpen {
		t.Error("enter didn't close it")
	}
	if h.g.w != 120-sideCells {
		t.Errorf("grid width without the pane: %d", h.g.w)
	}
}
