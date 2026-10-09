package detail

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/charmbracelet/x/ansi"
	_ "github.com/duckdb/duckdb-go/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/ui/app"
	"github.com/mjuric/pqx/go/internal/ui/kit"
	"github.com/mjuric/pqx/go/internal/ui/uitest"
)

// colsDS is a dataset that has only columns (the pane reads the file's
// units from it) and answers FetchColumns from fetch.
type colsDS struct {
	data.Unimplemented
	cols  []data.Column
	fetch func(ctx context.Context, rows []int64, cols []string) (data.Window, error)
	calls int
}

func (d *colsDS) Columns() []data.Column { return d.cols }
func (d *colsDS) FetchColumns(ctx context.Context, rows []int64, cols []string) (data.Window, error) {
	d.calls++
	if d.fetch == nil {
		return data.Window{}, data.ErrNotImplemented
	}
	return d.fetch(ctx, rows, cols)
}

// fakeGrid is the grid as the pane sees it.
type fakeGrid struct {
	rec    kit.Record
	merged []data.Window
	views  []kit.View
	read   func() (kit.View, []int64, []string, bool)
	keys   []string // "column key" of FieldKey calls
	value  data.Value
	values []string // columns FieldValue was asked for
}

func (g *fakeGrid) Record() kit.Record { return g.rec }
func (g *fakeGrid) Merge(v kit.View, w data.Window) {
	g.views = append(g.views, v)
	g.merged = append(g.merged, w)
}
func (g *fakeGrid) DetailRead() (kit.View, []int64, []string, bool) {
	if g.read == nil {
		return kit.View{}, nil, nil, false
	}
	return g.read()
}
func (g *fakeGrid) FieldKey(name string, k tea.KeyPressMsg) tea.Cmd {
	g.keys = append(g.keys, name+" "+k.String())
	return nil
}
func (g *fakeGrid) FieldValue(name string, fn func(v data.Value) tea.Cmd) tea.Cmd {
	g.values = append(g.values, name)
	return fn(g.value)
}

func col(name string, t arrow.DataType, unit string) data.Column {
	return data.Column{Name: name, Arrow: t, Unit: unit, SQLName: name}
}

var (
	f64 = arrow.PrimitiveTypes.Float64
	f32 = arrow.PrimitiveTypes.Float32
	i64 = arrow.PrimitiveTypes.Int64
	str = arrow.BinaryTypes.String
)

type rig struct {
	t    *testing.T
	p    *Pane
	g    *fakeGrid
	ds   *colsDS
	env  *kit.Env
	msgs []tea.Msg // every message the pane's commands yielded
}

// newRig is a pane over cols with the record rec, open, w × h.
func newRig(t *testing.T, cols []data.Column, rec kit.Record, w, h int) *rig {
	ds := &colsDS{cols: cols}
	g := &fakeGrid{rec: rec}
	env := &kit.Env{DS: ds, Look: app.BasicLook{}, Tasks: kit.NewTasks(), Grid: g,
		State: &kit.State{Columns: cols, Hidden: map[string]bool{}, Formats: map[string]fmtx.Override{}, DetailOpen: true}}
	if len(cols) > 0 {
		env.State.Current = cols[0].Name
	}
	r := &rig{t: t, p: New(env), g: g, ds: ds, env: env}
	r.p.View(w, h)
	r.send(kit.ToggleDetailMsg{})
	r.p.View(w, h)
	return r
}

// send gives msg to the pane and runs what its commands yield: the pane's
// own messages go back to it (a task's result as the root passes it on),
// the others are recorded.
func (r *rig) send(msg tea.Msg) {
	queue := []tea.Msg{msg}
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		if d, ok := m.(kit.DoneMsg); ok && !r.env.Tasks.Done(d) {
			continue
		}
		for _, out := range uitest.Drain(r.p.Update(m)) {
			switch out.(type) {
			case fetchTick, kit.DoneMsg:
				queue = append(queue, out)
				continue
			default:
				r.msgs = append(r.msgs, out)
			}
		}
	}
}

