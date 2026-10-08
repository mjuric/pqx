package stats

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/ui/app"
	"github.com/mjuric/pqx/go/internal/ui/kit"
	"github.com/mjuric/pqx/go/internal/ui/uitest"
)

func setup(t *testing.T) (*uitest.Driver, *Pane, *uitest.FakeDS) {
	t.Helper()
	ds := uitest.Demo(t)
	env := uitest.Env(ds)
	p := New(env)
	d := uitest.NewDriver(t, env, app.Parts{Stats: p}, 150, 42)
	return d, p, ds
}

func names(p *Pane) []string {
	var out []string
	for _, c := range p.env.State.Columns {
		out = append(out, c.Name)
	}
	return out
}

func idx(p *Pane, name string) int {
	for i, n := range names(p) {
		if n == name {
			return i
		}
	}
	return -1
}

func text(lines []string) string { return strings.Join(lines, "\n") }

// moveTo highlights name in the list with the arrow keys.
func moveTo(d *uitest.Driver, p *Pane, name string) {
	for p.list.Highlighted() < idx(p, name) {
		d.Press("down")
	}
	for p.list.Highlighted() > idx(p, name) {
		d.Press("up")
	}
}

// test_tabs_stats_plots (Stats part) and test_linked_columns_jumps: Schema
// Enter / "i" lands on the column's stats; a highlight profiles after the
// debounce.
func TestStatsTabAndJumps(t *testing.T) {
	d, p, ds := setup(t)
	d.Press("3")
	if p.Shown() != "diaSourceId" || !reflect.DeepEqual(ds.StatsCalls(), []string{"diaSourceId"}) {
		t.Fatalf("shown %q calls %v", p.Shown(), ds.StatsCalls())
	}
	d.Press("1")
	d.Send(kit.ColumnStatsMsg{Column: "mag"})
	if p.Shown() != "mag" || p.list.HighlightedID() != "mag" {
		t.Fatalf("shown %q highlighted %q", p.Shown(), p.list.HighlightedID())
	}
	moveTo(d, p, "band") // each step waits out its debounce: profiled at each stop
	if p.Shown() != "band" || d.Env.State.Current != "band" {
		t.Fatalf("shown %q current %q", p.Shown(), d.Env.State.Current)
	}
	// a highlight change announces the current column
	var from []string
	for _, m := range d.Msgs {
		if c, ok := m.(kit.ColumnChangedMsg); ok {
			from = append(from, c.From)
		}
	}
	if len(from) == 0 || from[len(from)-1] != "stats" {
		t.Fatalf("ColumnChangedMsg from %v", from)
	}
	scr := text(d.Screen())
	for _, want := range []string{"band   ", "✓ Profiled band", "20,000 rows", "all rows", "most frequent", "█", "distinct"} {
		if !strings.Contains(scr, want) {
			t.Errorf("screen lacks %q:\n%s", want, scr)
		}
	}
}

// A fast run through the list profiles only where it stops (the debounce).
func TestHighlightDebounced(t *testing.T) {
	d, p, ds := setup(t)
	d.Press("3")
	// three moves delivered before any debounce fires: only the last profiles
	var ticks []any
	for i := 0; i < 3; i++ {
		for _, m := range uitest.Drain(p.Update(uitest.Key("down"))) {
			if _, ok := m.(debounceMsg); ok {
				ticks = append(ticks, m)
			} else {
				d.Send(m)
			}
		}
	}
	for _, m := range ticks {
		d.Send(m)
	}
	if got := ds.StatsCalls(); !reflect.DeepEqual(got, []string{"diaSourceId", "dec"}) {
		t.Fatalf("calls %v", got)
	}
	if p.Shown() != "dec" {
		t.Fatalf("shown %q", p.Shown())
	}
}

