package grid

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/ui/app"
	"github.com/mjuric/pqx/go/internal/ui/detail"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// The grid with the details pane (internal/ui/detail): the pane's reads,
// focus and keys, as tests/test_app.py, test_lazycols.py and
// test_detail_keys.py check them in Python pqx.

// withDetail is the harness with the details pane wired in, as
// cmd/pqx does.
func withDetail(t *testing.T, ds data.Dataset, w, h int, o ...hopts) (*harness, *detail.Pane) {
	t.Helper()
	hs := newHarness(t, ds, w, h, o...)
	hs.env.Grid = hs.g
	p := detail.New(hs.env)
	hs.app = app.New(hs.env, app.Parts{Grid: hs.g, Filter: hs.f, Detail: p})
	hs.send(tea.WindowSizeMsg{Width: w, Height: h})
	hs.settle()
	return hs, p
}

func paneValue(t *testing.T, p *detail.Pane, name string) string {
	t.Helper()
	v, ok := p.Value(name)
	if !ok {
		t.Fatalf("no entry for %s", name)
	}
	return v
}

func colCalls(ds *fakeDS) []call {
	var out []call
	for _, c := range ds.log() {
		if c.kind == "columns" {
			out = append(out, c)
		}
	}
	return out
}

func fmtTruth(name string, fr int64) string {
	return fmt.Sprint(truth(name, fr))
}

// Port of test_lazycols.py::test_detail_pane_shows_columns_the_grid_has_not_loaded.
func TestDetailReadsWhatTheGridLacks(t *testing.T) {
	ds := newFake(3000, 120)
	h, p := withDetail(t, ds, 160, 48)
	g := h.g
	far := g.cols[len(g.cols)-1].Name
	if _, ok := g.v.cell(far, 0); ok {
		t.Fatal("the far column is loaded already")
	}
	ds.clearLog()
	h.press("d")
	if calls := colCalls(ds); len(calls) != 1 || len(ds.fetches()) != 0 {
		t.Fatalf("reads %+v", ds.log())
	}
	if rec := g.Record(); len(rec.Missing) != 0 {
		t.Fatalf("missing %v", rec.Missing)
	}
	if v := paneValue(t, p, far); v != fmtTruth(far, 0) {
		t.Fatalf("%s: %q", far, v)
	}
	// merged for every row read near the screen: the grid has them too
	a, b := g.rowRange(g.top-int64(g.bodyH()), g.top+2*int64(g.bodyH()))
	for r := a; r < b; r++ {
		if v, ok := g.v.cell(far, r); g.v.loaded(r) && (!ok || v != truth(far, r)) {
			t.Fatalf("row %d: %v", r, v)
		}
	}
	ds.clearLog()
	for i := 0; i < 5; i++ {
		h.press("down")
	}
	if len(ds.log()) != 0 {
		t.Fatalf("reads %+v", ds.log())
	}
	if v := paneValue(t, p, far); v != fmtTruth(far, 5) || p.Title().Plain != "row 5 · file row 5" {
		t.Fatalf("%q %q", v, p.Title().Plain)
	}
}

// Port of test_lazycols.py::test_detail_pane_placeholder_while_loading.
func TestDetailPlaceholderWhileLoading(t *testing.T) {
	ds := newFake(3000, 120)
	h, p := withDetail(t, ds, 160, 48)
	far := h.g.cols[len(h.g.cols)-1].Name
	ds.colsGate = make(chan struct{})
	h.press("d")
	if !h.env.Tasks.Running("detail") || paneValue(t, p, far) != "…" || paneValue(t, p, "id") != "0" {
		t.Fatalf("%q %q", paneValue(t, p, far), paneValue(t, p, "id"))
	}
	close(ds.colsGate)
	h.settle()
	if v := paneValue(t, p, far); v != fmtTruth(far, 0) {
		t.Fatal(v)
	}
}