func (r *rig) press(keys ...string) {
	for _, k := range keys {
		r.send(uitest.Key(k))
	}
}

// last is the last message of msg's type the pane sent, or nil.
func last[T any](r *rig) (T, bool) {
	for i := len(r.msgs) - 1; i >= 0; i-- {
		if m, ok := r.msgs[i].(T); ok {
			return m, true
		}
	}
	var zero T
	return zero, false
}

func (r *rig) value(name string) string {
	r.t.Helper()
	v, ok := r.p.Value(name)
	if !ok {
		r.t.Fatalf("no entry %q", name)
	}
	return v
}

func demoCols() []data.Column {
	return []data.Column{
		col("diaSourceId", i64, ""), col("ra", f64, "deg"), col("dec", f64, "deg"),
		col("raErr", f32, "deg"), col("midpointMjdTai", f64, "d"), col("band", str, ""),
		col("psfFlux", f32, "nJy"), col("trailLength", f64, "arcsec"), col("note", str, ""),
	}
}

func TestEntriesTitleAndValues(t *testing.T) {
	rec := kit.Record{Row: 1234, FileRow: 5678, Values: map[string]data.Value{
		"diaSourceId": int64(170000000000000000), "ra": 333.1528812003216, "dec": -4.229769630326729,
		"raErr": float32(2.6714137e-05), "midpointMjdTai": 60800.00697713115, "band": "r",
		"psfFlux": float32(32426.762), "trailLength": nil,
	}, Missing: []string{"note"}}
	r := newRig(t, demoCols(), rec, 49, 30)
	if got := r.p.Title().Plain; got != "row 1,234 · file row 5,678" {
		t.Fatalf("title %q", got)
	}
	if !r.p.Title().Style.Dim {
		t.Fatal("title not dim")
	}
	// (the readings as Python pqx shows them: see the screenshots in the PR)
	want := map[string]string{
		"diaSourceId":    "170000000000000000",
		"ra":             "333.1528812003216  deg\n· 22h12m36.691s",
		"dec":            "-4.229769630326729  deg\n· -04°13′47.17″",
		"raErr":          "2.6714137e-05  deg\n· 96.2 mas",
		"midpointMjdTai": "60800.00697713115  d\n· 2025-05-05 00:10:02.824",
		"band":           "r",
		"psfFlux":        "32426.762  nJy\n· 20.123 AB mag (if nJy)",
		"trailLength":    "∅", // no unit after NULL
		"note":           "…",
	}
	for n, v := range want {
		if got := r.value(n); got != v {
			t.Errorf("%s: %q, want %q", n, got, v)
		}
	}
	if got := strings.Join(r.p.Names(), " "); got != "diaSourceId ra dec raErr midpointMjdTai band psfFlux trailLength note" {
		t.Fatalf("names %s", got)
	}
	// the screen: names in a 14-wide column (the longest), values after two spaces
	lines := strings.Split(ansi.Strip(r.p.View(49, 30)), "\n")
	if lines[0] != "diaSourceId     170000000000000000               " || lines[2] != "                · 22h12m36.691s                  " {
		t.Fatalf("%q", lines[:3])
	}
	if len(lines) != 30 || ansi.StringWidth(lines[29]) != 49 {
		t.Fatalf("%d lines", len(lines))
	}

	// failed columns; no file rows; pending record; no rows
	r.g.rec.Failed = map[string]error{"note": errors.New("x")}
	r.g.rec.Missing = nil
	r.g.rec.FileRow = -1
	r.send(kit.CursorMsg{})
	if r.value("note") != "✗ couldn't load" || r.p.Title().Plain != "row 1,234" {
		t.Fatalf("%q %q", r.value("note"), r.p.Title().Plain)
	}
	r.g.rec.Pending, r.g.rec.FileRow = true, 15000
	r.send(kit.CursorMsg{})
	if r.p.Title().Plain != "finding record… · file row 15,000" {
		t.Fatalf("%q", r.p.Title().Plain)
	}
	r.g.rec = kit.Record{Row: -1, FileRow: -1}
	r.send(kit.ViewChangedMsg{})
	if r.p.Title().Plain != "no rows" || len(r.p.Names()) != 0 || r.p.Selected() != "" {
		t.Fatalf("%q %v", r.p.Title().Plain, r.p.Names())
	}
	r.press("down", "end", "=", "y") // nothing to wander into
	if len(r.msgs) != 0 || len(r.g.keys) != 0 {
		t.Fatalf("%v %v", r.msgs, r.g.keys)
	}
}

