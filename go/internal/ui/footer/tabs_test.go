package footer_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/ui/app"
	"github.com/mjuric/pqx/go/internal/ui/footer"
	ft "github.com/mjuric/pqx/go/internal/ui/footer/footertest"
	"github.com/mjuric/pqx/go/internal/ui/kit"
	"github.com/mjuric/pqx/go/internal/ui/meta"
	"github.com/mjuric/pqx/go/internal/ui/schema"
)

// stub is a part not under test: it draws its name.
type stub struct{ name string }

func (s stub) Update(tea.Msg) tea.Cmd { return nil }
func (s stub) Keys() []kit.KeyHint    { return nil }
func (s stub) View(w, h int) string {
	l := make([]string, h)
	l[0] = strings.ToUpper(s.name)
	return strings.Join(l, "\n")
}

type tabs struct {
	*ft.App
	ds     *ft.DS
	env    *kit.Env
	schema *schema.Pane
	meta   *meta.Pane
	reader *footer.Reader
}

// open runs the root with Schema and Metadata over fixture name on a
// 200 × 50 screen. hold keeps the footer pass waiting until release.
func open(t *testing.T, name string, hold bool, edit ...func(*ft.DS)) *tabs {
	t.Helper()
	ds := ft.Open(t, name)
	if hold {
		ds.Gate = make(chan struct{})
	}
	for _, f := range edit {
		f(ds)
	}
	env := ft.Env(ds)
	r := footer.New(env)
	r.Wait = 20 * time.Millisecond
	tb := &tabs{ds: ds, env: env, reader: r, schema: schema.New(env, r), meta: meta.New(env)}
	tb.App = ft.NewApp(t, env, app.Parts{Grid: stub{"grid"}, Filter: stub{"filter"}, Stats: stub{"stats"},
		Plot: stub{"plot"}, Schema: tb.schema, Meta: tb.meta}, 200, 50)
	return tb
}

func (tb *tabs) release() { close(tb.ds.Gate) }

func (tb *tabs) text() string { return strings.Join(tb.Screen(), "\n") }

// read waits for the footer pass's result to be shown.
func (tb *tabs) read() {
	tb.Until(func() bool { return tb.schema.Built() })
	tb.Settle()
}

// gridMoves is the grid moving the current column.
func (tb *tabs) gridMoves(col string) {
	tb.env.State.Current = col
	tb.Send(kit.ColumnChangedMsg{From: "grid"})
}

func names(ds data.Dataset) []string {
	var n []string
	for _, c := range ds.Columns() {
		n = append(n, c.Name)
	}
	return n
}

// line is the screen line holding s.
func (tb *tabs) line(s string) string {
	for _, l := range tb.Screen() {
		if strings.Contains(l, s) {
			return l
		}
	}
	return ""
}

// test_startup.py::test_grid_usable_before_tabs_built
func TestTabsBuiltLate(t *testing.T) {
	tb := open(t, "demo", true)
	tb.gridMoves("ssObjectId")
	tb.Until(func() bool { return tb.ds.Calls.Load() == 1 }) // started (after the wait), held
	tb.Press("2")
	if s := tb.text(); !strings.Contains(s, "Reading sizes and statistics from the footer …   8 row groups × 16 columns") {
		t.Fatalf("schema while reading:\n%s", s)
	}
	if tb.schema.Built() {
		t.Fatal("built before the footer was read")
	}
	tb.Press("5")
	if s := tb.text(); !strings.Contains(s, "Reading the footer …") || !strings.Contains(s, "row groups  8") {
		t.Fatalf("meta while reading:\n%s", s)
	}
	if tb.meta.RowGroups() != 0 {
		t.Fatal("row groups before the footer was read")
	}
	tb.release()
	tb.read()
	if tb.schema.Rows() != len(tb.ds.Columns()) || tb.meta.RowGroups() != 8 {
		t.Fatalf("schema rows %d, row groups %d", tb.schema.Rows(), tb.meta.RowGroups())
	}
	if !strings.Contains(tb.text(), "✓ Footer read") {
		t.Fatalf("meta:\n%s", tb.text())
	}
	if tb.ds.Calls.Load() != 1 || tb.env.State.Current != "ssObjectId" {
		t.Fatalf("calls %d, current %q", tb.ds.Calls.Load(), tb.env.State.Current)
	}
}