// Port of test_lazycols.py::test_esc_closes_the_pane_while_its_columns_load:
// pqx's own loading doesn't make Esc a cancel.
func TestEscClosesThePaneWhileItsColumnsLoad(t *testing.T) {
	ds := newFake(3000, 120)
	h, _ := withDetail(t, ds, 160, 48)
	ds.colsGate = make(chan struct{})
	for _, keys := range [][]string{{"d"}, {"d", "tab"}} {
		h.press(keys...)
		if !h.env.Tasks.Running("detail") {
			t.Fatal("not reading")
		}
		h.press("esc")
		if h.env.State.DetailOpen || !h.g.focused || !h.env.Tasks.Running("detail") {
			t.Fatalf("%v: open %v, grid focused %v", keys, h.env.State.DetailOpen, h.g.focused)
		}
	}
	close(ds.colsGate)
	h.settle()
	if h.env.Tasks.Busy() {
		t.Fatal("still busy")
	}
}

// Port of test_lazycols.py::test_detail_and_grid_fetches_do_not_overlap.
func TestDetailAndGridReadsDoNotOverlap(t *testing.T) {
	ds := newFake(3000, 120)
	h, _ := withDetail(t, ds, 160, 48)
	ds.clearLog()
	ds.colsGate = make(chan struct{})
	h.press("d")
	if n := len(colCalls(ds)); n != 1 {
		t.Fatalf("%d reads", n)
	}
	h.press("end") // the far columns: the pane's read is bringing them
	h.press("left", "left")
	close(ds.colsGate)
	h.settle()
	seen := map[string]bool{}
	for _, c := range colCalls(ds) {
		for _, name := range c.cols {
			for _, r := range c.rows {
				k := fmt.Sprint(name, r)
				if seen[k] {
					t.Fatalf("%s read twice: %+v", k, colCalls(ds))
				}
				seen[k] = true
			}
		}
	}
	for _, c := range h.g.cols {
		if v, ok := h.g.v.cell(c.Name, h.g.curRow); !ok || v != truth(c.Name, h.g.curRow) {
			t.Fatalf("%s: %v", c.Name, v)
		}
	}
}

// Port of test_lazycols.py::test_columns_that_fail_show_so_and_are_not_retried.
func TestDetailFailedColumnsAreNotRetried(t *testing.T) {
	ds := newFake(3000, 120)
	h, p := withDetail(t, ds, 160, 48)
	calls := 0
	ds.colsHook = func(ctx context.Context, cols []string) error {
		calls++
		return errors.New("boom")
	}
	h.press("d")
	far := h.g.cols[len(h.g.cols)-1].Name
	if calls != 1 || !strings.HasPrefix(paneValue(t, p, far), "✗") || !h.noted("✗ Columns") {
		t.Fatalf("%d reads, %q", calls, paneValue(t, p, far))
	}
	for i := 0; i < 4; i++ {
		h.press("down")
	}
	h.press("end")
	if calls != 1 {
		t.Fatalf("%d reads", calls)
	}
	if v, _ := h.g.v.cell(far, 0); !isFailed(v) {
		t.Fatalf("grid cell %v", v)
	}
	if !strings.Contains(strings.Join(h.grid(), "\n"), "✗") {
		t.Fatal("no ✗ in the grid")
	}
	h.press("y")
	if len(h.copies) != 0 || !h.noted("couldn't be loaded") {
		t.Fatalf("%v", h.copies)
	}
}