func TestHiddenColumnsAreLeftOut(t *testing.T) {
	cols := demoCols()
	r := newRig(t, cols, kit.Record{Row: 0, FileRow: 0, Values: map[string]data.Value{}}, 49, 20)
	r.env.State.Hidden["ra"] = true
	r.send(kit.ColumnsChangedMsg{})
	if strings.Contains(strings.Join(r.p.Names(), " "), "ra ") || len(r.p.Names()) != len(cols)-1 {
		t.Fatalf("%v", r.p.Names())
	}
}

// Port of tests/test_app.py::test_detail_pane_fits_full_precision_numbers:
// any double at full precision and any int64 fit on the name's line, with
// the scrollbar showing and the longest name.
func TestFitsFullPrecisionNumbers(t *testing.T) {
	cols := []data.Column{col("midpointMjdTai_flag_degraded", arrow.FixedWidthTypes.Boolean, ""),
		col("ra", f64, ""), col("tiny", f64, ""), col("id_min", i64, ""), col("diaSourceId", i64, "")}
	vals := map[string]data.Value{"midpointMjdTai_flag_degraded": true, "ra": 348.11223041553035,
		"tiny": -1.2345678901234567e-123, "id_min": int64(math.MinInt64), "diaSourceId": int64(745504140635930649)}
	for i := 0; i < 60; i++ {
		n := "c" + string(rune('A'+i/26)) + string(rune('a'+i%26))
		cols = append(cols, col(n, f64, ""))
		vals[n] = float64(i)
	}
	r := newRig(t, cols, kit.Record{Row: 0, FileRow: 0, Values: vals}, 49, 40)
	r.p.View(49, 40)
	if !r.p.bar || r.p.layW != 48 || r.p.nameW != 22 {
		t.Fatalf("bar %v width %d name width %d", r.p.bar, r.p.layW, r.p.nameW)
	}
	for i, e := range r.p.entries {
		switch e.name {
		case "ra", "tiny", "id_min", "diaSourceId":
			first := strings.Split(e.value.Plain, "\n")[0]
			if utf8.RuneCountInString(first) > 24 {
				t.Fatalf("%s: %q", e.name, first)
			}
			if r.p.hts[i] != strings.Count(e.value.Plain, "\n")+1 {
				t.Fatalf("%s takes %d lines: %q", e.name, r.p.hts[i], e.value.Plain)
			}
		}
	}
}