// test_startup.py::test_schema_built_late_follows_current_column
func TestSchemaBuiltLateFollowsCurrentColumn(t *testing.T) {
	tb := open(t, "demo", true)
	n := names(tb.ds)
	tb.gridMoves("mag")
	tb.Press("2")
	tb.release()
	tb.read()
	if tb.schema.Cursor() != slices.Index(n, "mag") || tb.env.State.Current != "mag" {
		t.Fatalf("cursor %d current %q", tb.schema.Cursor(), tb.env.State.Current)
	}
	if !strings.HasPrefix(tb.schema.Description(), "mag") {
		t.Fatalf("description %q", tb.schema.Description())
	}
	tb.Press("up", "up", "up") // linked as usual from then on: mag -> dec
	tb.Press("down", "down")
	if tb.env.State.Current != n[slices.Index(n, "mag")-1] {
		t.Fatalf("current %q", tb.env.State.Current)
	}
	tb.Press("enter")
	tb.Settle()
	if !strings.Contains(tb.text(), "STATS") || tb.env.State.Current != n[slices.Index(n, "mag")-1] {
		t.Fatalf("enter: current %q\n%s", tb.env.State.Current, tb.text())
	}
}

// test_startup.py::test_schema_built_late_on_first_column_and_sql_result
func TestSchemaBuiltLateOnSQLResultColumn(t *testing.T) {
	tb := open(t, "demo", true)
	tb.gridMoves("m2") // a SQL result's own column
	tb.Press("2")
	tb.release()
	tb.read()
	if tb.schema.Cursor() != 0 || tb.env.State.Current != "m2" {
		t.Fatalf("cursor %d current %q", tb.schema.Cursor(), tb.env.State.Current)
	}
	if !strings.HasPrefix(tb.schema.Description(), names(tb.ds)[0]) {
		t.Fatalf("description %q", tb.schema.Description())
	}
}

// test_startup.py::test_footer_failure_is_shown
func TestFooterFailureIsShown(t *testing.T) {
	tb := open(t, "demo", false, func(d *ft.DS) { d.Err = errors.New("bad statistics") })
	tb.Until(func() bool { return strings.Contains(tb.meta.Status(), "bad statistics") })
	if s := tb.meta.Status(); !strings.HasPrefix(s, "✗ Couldn't read the footer's statistics") {
		t.Fatalf("meta status %q", s)
	}
	tb.Press("2")
	if !strings.Contains(tb.text(), "✗ Couldn't read the footer's statistics   bad statistics") {
		t.Fatalf("schema:\n%s", tb.text())
	}
}

// test_startup.py::test_rowgroup_table_filled_in_batches
func TestRowGroupTableFilledInBatches(t *testing.T) {
	tb := open(t, "demo", true, func(d *ft.DS) {
		d.RGs = nil
		for i := range 5200 {
			d.RGs = append(d.RGs, data.RowGroup{Index: i, Start: int64(i), Rows: 1, Compressed: 100, Uncompressed: 120})
		}
	})
	tb.Until(func() bool { return tb.ds.Calls.Load() == 1 })
	tb.release()
	tb.Until(func() bool { return tb.meta.RowGroups() > 0 })
	if got := tb.meta.RowGroups(); got != meta.Batch {
		t.Fatalf("first batch %d", got)
	}
	tb.Until(func() bool { return tb.meta.RowGroups() == meta.MaxRowGroups }) // the first 5000
	tb.Settle()
	if tb.meta.RowGroups() != meta.MaxRowGroups {
		t.Fatalf("%d rows", tb.meta.RowGroups())
	}
	if a, b := tb.meta.RowGroupCell(0, 0), tb.meta.RowGroupCell(4999, 0); a != "0" || b != "4999" {
		t.Fatalf("first %q last %q", a, b)
	}
	tb.Press("5")
	if !strings.Contains(tb.text(), "row groups  5,200") {
		t.Fatalf("title:\n%s", tb.text())
	}
}

// test_startup.py::test_quit_while_reading_footer: q stops the pass and
// nothing is reported.
func TestQuitWhileReadingFooter(t *testing.T) {
	tb := open(t, "demo", true)
	tb.Until(func() bool { return tb.ds.Calls.Load() == 1 })
	tb.Press("q")
	tb.Settle()
	if tb.meta.Status() != "" && !strings.HasPrefix(tb.meta.Status(), "Reading the footer") {
		t.Fatalf("status %q", tb.meta.Status())
	}
	if tb.env.Tasks.Running(footer.Tag) {
		t.Fatal("still running")
	}
}