// Port of test_app.py::test_detail_pane_focus_and_link.
func TestDetailFocusAndLink(t *testing.T) {
	ds := newFake(1000, 30)
	h, p := withDetail(t, ds, 150, 24) // short: the pane scrolls
	g, st := h.g, h.env.State
	h.press("down", "down", "down", "down", "down", "down", "down", "right", "right")
	h.press("d")
	if len(p.Names()) != len(g.cols) || p.Selected() != g.cols[2].Name || !strings.Contains(keysOf(g), "into detail") {
		t.Fatalf("%d entries, %q", len(p.Names()), p.Selected())
	}
	h.press("tab") // into the pane, selection on the grid's column
	if !p.Focused() || g.focused || p.Selected() != g.cols[2].Name || !strings.Contains(keysOf(p), "esc close") {
		t.Fatal("tab")
	}
	h.press("down", "down") // the grid follows sideways, same row
	if p.Selected() != g.cols[4].Name || g.curCol != 4 || g.curRow != 7 || st.Current != g.cols[4].Name {
		t.Fatalf("%q col %d row %d", p.Selected(), g.curCol, g.curRow)
	}
	h.press("end")
	if g.curCol != len(g.cols)-1 || g.curRow != 7 {
		t.Fatal(g.curCol)
	}
	h.press("home", "down", "enter") // back to the grid, on the selected column
	if !g.focused || p.Focused() || g.curCol != 1 || g.curRow != 7 {
		t.Fatalf("focused %v col %d", g.focused, g.curCol)
	}
	for _, key := range []string{"enter", "tab"} {
		h.press("tab", "down", key)
		if !g.focused || g.curCol != 2 || g.curRow != 7 {
			t.Fatalf("%s: col %d", key, g.curCol)
		}
		h.press("left")
	}
	// moving the grid moves the selection, without the pane taking over
	for i := 0; i < 4; i++ {
		h.press("right")
	}
	if p.Selected() != g.cols[5].Name || !g.focused {
		t.Fatal(p.Selected())
	}
	h.press("down") // a new row: same selection, new values
	if p.Selected() != g.cols[5].Name || g.curRow != 8 || paneValue(t, p, "id") != "8" {
		t.Fatal(p.Selected())
	}
	// the wheel only scrolls the pane
	x, y := h.app0("id")
	for i := 0; i < 5; i++ {
		h.send(tea.MouseWheelMsg{X: x, Y: y + 1, Button: tea.MouseWheelDown})
	}
	h.settle()
	if p.Top() == 0 || p.Selected() != g.cols[5].Name || g.curCol != 5 || !g.focused {
		t.Fatalf("top %d %q", p.Top(), p.Selected())
	}
	top := p.Top()
	h.press("down") // a new row keeps the pane where it was scrolled to
	if p.Top() != top || g.curRow != 9 {
		t.Fatal(p.Top())
	}
	// a click on an entry focuses the pane and moves the grid there
	for i := 0; i < 5; i++ {
		h.send(tea.MouseWheelMsg{X: x, Y: y + 1, Button: tea.MouseWheelUp})
	}
	x, y = h.app0(g.cols[3].Name)
	h.send(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	h.settle()
	if !p.Focused() || p.Selected() != g.cols[3].Name || g.curCol != 3 || g.curRow != 9 {
		t.Fatalf("%v %q %d", p.Focused(), p.Selected(), g.curCol)
	}
	h.press("d") // closing the pane hands focus back to the grid
	if st.DetailOpen || !g.focused || strings.Contains(keysOf(g), "into detail") {
		t.Fatal("d")
	}
	// Esc closes the pane, from the grid or from inside it
	h.press("d", "esc")
	if st.DetailOpen || !g.focused {
		t.Fatal("esc from the grid")
	}
	h.press("d", "tab", "down", "esc")
	if st.DetailOpen || !g.focused || g.curCol != 4 || g.curRow != 9 {
		t.Fatalf("esc from the pane: col %d", g.curCol)
	}
}

// app0 is where the pane's entry for name is on the screen.
func (h *harness) app0(name string) (int, int) {
	h.t.Helper()
	for y, l := range strings.Split(h.screen(), "\n") {
		if i := strings.LastIndex(l, "│ "+name+" "); i > len(l)/2 {
			return len([]rune(l[:i])) + 2, y
		}
	}
	h.t.Fatalf("%s not on screen:\n%s", name, h.screen())
	return 0, 0
}

func keysOf(p kit.Pane) string {
	var b strings.Builder
	for _, k := range p.Keys() {
		b.WriteString(k.Key + " " + k.Help + ", ")
	}
	return b.String()
}

// Port of test_app.py::test_detail_pane_rows_and_views.
func TestDetailRowsAndViews(t *testing.T) {
	ds := newFake(3000, 12)
	h, p := withDetail(t, ds, 160, 48)
	g := h.g
	h.press("right", "right", "right", "d", "ctrl+end") // a new window of rows: the pane follows
	if g.curRow != 2999 || p.Title().Plain != "row 2,999 · file row 2,999" || p.Selected() != g.cols[3].Name {
		t.Fatalf("%q %q", p.Title().Plain, p.Selected())
	}
	h.press("tab", "down", "enter")
	if g.curRow != 2999 || g.curCol != 4 {
		t.Fatal(g.curCol)
	}
	h.filterWith("select id, name from t") // other columns
	if strings.Join(p.Names(), " ") != "id name" || p.Title().Plain != "row 0" {
		t.Fatalf("%v %q", p.Names(), p.Title().Plain)
	}
	h.send(kit.FocusMsg{Pane: "grid"})
	h.press("tab", "down")
	if !p.Focused() || g.cols[g.curCol].Name != p.Selected() || p.Selected() != "name" {
		t.Fatal(p.Selected())
	}
	h.press("enter")
	h.filterWith("none") // no rows: no stale entries to wander into
	if len(p.Names()) != 0 || p.Title().Plain != "no rows" {
		t.Fatalf("%v %q view %+v total %d/%d limit %d", p.Names(), p.Title().Plain, h.env.State.View, h.env.State.Total, g.v.total, g.v.limit())
	}
	cur := h.env.State.Current
	h.send(kit.FocusMsg{Pane: "grid"})
	h.press("tab", "down")
	if h.env.State.Current != cur {
		t.Fatal(h.env.State.Current)
	}
}

// Port of test_detail_keys.py::test_copy_stats_format_from_pane, and x.
func TestDetailCellKeys(t *testing.T) {
	ds := newFake(3000, 12)
	h, p := withDetail(t, ds, 160, 48)
	h.press("down", "down", "down", "right", "right", "right", "d", "tab")
	name := h.g.cols[3].Name
	if p.Selected() != name {
		t.Fatal(p.Selected())
	}
	h.press("y")
	if len(h.copies) != 1 || h.copies[0] != fmtTruth(name, 3) || !p.Focused() {
		t.Fatalf("%v", h.copies)
	}
	h.press("down", "down", "<") // f005, a float column
	name = h.g.cols[5].Name
	note := h.notes[len(h.notes)-1].Text
	if h.g.curCol != 5 || !strings.HasPrefix(note, "✓ "+name+":") || !strings.HasSuffix(note, "(grid)") || !p.Focused() {
		t.Fatalf("%q", note)
	}
	h.press("F")
	if len(h.dialogs) != 1 || !strings.HasPrefix(h.dialogs[0], "format "+name) || !p.Focused() {
		t.Fatalf("%v", h.dialogs)
	}
	h.send(kit.CloseDialogMsg{})
	h.press("down", "i")
	if h.env.State.Tab != kit.TabStats || h.env.State.Current != h.g.cols[6].Name {
		t.Fatalf("%v %q", h.env.State.Tab, h.env.State.Current)
	}
	h.press("1")
	h.filterWith("id % 2 = 0")
	h.send(kit.FocusMsg{Pane: "detail"})
	if !p.Focused() {
		t.Fatal("not in the pane")
	}
	h.press("x") // the app's clear-filter key: the whole file, focus on the grid
	if !h.env.State.View.Plain() || !h.g.focused {
		t.Fatalf("%+v", h.env.State.View)
	}
}

// Port of test_detail_keys.py::test_equals_from_pane_narrows_in_two_keystrokes
// (the record kept in the filtered view is WP11's).
func TestDetailEquals(t *testing.T) {
	ds := openFixture(t, "demo")
	h, p := withDetail(t, ds, 160, 48)
	h.send(kit.GotoMsg{Row: 15_000})
	h.settle()
	for h.g.curName() != "band" {
		h.press("right")
	}
	h.press("d", "tab")
	band, _ := h.g.v.cell("band", 15_000)
	h.press("=")
	st := h.env.State
	if want := fmt.Sprintf("band = '%s'", band); st.View.Where != want {
		t.Fatalf("%q, want %q", st.View.Where, want)
	}
	if !p.Focused() || p.Selected() != "band" {
		t.Fatal("focus")
	}
	for p.Selected() != "detector" {
		h.press("down")
	}
	// (the record under the cursor: keeping 15,000 in the new view is WP11's)
	det := h.g.Record().Values["detector"]
	h.press("=")
	if want := fmt.Sprintf("band = '%s' and detector = %d", band, det); st.View.Where != want {
		t.Fatalf("%q, want %q", st.View.Where, want)
	}
	if !p.Focused() || p.Selected() != "detector" || !strings.Contains(keysOf(p), "= match") {
		t.Fatal("focus")
	}
	h.press("esc") // back to the grid, on that field, closing the pane
	if !h.g.focused || h.g.curName() != "detector" || st.DetailOpen {
		t.Fatal("esc")
	}
}

// Port of test_detail_keys.py::test_equals_from_pane_on_unloaded_value: the
// pane's own read hangs; the action's goes through.
func TestDetailEqualsOnUnloadedValue(t *testing.T) {
	ds := newFake(3000, 120)
	h, p := withDetail(t, ds, 160, 48)
	release := make(chan struct{})
	first := true
	ds.colsHook = func(ctx context.Context, cols []string) error {
		if first {
			first = false
			return ds.hold(ctx, release)
		}
		return nil
	}
	h.press("down", "down", "down", "down", "down", "d", "tab", "end")
	name := p.Selected()
	if name != h.g.cols[len(h.g.cols)-1].Name || paneValue(t, p, name) != "…" {
		t.Fatalf("%q %q", name, paneValue(t, p, name))
	}
	h.press("=")
	if want := fmt.Sprintf("%s = %v", name, truth(name, 5)); h.env.State.View.Where != want {
		t.Fatalf("%q, want %q", h.env.State.View.Where, want)
	}
	close(release)
	h.settle()
	if !p.Focused() || p.Selected() != name {
		t.Fatal("focus")
	}
}

// A window for another view generation, or for rows no longer cached,
// changes nothing.
func TestMergeDropsStaleWindows(t *testing.T) {
	ds := newFake(3000, 120)
	h, _ := withDetail(t, ds, 160, 48)
	g := h.g
	far := g.cols[len(g.cols)-1].Name
	w := data.Window{Len: 2, FileRows: []int64{0, 100_000}, Cols: map[string][]data.Value{far: {int64(-1), int64(-2)}}}
	g.Merge(kit.View{Gen: g.v.gen - 1}, w)
	if _, ok := g.v.cell(far, 0); ok {
		t.Fatal("stale window merged")
	}
	g.Merge(kit.View{View: g.v.view, Gen: g.v.gen}, w)
	if v, _ := g.v.cell(far, 0); v != int64(-1) || g.v.loaded(100_000) {
		t.Fatalf("%v", v)
	}
	w.Cols[far] = []data.Value{int64(-3), nil}
	g.Merge(kit.View{View: g.v.view, Gen: g.v.gen}, w)
	if v, _ := g.v.cell(far, 0); v != int64(-1) {
		t.Fatal("replaced")
	}
}

// Port of the pane part of test_security.py::test_widgets_show_no_control_characters,
// byte by byte on the whole screen: the hostile fixture's names, values,
// units and descriptions, every row, every entry selected, focused or not.
func TestDetailShowsHostileTextSafely(t *testing.T) {
	ds := openFixture(t, "hostile")
	h, p := withDetail(t, ds, 160, 48)
	sgr := regexp.MustCompile(`\x1b\[[0-9;:]*m`)
	check := func(where string) {
		t.Helper()
		for _, r := range sgr.ReplaceAllString(h.raw(), "") {
			if r != '\n' && (r < 0x20 || (r >= 0x7F && r < 0xA0) || (r >= 0x202A && r <= 0x202E) ||
				(r >= 0x2066 && r <= 0x2069) || (r >= 0x200B && r <= 0x200F)) {
				t.Fatalf("%U on screen (%s)", r, where)
			}
		}
	}
	h.press("d")
	for row := 0; row < 20; row++ {
		check(fmt.Sprint("row ", row))
		h.press("down")
	}
	h.press("ctrl+home", "tab")
	for range p.Names() {
		check("entry " + p.Selected())
		h.press("down")
	}
}