// Port of tests/test_app.py::test_detail_pane_heights_follow_its_width_and_prompts:
// entries are measured again when the scrollbar comes or goes.
func TestHeightsFollowTheScrollbar(t *testing.T) {
	cols := []data.Column{col("a", i64, "")}
	r := newRig(t, cols, kit.Record{Row: 0, FileRow: 0, Values: map[string]data.Value{"a": int64(1)}}, 49, 30)
	if r.p.bar {
		t.Fatal("a scrollbar for one entry")
	}
	long := strings.Repeat("x", 49-4-2) // just fits the value column without a scrollbar
	cols = nil
	vals := map[string]data.Value{}
	for i := 0; i < 60; i++ {
		n := "c" + string(rune('0'+i/10)) + string(rune('0'+i%10))
		cols = append(cols, col(n, str, ""))
		vals[n] = n[1:]
	}
	r.env.State.Columns = cols
	vals["c03"] = long
	r.g.rec.Values = vals
	r.send(kit.ViewChangedMsg{})
	r.p.View(49, 30)
	if !r.p.bar || r.p.layW != 48 || r.p.nameW != 3 {
		t.Fatalf("bar %v width %d", r.p.bar, r.p.layW)
	}
	if r.p.hts[3] != 1 { // name width 3 here: still fits in 48 - 5
		t.Fatalf("height %d", r.p.hts[3])
	}
	vals["c03"] = long + "xx" // one column narrower than without the bar: it wraps
	r.send(kit.CursorMsg{})
	r.p.View(49, 30)
	if r.p.hts[3] != 2 || r.p.total != 61 {
		t.Fatalf("height %d total %d", r.p.hts[3], r.p.total)
	}
	r.press("down", "down", "down")
	text := strings.Split(ansi.Strip(r.p.View(49, 30)), "\n")
	got := strings.ReplaceAll(text[3]+text[4], " ", "")
	if !strings.HasPrefix(got, "c03"+long+"xx") {
		t.Fatalf("%q", text[3:5])
	}
}

func TestLongValuesAreCut(t *testing.T) {
	big := strings.Repeat("ab", 600)
	r := newRig(t, []data.Column{col("j", str, "")}, kit.Record{Row: 0, FileRow: 0, Values: map[string]data.Value{"j": big}}, 49, 20)
	if want := big[:300] + "… (1,200 chars; y copies it all)"; r.value("j") != want {
		t.Fatalf("%q", r.value("j"))
	}
}

// Port of the pane parts of tests/test_app.py::test_detail_pane_focus_and_link:
// moves in the pane drive the grid (the current column); the grid's moves
// move the selection without a message back.
func TestKeysMoveTheSelection(t *testing.T) {
	cols := demoCols()
	vals := map[string]data.Value{}
	for _, c := range cols {
		vals[c.Name] = "v"
	}
	r := newRig(t, cols, kit.Record{Row: 7, FileRow: 7, Values: vals}, 49, 6)
	st := r.env.State
	st.Current = "dec"
	r.send(kit.ColumnChangedMsg{From: "grid"})
	if r.p.Selected() != "dec" || len(r.msgs) != 0 {
		t.Fatalf("%q %v", r.p.Selected(), r.msgs)
	}
	r.p.Focus()
	r.press("down", "down")
	if r.p.Selected() != "midpointMjdTai" || st.Current != "midpointMjdTai" {
		t.Fatalf("%q %q", r.p.Selected(), st.Current)
	}
	if m, ok := last[kit.ColumnChangedMsg](r); !ok || m.From != Part {
		t.Fatalf("%v", r.msgs)
	}
	// its own announcement doesn't move it
	r.msgs = nil
	r.send(kit.ColumnChangedMsg{From: Part})
	if r.p.Selected() != "midpointMjdTai" || len(r.msgs) != 0 {
		t.Fatal("moved")
	}
	r.press("end")
	if r.p.Selected() != "note" || r.p.Top() != len(cols)-6 {
		t.Fatalf("%q top %d", r.p.Selected(), r.p.Top())
	}
	r.press("home")
	if r.p.Selected() != "diaSourceId" || r.p.Top() != 0 {
		t.Fatal(r.p.Selected())
	}
	r.press("pgdown")
	if r.p.Selected() != "psfFlux" { // a page of 6 lines down
		t.Fatal(r.p.Selected())
	}
	r.press("pgup", "pgup")
	if r.p.Selected() != "diaSourceId" {
		t.Fatal(r.p.Selected())
	}
	r.msgs = nil
	r.press("enter")
	if m, ok := last[kit.FocusMsg](r); !ok || m.Pane != "grid" {
		t.Fatalf("%v", r.msgs)
	}
	r.press("d")
	if _, ok := last[kit.ToggleDetailMsg](r); !ok {
		t.Fatal("d")
	}
	r.press("down", "i")
	if m, ok := last[kit.ColumnStatsMsg](r); !ok || m.Column != "ra" {
		t.Fatalf("%v", r.msgs)
	}
	r.press("y", "F", "<", ">", "x")
	if strings.Join(r.g.keys, ",") != "ra y,ra F,ra <,ra >" {
		t.Fatalf("%v", r.g.keys) // (x: no filter to clear)
	}
	st.View = data.View{Where: "band = 'r'"}
	r.msgs = nil
	r.press("x")
	if r.g.keys[len(r.g.keys)-1] != "ra x" {
		t.Fatalf("%v", r.g.keys)
	}
	if m, ok := last[kit.FocusMsg](r); !ok || m.Pane != "grid" {
		t.Fatalf("x doesn't hand focus to the grid: %v", r.msgs)
	}
}