// Esc leaves the footer pass running: it is background work, as in Python
// pqx (it isn't busy work there), so Esc doesn't cancel or restart it.
func TestEscLeavesFooterRunning(t *testing.T) {
	tb := open(t, "demo", true)
	tb.Until(func() bool { return tb.ds.Calls.Load() == 1 })
	tb.Press("2", "esc")
	tb.Settle()
	if !tb.env.Tasks.Running(footer.Tag) || tb.env.Tasks.Busy() {
		t.Fatalf("running %v busy %v", tb.env.Tasks.Running(footer.Tag), tb.env.Tasks.Busy())
	}
	tb.release()
	tb.read()
	if tb.ds.Calls.Load() != 1 || tb.schema.Rows() != len(tb.ds.Columns()) {
		t.Fatalf("%d calls, %d rows", tb.ds.Calls.Load(), tb.schema.Rows())
	}
}

// The pass waits for the grid's first page: it starts on the grid's first
// CursorMsg, or after the wait.
func TestFooterWaitsForFirstPage(t *testing.T) {
	ds := ft.Open(t, "demo")
	env := ft.Env(ds)
	r := footer.New(env)
	r.Wait = time.Hour
	if cmd := r.Update(tea.WindowSizeMsg{Width: 80, Height: 24}); cmd == nil {
		t.Fatal("no timer")
	}
	if env.Tasks.Running(footer.Tag) {
		t.Fatal("started at once")
	}
	if r.Update(kit.CursorMsg{}) == nil || !env.Tasks.Running(footer.Tag) {
		t.Fatal("didn't start on the grid's first cursor")
	}
	if r.Update(kit.CursorMsg{}) != nil {
		t.Fatal("started twice")
	}

	env = ft.Env(ds)
	r = footer.New(env)
	r.Wait = time.Millisecond
	cmd := r.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if r.Update(cmd()) == nil || !env.Tasks.Running(footer.Tag) {
		t.Fatal("didn't start after the wait")
	}
}

// test_app.py::test_tabs_stats_plots (Schema): every column; Enter shows
// its statistics.
func TestSchemaEnterOpensStats(t *testing.T) {
	tb := open(t, "demo", false)
	tb.read()
	tb.Press("2")
	if tb.schema.Rows() != len(tb.ds.Columns()) {
		t.Fatalf("%d rows", tb.schema.Rows())
	}
	n := names(tb.ds)
	for range slices.Index(n, "mag") {
		tb.Press("down")
	}
	tb.Press("enter")
	tb.Settle()
	if tb.env.State.Current != "mag" || !strings.Contains(tb.text(), "STATS") {
		t.Fatalf("current %q\n%s", tb.env.State.Current, tb.text())
	}
}

// test_app.py::test_schema_unit_column
func TestSchemaUnitColumn(t *testing.T) {
	tb := open(t, "demo", false)
	tb.read()
	if u := tb.schema.Cell("ra", "unit"); u != "deg" {
		t.Fatalf("ra unit %q", u)
	}
	if u := tb.schema.Cell("ssObjectId", "unit"); u != "–" {
		t.Fatalf("ssObjectId unit %q", u)
	}
	if n := tb.schema.Cell("ssObjectId", "nulls"); strings.ReplaceAll(n, ",", "") != "13934" {
		t.Fatalf("nulls %q", n)
	}
	if fmtx.Percent(1, 2) == "" {
		t.Skip("fmtx.Percent is a starter until WP3")
	}
	if p := tb.schema.Cell("ssObjectId", "null %"); !strings.HasSuffix(p, "%") {
		t.Fatalf("null %% %q", p)
	}
}

// test_app.py::test_schema_all_null_column
func TestSchemaAllNullColumn(t *testing.T) {
	tb := open(t, "odd", false)
	tb.read()
	if a, b := tb.schema.Cell("allnull", "min"), tb.schema.Cell("allnull", "max"); a != "–" || b != "–" {
		t.Fatalf("min %q max %q", a, b)
	}
	if u := tb.schema.Cell("allnull", "unit"); u != "–" {
		t.Fatalf("unit %q", u)
	}
	if n := tb.schema.Cell("allnull", "nulls"); n != "1,000" {
		t.Fatalf("nulls %q", n)
	}
	// nested columns add up their leaves and have no statistics
	if n, mn := tb.schema.Cell("pos", "nulls"), tb.schema.Cell("pos", "min"); n != "–" || mn != "–" {
		t.Fatalf("pos nulls %q min %q", n, mn)
	}
	for _, k := range []string{"2", "5"} {
		tb.Press(k, "down", "tab", "down")
	}
	if fmtx.Percent(1, 2) == "" {
		t.Skip("fmtx.Percent is a starter until WP3")
	}
	if p := tb.schema.Cell("allnull", "null %"); p != "100%" {
		t.Fatalf("null %% %q", p)
	}
}