// test_linked_columns_across_tabs (Stats parts): Stats follows the current
// column when shown, profiles it once, and leaving and coming back doesn't
// profile again.
func TestLinkedColumns(t *testing.T) {
	d, p, ds := setup(t)
	st := d.Env.State
	st.Current = "snr"
	d.Press("3")
	if p.list.HighlightedID() != "snr" || p.Shown() != "snr" || !reflect.DeepEqual(ds.StatsCalls(), []string{"snr"}) {
		t.Fatalf("highlighted %q shown %q calls %v", p.list.HighlightedID(), p.Shown(), ds.StatsCalls())
	}
	d.Press("down")
	if st.Current != "trailLength" || p.Shown() != "trailLength" {
		t.Fatalf("current %q shown %q", st.Current, p.Shown())
	}
	d.Press("1", "3") // Data -> Stats: already profiled, not again
	if p.list.HighlightedID() != "trailLength" || len(ds.StatsCalls()) != 2 {
		t.Fatalf("highlighted %q calls %v", p.list.HighlightedID(), ds.StatsCalls())
	}
	// moved elsewhere while Stats is hidden: followed when shown
	d.Press("1")
	st.Current = "isDipole"
	d.Send(kit.ColumnChangedMsg{From: "grid"})
	if len(ds.StatsCalls()) != 2 {
		t.Fatal("profiled while hidden")
	}
	d.Press("ctrl+right", "ctrl+right") // Data -> Schema -> Stats
	if p.list.HighlightedID() != "isDipole" || p.Shown() != "isDipole" {
		t.Fatalf("highlighted %q shown %q", p.list.HighlightedID(), p.Shown())
	}
	// a column that isn't in the view keeps Stats where it is, and the
	// current column stays
	d.Press("1")
	st.Current = "nope"
	d.Press("3")
	if p.Column() != "isDipole" || st.Current != "nope" {
		t.Fatalf("column %q current %q", p.Column(), st.Current)
	}
}

// test_linked_columns_filters (Stats parts): a filter on Stats re-profiles
// under it; a query dropping the column lands on its first column, leaving
// the current column alone.
func TestViewChanges(t *testing.T) {
	d, p, ds := setup(t)
	st := d.Env.State
	st.Current = "mag"
	d.Press("3")
	st.View = data.View{Where: "mag < 20 and band = 'r'"}
	d.Send(kit.ViewChangedMsg{})
	calls := ds.Calls()
	last := calls[len(calls)-1]
	if p.Shown() != "mag" || last.View.Where != st.View.Where || p.stale {
		t.Fatalf("shown %q last call %+v", p.Shown(), last)
	}
	if !strings.Contains(text(d.Screen()), "where mag < 20 and band = 'r'") {
		t.Fatalf("no scope:\n%s", text(d.Screen()))
	}
	// a SQL result without mag
	st.View = data.View{SQL: "select ra, dec from t"}
	st.Columns = []data.Column{ds.Cols[2], ds.Cols[3]}
	d.Send(kit.ViewChangedMsg{})
	if p.Column() != "ra" || p.list.Highlighted() != 0 || p.Shown() != "ra" || st.Current != "mag" {
		t.Fatalf("column %q highlighted %d shown %q current %q", p.Column(), p.list.Highlighted(), p.Shown(), st.Current)
	}
	if !strings.Contains(text(d.Screen()), "columns  2") {
		t.Fatalf("list title:\n%s", text(d.Screen()))
	}
	// cleared while on another tab: the summary goes, recomputed when shown
	d.Press("1")
	st.View, st.Columns = data.View{}, ds.Cols
	n := len(ds.StatsCalls())
	d.Send(kit.ViewChangedMsg{})
	if len(ds.StatsCalls()) != n || p.summary != nil {
		t.Fatalf("hidden: calls %v summary %v", ds.StatsCalls(), p.summary)
	}
	d.Press("3")
	if p.Shown() != "mag" || p.list.HighlightedID() != "mag" || len(ds.StatsCalls()) != n+1 {
		t.Fatalf("shown %q highlighted %q calls %v", p.Shown(), p.list.HighlightedID(), ds.StatsCalls())
	}
}