func TestMouse(t *testing.T) {
	cols := demoCols()
	vals := map[string]data.Value{}
	for _, c := range cols {
		vals[c.Name] = 1.5 // with derived readings for some: two lines
	}
	r := newRig(t, cols, kit.Record{Row: 7, FileRow: 7, Values: vals}, 49, 6)
	r.send(tea.MouseWheelMsg{X: 3, Y: 2, Button: tea.MouseWheelDown})
	r.send(tea.MouseWheelMsg{X: 3, Y: 2, Button: tea.MouseWheelDown})
	if r.p.Top() != 4 || r.p.Selected() != "diaSourceId" || len(r.msgs) != 0 {
		t.Fatalf("top %d %q %v", r.p.Top(), r.p.Selected(), r.msgs)
	}
	top := r.p.Top()
	r.send(kit.CursorMsg{}) // a new row keeps the pane where it was scrolled to
	if r.p.Top() != top {
		t.Fatal("scrolled back")
	}
	// line 4 of the pane is the 9th line of the entries
	want := r.p.lineAt(0)
	r.send(tea.MouseClickMsg{X: 3, Y: 0, Button: tea.MouseLeft})
	if r.p.Selected() != r.p.entries[want].name || r.env.State.Current != r.p.Selected() {
		t.Fatalf("%q", r.p.Selected())
	}
	r.send(tea.MouseWheelMsg{X: 3, Y: 2, Button: tea.MouseWheelUp})
	r.send(tea.MouseWheelMsg{X: 3, Y: 2, Button: tea.MouseWheelUp})
	r.send(tea.MouseWheelMsg{X: 3, Y: 2, Button: tea.MouseWheelUp})
	if r.p.Top() != 0 {
		t.Fatal(r.p.Top())
	}
}

// The selection is reverse video whole while the pane has focus, only the
// name otherwise.
func TestSelectionLook(t *testing.T) {
	r := newRig(t, demoCols(), kit.Record{Row: 0, FileRow: 0, Values: map[string]data.Value{"band": "r"}}, 49, 12)
	r.env.State.Current = "band"
	r.send(kit.ColumnChangedMsg{From: "grid"})
	reversed := func() []string {
		var out []string
		for _, l := range strings.Split(r.p.View(49, 12), "\n") {
			if strings.Contains(l, "\x1b[7") || strings.Contains(l, ";7m") || strings.Contains(l, ";7;") {
				out = append(out, l)
			}
		}
		return out
	}
	rv := reversed()
	if len(rv) != 1 || !strings.HasPrefix(ansi.Strip(rv[0]), "band") {
		t.Fatalf("%q", rv)
	}
	if strings.Count(rv[0], "\x1b[") > 4 { // the name only: bold reverse, then the rest plain
		t.Logf("%q", rv[0])
	}
	r.p.Focus()
	rv = reversed()
	if len(rv) != 1 {
		t.Fatalf("%q", rv)
	}
	// every cell reversed: the reverse style runs to the end of the line
	if s := rv[0]; !strings.HasSuffix(ansi.Strip(s), " ") || strings.Count(s, "\x1b[m") > 1+strings.Count(s, "\x1b[7") {
		t.Fatalf("%q", s)
	}
	r.p.Blur()
	if len(reversed()) != 1 {
		t.Fatal("blur")
	}
}