// test_app.py::test_linked_columns_across_tabs (Schema's part)
func TestLinkedColumn(t *testing.T) {
	tb := open(t, "demo", false)
	tb.read()
	n := names(tb.ds)
	tb.gridMoves("mag")
	tb.Press("2")
	if tb.schema.Cursor() != slices.Index(n, "mag") || !strings.HasPrefix(tb.schema.Description(), "mag") {
		t.Fatalf("cursor %d", tb.schema.Cursor())
	}
	tb.Press("down")
	if tb.env.State.Current != "snr" {
		t.Fatalf("current %q", tb.env.State.Current)
	}
	// another part moves it while Schema shows: Schema follows, and doesn't
	// bounce it back
	tb.Press("3")
	tb.gridMoves("band")
	tb.Press("2")
	if tb.schema.Cursor() != slices.Index(n, "band") || tb.env.State.Current != "band" {
		t.Fatalf("cursor %d current %q", tb.schema.Cursor(), tb.env.State.Current)
	}
	// a column not in the file leaves Schema put
	tb.gridMoves("m2")
	if tb.schema.Cursor() != slices.Index(n, "band") || tb.env.State.Current != "m2" {
		t.Fatalf("cursor %d current %q", tb.schema.Cursor(), tb.env.State.Current)
	}
	// moving onto a column only Schema has (not in a SQL result) makes it current
	tb.Press("up")
	if tb.env.State.Current != n[slices.Index(n, "band")-1] {
		t.Fatalf("current %q", tb.env.State.Current)
	}
}

// The Schema screen at 200 × 50: the table with the cursor's column
// described below it.
func TestSchemaScreen(t *testing.T) {
	tb := open(t, "demo", false)
	tb.read()
	tb.gridMoves("ra")
	tb.Press("2")
	s := tb.Screen()
	if !strings.Contains(s[6], "1 Data ─ 2 Schema ─ 3 Stats ─ 4 Plot ─ 5 Meta") {
		t.Fatalf("tab strip %q", s[6])
	}
	head := tb.line("#  column")
	for _, h := range []string{"#", "column", "type", "unit", "nulls", "null %", "min", "max", "size", "ratio"} {
		if !strings.Contains(head, h) {
			t.Fatalf("header %q lacks %q", head, h)
		}
	}
	if l := tb.line(" ra "); !strings.Contains(l, "deg") || !strings.Contains(l, "×") {
		t.Fatalf("ra row %q", l)
	}
	desc := tb.line("column 2")
	if desc == "" {
		t.Fatalf("no description title:\n%s", tb.text())
	}
	if l := tb.line("ra   double   [deg]   nullable  ·  "); !strings.Contains(l, "dict, plain, rle") {
		t.Fatalf("description %q\n%s", l, tb.text())
	}
	if l := tb.line("→ enter opens statistics  ·  i from the data grid"); l == "" {
		t.Fatalf("no hint:\n%s", tb.text())
	}
	if len(s) != 50 || !strings.Contains(s[49], "↑↓ column   enter stats   / filter   1-5 tabs") {
		t.Fatalf("key bar %q", s[len(s)-1])
	}
}

// The Metadata screen: the overview, the key-value metadata and the
// row groups; Tab steps between the panels, then to the filter.
func TestMetaScreen(t *testing.T) {
	tb := open(t, "demo", false)
	tb.read()
	tb.Press("5")
	want := []string{
		"path        demo.parquet",
		"rows        20,000",
		"columns     16   16 leaf",
		"row groups  8   ~2,500 rows each",
		"format      2.6",
		"created by  parquet-cpp-arrow version 25.0.1",
		"key-value metadata",
		"ARROW:schema",
		"description   Synthetic LSST-like DiaSource table generated by pqx.demo",
		"generator   pqx.demo",
		"#  first row   rows",
		"0          0  2,500",
		"7     17,500  2,500",
		"✓ Footer read",
		"stats on 16/16 columns",
		"↑↓ scroll   tab next panel   1-5 tabs",
	}
	s := tb.text()
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Fatalf("no %q:\n%s", w, s)
		}
	}
	if !tb.meta.FocusedPanel(0) {
		t.Fatal("the file panel hasn't focus")
	}
	tb.Press("tab")
	if !tb.meta.FocusedPanel(1) {
		t.Fatal("tab didn't move to the row groups")
	}
	tb.Press("down", "down")
	if tb.meta.Cursor() != 2 {
		t.Fatalf("row-group cursor %d", tb.meta.Cursor())
	}
	tb.Press("tab") // on to the filter
	tb.Press("tab") // and back to the file
	if !tb.meta.FocusedPanel(0) {
		t.Fatal("tab didn't come back to the file panel")
	}
	tb.Press("shift+tab") // to the filter
	tb.Press("shift+tab")
	if !tb.meta.FocusedPanel(1) {
		t.Fatal("shift+tab didn't come back to the row groups")
	}
}