// test_format_change_does_not_resurrect_old_stats: a format change
// reformats in place; after a view change whose profile never arrived, the
// old numbers aren't redrawn.
func TestFormatChange(t *testing.T) {
	d, p, ds := setup(t)
	d.Env.State.Current = "psfFlux"
	d.Press("3")
	n := len(ds.Calls())
	d.Env.State.Formats["psfFlux"] = fmtx.Override{Digits: 2, Set: true}
	before := p.summary
	d.Send(kit.FormatChangedMsg{Column: "psfFlux"})
	if len(ds.Calls()) != n || &p.summary[0] == &before[0] {
		t.Fatal("not reformatted in place")
	}
	// a new view whose profile was cancelled
	p.gen++
	p.stale = true
	before = p.summary
	d.Send(kit.FormatChangedMsg{Column: "psfFlux"})
	if &p.summary[0] != &before[0] {
		t.Fatal("old numbers redrawn under a new view")
	}
}

// The summary's two columns and the head line.
func TestSummaryLayout(t *testing.T) {
	d, p, _ := setup(t)
	d.Env.State.Current = "psfFlux"
	d.Press("3")
	head := p.head[0].Plain
	if !strings.HasPrefix(head, "psfFlux   ") || !strings.Contains(head, "Point Source") && !strings.Contains(head, "flux") {
		t.Errorf("head %q", head)
	}
	var lines []string
	for _, l := range p.summary {
		lines = append(lines, l.Plain)
	}
	if lines[0] != "" {
		t.Errorf("first summary line %q", lines[0])
	}
	labels := [][2]string{{"rows", "p1"}, {"nulls", "p5"}, {"NaN", "p25"}, {"distinct", "p50"}, {"min", "p75"},
		{"max", "p95"}, {"mean", "p99"}, {"std", ""}}
	for i, l := range labels {
		f := strings.Fields(lines[i+1])
		if f[0] != l[0] || l[1] != "" && !contains(f, l[1]) {
			t.Errorf("row %d %q, want %v", i, lines[i+1], l)
		}
	}
	if !strings.Contains(lines[4], "≈19,938") || !strings.Contains(lines[4], "median") {
		t.Errorf("distinct row %q", lines[4])
	}
	if !strings.Contains(lines[3], "36") {
		t.Errorf("NaN row %q", lines[3])
	}
	// the columns line up: every row is the same width
	w := len([]rune(lines[1]))
	for _, l := range lines[1:] {
		if len([]rune(l)) != w {
			t.Errorf("ragged grid:\n%s", strings.Join(lines, "\n"))
			break
		}
	}
	// psfFlux has a histogram, not the most frequent values
	if p.plot == nil || strings.Contains(strings.Join(lines, "\n"), "most frequent") {
		t.Fatal("no histogram")
	}
	if p.plot[1].Plain != "distribution" || p.plot[len(p.plot)-1].Plain != "60 bins" {
		t.Errorf("plot head %q foot %q", p.plot[1].Plain, p.plot[len(p.plot)-1].Plain)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// Few distinct values: the most frequent, with bars and shares.
func TestTopValues(t *testing.T) {
	d, p, ds := setup(t)
	d.Env.State.Current = "isDipole"
	d.Press("3")
	for _, c := range ds.Calls() {
		if c.Method == "hist" {
			t.Fatal("histogram for a boolean column")
		}
	}
	var lines []string
	for _, l := range p.summary {
		lines = append(lines, l.Plain)
	}
	s := strings.Join(lines, "\n")
	i := strings.Index(s, "\nmost frequent\n")
	if i < 0 || !strings.HasSuffix(s[:i], "\n") {
		t.Fatalf("no most frequent after a blank line:\n%s", s)
	}
	top := strings.Split(s[i+len("\nmost frequent\n"):], "\n")
	if len(top) != 2 || !strings.Contains(top[0], strings.Repeat("█", 30)) || !strings.Contains(top[0], "18,952") {
		t.Fatalf("top rows %q", top)
	}
	if !strings.Contains(top[1], "1,048") || strings.Count(top[1], "█") != 1 {
		t.Fatalf("second row %q", top[1])
	}
	if !strings.Contains(s, "distinct           2") && !strings.Contains(s, "distinct  2") {
		// exact: no ≈
		if strings.Contains(s, "≈2") {
			t.Fatalf("distinct shown approximate:\n%s", s)
		}
	}
}

func TestBar(t *testing.T) {
	for _, c := range []struct {
		n, m int64
		want string
	}{{10, 10, strings.Repeat("█", 30)}, {5, 10, strings.Repeat("█", 15)}, {1, 1000, ""}, {1, 100, "▎"}, {0, 10, ""}} {
		if got := bar(c.n, c.m); got != c.want {
			t.Errorf("bar(%d, %d) = %q, want %q", c.n, c.m, got, c.want)
		}
	}
}

// l L [ ]: the histogram's settings, recomputed at once on Stats.
func TestHistogramKeys(t *testing.T) {
	d, p, ds := setup(t)
	d.Env.State.Current = "mag"
	d.Press("3")
	hist := func() data.HistOptions {
		var o data.HistOptions
		for _, c := range ds.Calls() {
			if c.Method == "hist" {
				o = c.Opts.(data.HistOptions)
			}
		}
		return o
	}
	if o := hist(); o.Bins != 60 || o.Log || o.Temporal {
		t.Fatalf("first histogram %+v", o)
	}
	d.Press("]")
	if hist().Bins != 90 {
		t.Fatalf("] gave %d bins", hist().Bins)
	}
	d.Press("[", "[")
	if hist().Bins != 40 {
		t.Fatalf("[ [ gave %d bins", hist().Bins)
	}
	for i := 0; i < 20; i++ {
		d.Press("[")
	}
	if hist().Bins != MinBins {
		t.Fatalf("lower limit %d", hist().Bins)
	}
	for i := 0; i < 30; i++ {
		d.Press("]")
	}
	if hist().Bins != MaxBins-1 && hist().Bins != MaxBins {
		t.Fatalf("upper limit %d", hist().Bins)
	}
	d.Press("L")
	if !hist().Log {
		t.Fatal("L didn't log the values")
	}
	d.Press("l")
	foot := p.plot[len(p.plot)-1].Plain
	if !strings.HasSuffix(foot, "bins  ·  log counts  ·  log values") {
		t.Fatalf("foot %q", foot)
	}
	// a temporal column: binned on epoch seconds, never log values
	d.Press("down", "up") // (stay on Stats)
	d.Env.State.Current = "ingestTime"
	d.Send(kit.ColumnChangedMsg{From: "grid"})
	o := hist()
	if !o.Temporal || o.Log {
		t.Fatalf("temporal histogram %+v", o)
	}
	foot = p.plot[len(p.plot)-1].Plain
	if strings.Contains(foot, "log values") || !strings.Contains(foot, "log counts") {
		t.Fatalf("temporal foot %q", foot)
	}
}

// m: sampling on and off, with Python's notices; stats recompute.
func TestSampling(t *testing.T) {
	d, p, ds := setup(t)
	d.Env.State.Current = "mag"
	d.Press("3", "m")
	if !d.Env.State.Sampling || d.Count(kit.SamplingChangedMsg{}) != 1 {
		t.Fatal("m didn't turn sampling on")
	}
	calls := ds.Calls()
	if last := calls[len(calls)-1]; last.Sample.Rows != kit.SampleRows {
		t.Fatalf("last call %+v", last)
	}
	st := p.head[1].Plain
	if !strings.HasPrefix(st, "! Profiled mag, sampled") || !strings.HasSuffix(st, "all rows  ·  m scans everything") {
		t.Fatalf("status %q", st)
	}
	d.Press("m")
	n := d.Notices()
	if len(n) != 2 || !strings.HasPrefix(n[0], "! Sampling on · stats and plots use ~") ||
		n[1] != "✓ Sampling off · stats and plots scan every row" {
		t.Fatalf("notices %q", n)
	}
	if !strings.HasPrefix(p.head[1].Plain, "✓ Profiled mag") {
		t.Fatalf("status %q", p.head[1].Plain)
	}
}

// A failed profile shows the error; the head keeps "Profiling".
func TestError(t *testing.T) {
	d, p, ds := setup(t)
	ds.Err = errors.New("Binder Error: Referenced column \"zz\" not found in FROM clause!")
	d.Press("3")
	var status []string
	for _, m := range d.Msgs {
		if s, ok := m.(kit.StatusMsg); ok {
			status = append(status, s.Text)
		}
	}
	if len(status) != 1 || status[0] != `unknown column "zz"` {
		t.Fatalf("status %q", status)
	}
	if p.Shown() != "" || !strings.Contains(p.head[1].Plain, "Profiling diaSourceId") {
		t.Fatalf("shown %q head %q", p.Shown(), p.head[1].Plain)
	}
}

// The pane's screen: the body and the column list in its panel.
func TestView(t *testing.T) {
	d, _, _ := setup(t)
	d.Press("3")
	scr := d.Screen()
	_, y := d.Find("columns  16")
	if y < 0 {
		t.Fatalf("no list panel:\n%s", text(scr))
	}
	if !strings.Contains(scr[y+1], "│ diaSourceId         ") {
		t.Fatalf("first item %q", scr[y+1])
	}
	_, hy := d.Find("✓ Profiled diaSourceId")
	if hy < 0 {
		t.Fatalf("no status:\n%s", text(scr))
	}
	// a click on an item highlights it
	x, iy := d.Find("snr ")
	d.Click(x, iy)
	if d.Env.State.Current != "snr" {
		t.Fatalf("click: current %q", d.Env.State.Current)
	}
	for _, l := range scr {
		if w := len([]rune(l)); w > 150 {
			t.Fatalf("line too wide (%d): %q", w, l)
		}
	}
}

func TestEpochLabel(t *testing.T) {
	if got := epochLabel(1748736602.824); got != "2025-06-01 00:10" {
		t.Fatalf("epochLabel = %q", got)
	}
	if got := intText(170000000000009920); got != "170000000000009920" {
		t.Fatalf("intText = %q", got)
	}
	if got := intText(1234.5); got != "1,234" {
		t.Fatalf("intText = %q", got)
	}
}

// The column list is its own panel beside the profile's, one column apart,
// with the accent border while Stats has focus.
func TestPanels(t *testing.T) {
	d, p, _ := setup(t)
	d.Press("3")
	ps := p.Panels(150, 30)
	if len(ps) != 2 || ps[1].X != 150-SideWidth || ps[0].W != 150-SideWidth-1 || !ps[1].Focused || ps[0].Focused {
		t.Fatalf("panels %+v", ps)
	}
	scr := d.Screen()
	x, y := d.Find("╭─ columns  16")
	if y < 0 || x != 150-SideWidth || !strings.HasSuffix(scr[y][:strings.Index(scr[y], "╭─ columns")], "╮ ") {
		t.Fatalf("list panel at %d,%d:\n%s", x, y, text(scr))
	}
	if r := string([]rune(scr[y+1])[x:]); !strings.HasPrefix(r, "│ diaSourceId ") {
		t.Fatalf("first item %q", r)
	}
	// narrow: the profile alone
	if ps := p.Panels(40, 20); len(ps) != 1 {
		t.Fatalf("narrow panels %d", len(ps))
	}
}