func TestEquals(t *testing.T) {
	cols := []data.Column{col("band", str, ""), col("detector", i64, ""), col("ssObjectId", i64, ""),
		col("x", f64, ""), col("select", str, ""), col("weird name", i64, ""), col("bell\x07", i64, ""),
		col("flag", arrow.FixedWidthTypes.Boolean, ""), col("f", f32, ""), col("blob", arrow.BinaryTypes.Binary, ""),
		col("t", &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}, ""), col("day", arrow.FixedWidthTypes.Date32, ""),
		{Name: "Name", Arrow: str, SQLName: "Name_1"}}
	ts := time.Date(2026, 1, 2, 3, 4, 5, 678901000, time.UTC)
	rec := kit.Record{Row: 50, FileRow: 15000, Values: map[string]data.Value{
		"band": "it's \x1b[31m", "detector": int64(29), "ssObjectId": nil, "x": math.NaN(), "select": "s",
		"weird name": int64(2), "bell\x07": int64(1), "flag": true, "f": float32(0.1), "blob": []byte("x"),
		"t": data.Timestamp{T: ts, Zoned: true, Unit: time.Microsecond}, "day": data.Date(20454), "Name": "n",
	}}
	r := newRig(t, cols, rec, 49, 30)
	st := r.env.State
	st.View = data.View{OrderBy: []data.Sort{{Column: "mag"}}}
	want := map[string]string{
		"band":       `band = ('it''s ' || chr(27) || '[31m')`,
		"detector":   "detector = 29",
		"ssObjectId": "ssObjectId IS NULL",
		"x":          "isnan(x)",
		"select":     `"select" = 's'`,
		"weird name": `"weird name" = 2`,
		"bell\x07":   "COLUMNS(c -> c = ('bell' || chr(7))) = 1",
		"flag":       "flag = true",
		"f":          "f = 0.10000000149011612",
		"t":          "t = TIMESTAMPTZ '2026-01-02T03:04:05.678901+00:00'",
		"day":        `"day" = DATE '2026-01-01'`, // (a keyword)
		"Name":       "Name_1 = 'n'",
	}
	for name, cond := range want {
		st.Current = name
		r.send(kit.ColumnChangedMsg{From: "grid"})
		r.msgs = nil
		r.press("=")
		m, ok := last[kit.SetViewMsg](r)
		if !ok || m.View.Where != cond || m.KeepFileRow != 15000 || len(m.View.OrderBy) != 1 {
			t.Fatalf("%s: %+v", name, r.msgs)
		}
	}
	// added to the filter; parenthesized when it has an OR
	st.Current = "detector"
	r.send(kit.ColumnChangedMsg{From: "grid"})
	for cur, w := range map[string]string{"band = 'r'": "band = 'r' and detector = 29",
		"a = 1 OR b = 2": "(a = 1 OR b = 2) and detector = 29"} {
		st.View = data.View{Where: cur}
		r.press("=")
		if m, _ := last[kit.SetViewMsg](r); m.View.Where != w {
			t.Fatalf("%q", m.View.Where)
		}
	}
	// values it can't match; SQL results
	st.Current = "blob"
	r.send(kit.ColumnChangedMsg{From: "grid"})
	r.msgs = nil
	r.press("=")
	if m, ok := last[kit.NotifyMsg](r); !ok || m.Text != "Can't filter on this value type" {
		t.Fatalf("%v", r.msgs)
	}
	st.View = data.View{SQL: "select 1"}
	r.press("=")
	if m, _ := last[kit.NotifyMsg](r); !strings.Contains(m.Text, "not on SQL results") {
		t.Fatal(m.Text)
	}
	// a value not loaded yet: the grid reads it first
	st.View = data.View{}
	st.Current = "detector"
	r.send(kit.ColumnChangedMsg{From: "grid"})
	delete(r.g.rec.Values, "detector")
	r.g.rec.Missing = []string{"detector"}
	r.send(kit.CursorMsg{})
	r.g.value = int64(7)
	r.msgs = nil
	r.press("=")
	if m, ok := last[kit.SetViewMsg](r); !ok || m.View.Where != "detector = 7" || len(r.g.values) != 1 {
		t.Fatalf("%v %v", r.msgs, r.g.values)
	}
	// one that couldn't be read
	r.g.rec.Failed = map[string]error{"detector": errors.New("x")}
	r.g.rec.Missing = nil
	r.send(kit.CursorMsg{})
	r.msgs = nil
	r.press("=")
	if m, ok := last[kit.NotifyMsg](r); !ok || !strings.Contains(m.Text, "couldn't be loaded") {
		t.Fatalf("%v", r.msgs)
	}
}