// test_security.py::test_widgets_show_no_control_characters (Schema and
// Metadata): names, units, descriptions and metadata from the file show
// their control characters as symbols.
func TestHostileTextIsSanitized(t *testing.T) {
	tb := open(t, "hostile", false)
	tb.read()
	for _, k := range []string{"2", "down", "down", "down", "5"} {
		tb.Press(k)
		raw := tb.Raw()
		for _, r := range raw {
			if (r < 0x20 && r != '\n') || r == 0x7f || (r >= 0x80 && r < 0xa0) {
				t.Fatalf("after %s: control character %U on screen", k, r)
			}
		}
	}
	tb.Press("2")
	if !strings.Contains(tb.text(), "␛") {
		t.Fatalf("no ␛ in Schema:\n%s", tb.text())
	}
}

// test_security.py::test_markup_in_names_and_values_is_shown_as_text:
// nothing is markup.
func TestMarkupShownAsText(t *testing.T) {
	tb := open(t, "hostile", false)
	tb.read()
	text := strings.Join(tb.meta.FileText(200), "\n")
	if !strings.Contains(text, "[@click=app.quit]x[/]") {
		t.Fatalf("markup not shown as text:\n%s", text)
	}
	tb.Press("2", "end")
	if !strings.Contains(tb.text(), "[bold]mk[/] [@click=app.quit]x[/]") {
		t.Fatalf("markup in a name not shown as text:\n%s", tb.text())
	}
}

// test_security.py::test_long_json_metadata_is_cut
func TestLongJSONMetadataIsCut(t *testing.T) {
	tb := open(t, "hostile", false)
	var big string
	for _, kv := range tb.ds.KV {
		if kv.Key == "big" {
			big = kv.Value
		}
	}
	if len(big) < meta.MaxKV {
		t.Fatalf("the fixture's big value is %d long", len(big))
	}
	tb.read()
	text := strings.Join(tb.meta.FileText(200), "\n")
	i := strings.Index(text, "big   {")
	if i < 0 || !strings.Contains(text[i:], "…") {
		t.Fatalf("big not cut:\n%s", text)
	}
	if len([]rune(text)) > 6000+len([]rune(text[:i])) {
		t.Fatalf("%d characters", len(text))
	}
}

// A click on a Schema row moves the cursor there; a click on the cursor row
// opens its statistics. The wheel scrolls.
func TestSchemaMouse(t *testing.T) {
	tb := open(t, "demo", false)
	tb.read()
	tb.Press("2")
	body := 6 // the body starts under the title bar, a blank row, the filter panel and another blank row
	row := func(i int) tea.MouseClickMsg {
		return tea.MouseClickMsg{X: 10, Y: body + 2 + i, Button: tea.MouseLeft}
	}
	tb.Send(row(3))
	if tb.schema.Cursor() != 3 || tb.env.State.Current != names(tb.ds)[3] {
		t.Fatalf("cursor %d current %q", tb.schema.Cursor(), tb.env.State.Current)
	}
	tb.Send(row(3))
	tb.Settle()
	if !strings.Contains(tb.text(), "STATS") {
		t.Fatalf("second click didn't open stats:\n%s", tb.text())
	}
}

// The file panel scrolls with the keys while it has focus.
func TestMetaScroll(t *testing.T) {
	tb := open(t, "hostile", false)
	tb.read()
	tb.Press("5")
	first := tb.line("│  path")
	if first == "" {
		t.Fatalf("no path line:\n%s", tb.text())
	}
	tb.Press("down", "down")
	if tb.line("│  path") != "" || tb.line("│  rows") == "" {
		t.Fatalf("didn't scroll:\n%s", tb.text())
	}
	tb.Press("end")
	if !strings.Contains(tb.text(), "…") {
		t.Fatalf("end doesn't show the cut value's end:\n%s", tb.text())
	}
	tb.Press("home")
	if tb.line("│  path") == "" {
		t.Fatal("home")
	}
}