// The background read: after the cursor settles, once; merged into the
// grid; failures marked there and said once.
func TestBackgroundRead(t *testing.T) {
	cols := demoCols()
	rec := kit.Record{Row: 3, FileRow: 3, Values: map[string]data.Value{"diaSourceId": int64(3)},
		Missing: []string{"ra", "dec"}}
	r := newRig(t, cols, kit.Record{Row: -1, FileRow: -1}, 49, 20)
	view := kit.View{Gen: 4}
	r.g.read = func() (kit.View, []int64, []string, bool) { return view, []int64{2, 3, 4}, []string{"ra", "dec"}, true }
	r.ds.fetch = func(ctx context.Context, rows []int64, cs []string) (data.Window, error) {
		w := data.Window{Len: len(rows), FileRows: rows, Cols: map[string][]data.Value{}}
		for _, c := range cs {
			w.Cols[c] = []data.Value{1.0, 2.0, 3.0}
		}
		return w, nil
	}
	r.g.rec = rec
	t0 := time.Now()
	r.send(kit.CursorMsg{})
	if d := time.Since(t0); d < fetchDelay {
		t.Fatalf("read after %v", d)
	}
	if r.ds.calls != 1 || len(r.g.merged) != 1 || r.g.views[0].Gen != 4 || len(r.g.merged[0].Cols) != 2 {
		t.Fatalf("calls %d merged %v", r.ds.calls, r.g.merged)
	}
	if r.value("ra") != "…" { // (the fake grid's record doesn't change)
		t.Fatal(r.value("ra"))
	}
	// a pending record waits; so does a closed pane
	r.g.rec.Pending = true
	r.send(kit.CursorMsg{})
	r.g.rec.Pending = false
	r.env.State.DetailOpen = false
	r.send(kit.CursorMsg{})
	if r.ds.calls != 1 {
		t.Fatalf("%d reads", r.ds.calls)
	}
	// the same read again would bring nothing new
	r.env.State.DetailOpen = true
	r.send(kit.CursorMsg{})
	if r.ds.calls != 1 {
		t.Fatalf("%d reads", r.ds.calls)
	}
	// a failed read: every column marked failed, said once
	view.Gen = 5
	r.ds.fetch = func(ctx context.Context, rows []int64, cs []string) (data.Window, error) {
		return data.Window{}, errors.New("IO Error: \x1b]0;pwned\x07 broken\nmore")
	}
	r.msgs = nil
	r.send(kit.CursorMsg{})
	w := r.g.merged[len(r.g.merged)-1]
	if r.ds.calls != 2 || len(w.Failed) != 2 || len(w.FileRows) != 3 {
		t.Fatalf("%d %v", r.ds.calls, w)
	}
	m, ok := last[kit.NotifyMsg](r)
	if !ok || m.Title != "✗ Columns" || m.Text != "Couldn't load 2 columns: IO Error: ␛]0;pwned␇ broken" {
		t.Fatalf("%q", m.Text)
	}
	// a read running: no second one
	r.env.Tasks.Run("detail", "x", false, func(context.Context) tea.Msg { return nil })
	r.send(kit.CursorMsg{})
	if r.ds.calls != 2 {
		t.Fatal("read twice")
	}
}

var sgrSeq = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// unsafe reports the first character of s (SGR sequences removed) that a
// terminal could act on: C0 and C1 controls but newline, DEL, bidi
// controls, zero-width characters.
func unsafe(s string) (rune, bool) {
	for _, r := range sgrSeq.ReplaceAllString(s, "") {
		switch {
		case r == '\n':
		case r < 0x20, r >= 0x7F && r < 0xA0,
			r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0x061C,
			r >= 0x200B && r <= 0x200D, r == 0x2060, r == 0xFEFF:
			return r, true
		}
	}
	return 0, false
}

// Port of tests/test_security.py::test_widgets_show_no_control_characters
// (the pane): hostile names, values, units and errors never reach the
// terminal as control characters, byte for byte.
func TestHostileTextIsShownSafely(t *testing.T) {
	evil := []string{"\x1b]0;PWNED\x07T", "\x1b]52;c;cHduZWQ=\x07C", "a\x9b31mb", "\u202egnp.exe", "zw\u200bj",
		"cr\rlf\nnl\ttab", "del\x7f", "\x1b[2J\x1b[H", "[bold]mk[/] [@click=app.quit]x[/]"}
	var cols []data.Column
	vals := map[string]data.Value{}
	for i, e := range evil {
		c := col(e+string(rune('a'+i)), str, "u\x1b[5m")
		cols = append(cols, c)
		vals[c.Name] = e
	}
	cols = append(cols, col("ra\x1b[31m", f64, "deg\x1b]0;x\x07"))
	vals["ra\x1b[31m"] = 12.5
	cols = append(cols, col("long", str, ""))
	vals["long"] = strings.Repeat("\x1b[31m", 100)
	r := newRig(t, cols, kit.Record{Row: 1, FileRow: 1, Values: vals}, 49, 60)
	for _, focused := range []bool{false, true} {
		if focused {
			r.p.Focus()
		}
		for _, name := range r.p.Names() {
			r.env.State.Current = name
			r.send(kit.ColumnChangedMsg{From: "grid"})
			if c, bad := unsafe(r.p.View(49, 60)); bad {
				t.Fatalf("%U on screen with %q selected", c, name)
			}
		}
	}
	for _, name := range r.p.Names() {
		if c, bad := unsafe(r.value(name)); bad && c != '\t' { // (tab and newline are text, as in Python)
			t.Fatalf("%q: %U", name, c)
		}
	}
	r.g.rec.Pending = true
	r.send(kit.CursorMsg{})
	if _, bad := unsafe(r.p.Title().Plain); bad {
		t.Fatal(r.p.Title().Plain)
	}
}

// The DuckDB keywords built in are those of the DuckDB pqx is built with.
func TestKeywordsAreDuckDBs(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT lower(keyword_name) FROM duckdb_keywords()")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	live := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		live[k] = true
	}
	if !reflect.DeepEqual(live, keywords) {
		t.Fatalf("DuckDB has %d keywords, keywords.go %d: regenerate keywords.go", len(live), len(keywords))
	}
	for _, k := range []string{"select", "from", "order", "asof", "qualify"} {
		if sqlIdent(k) != `"`+k+`"` {
			t.Fatalf("%s: %s", k, sqlIdent(k))
		}
	}
	for _, k := range []string{"ra", "band", "detector", "mag", "x_1", "_a"} {
		if sqlIdent(k) != k {
			t.Fatalf("%s: %s", k, sqlIdent(k))
		}
	}
	if sqlIdent(`q"uote`) != `"q""uote"` || sqlIdent("a.b") != `"a.b"` || sqlIdent("1a") != `"1a"` {
		t.Fatal("quoting")
	}
}
