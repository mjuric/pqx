// Package detail is the Data tab's details pane (Python pqx's DetailList and
// the app's detail pane code): every column of the record under the grid's
// cursor as "name  value", values at full precision with their unit and a
// derived reading (UTC for MJDs, h:m:s for RA, …). Its selection follows the
// grid's column, and moving it moves the grid; the grid's cell keys
// (= y i F < >) act on the selected field.
//
// The pane shows every column, so it reads the ones the grid hasn't loaded
// yet in the background (task "detail", after a short wait for the cursor to
// settle) and merges them into the grid's cache (kit.RecordSource.Merge),
// where the grid uses them too.
package detail

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/cells"
	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Part is the pane's name for focus and ColumnChangedMsg.
const Part = "detail"

const (
	// nameMax is the widest the name column gets (PR #24: names up to 22,
	// a gap of 2, values 24 in the 53-wide pane with its scrollbar).
	nameMax = 22
	// cutAt: a longer value (a JSON document, a blob) is cut, so it doesn't
	// bury every other column.
	cutAt = 300
	// fetchDelay is how long the pane waits for the cursor to settle before
	// reading the columns it lacks (Python's DETAIL_FETCH_DELAY).
	fetchDelay = 100 * time.Millisecond
	// wheelStep is the lines a notch of the wheel scrolls (Textual's
	// scroll sensitivity).
	wheelStep = 2
	// failedNoticeTime is how long "✗ Columns" shows.
	failedNoticeTime = 6 * time.Second
)

// Grid is the grid as the pane uses it: kit.RecordSource and the grid's
// part of the pane's work (internal/ui/grid implements it).
type Grid interface {
	kit.RecordSource
	// DetailRead names what the background read is to bring: the view
	// (with its generation), the file rows of the rows read near the
	// screen and the columns some of them lack. ok is false if there is
	// nothing to read. The caller runs the read under the tag "detail"
	// right away; the grid leaves those cells to it.
	DetailRead() (view kit.View, fileRows []int64, cols []string, ok bool)
	// FieldKey runs the grid's own action for k (y, F, <, >, x, ctrl+x)
	// on column name of the record under the cursor.
	FieldKey(name string, k tea.KeyPressMsg) tea.Cmd
	// FieldValue calls fn with the value of column name of the record
	// under the cursor, reading it first if need be.
	FieldValue(name string, fn func(v data.Value) tea.Cmd) tea.Cmd
}

// entry is one column of the record.
type entry struct {
	name  string      // the column's name, as in the file
	label string      // the name as shown (sanitized)
	value styled.Text // value, unit, derived reading
}

// Pane is the details pane. It implements kit.Pane, kit.Framed and
// kit.Focusable.
type Pane struct {
	env  *kit.Env
	st   *kit.State
	grid Grid // nil if the grid doesn't offer the pane's API

	focused bool
	entries []entry
	nameW   int
	sel     int // the selected entry, -1 for none
	top     int // the first line shown
	title   styled.Text
	rec     kit.Record

	// the layout of the entries at the content width layW: each one's
	// first line and height; total lines
	layW   int
	starts []int
	hts    []int
	total  int
	bar    bool // a scrollbar shows (the content is a column narrower)
	w, h   int  // the size drawn last

	seq  int    // debounce generation of the background read
	last string // what the last background read asked for (readKey)
}

// New makes the details pane over env; its grid is env.Grid.
func New(env *kit.Env) *Pane {
	p := &Pane{env: env, st: env.State, sel: -1, h: 20, w: 49}
	if g, ok := env.Grid.(Grid); ok {
		p.grid = g
	}
	p.title = p.dim("no rows")
	return p
}

func (p *Pane) dim(s string) styled.Text { return styled.New(s, p.env.Look.Style("dim")) }

// Title implements kit.Framed: the record's row and file row.
func (p *Pane) Title() styled.Text { return p.title }

// Subtitle implements kit.Framed.
func (p *Pane) Subtitle() styled.Text { return styled.Text{} }

// Focus implements kit.Focusable.
func (p *Pane) Focus() tea.Cmd { p.focused = true; return nil }

// Blur implements kit.Focusable.
func (p *Pane) Blur() { p.focused = false }

// Keys implements kit.Pane (Python's KEYS["detail"]).
func (p *Pane) Keys() []kit.KeyHint {
	return []kit.KeyHint{{Key: "↑↓", Help: "column"}, {Key: "=", Help: "match"}, {Key: "y", Help: "copy"},
		{Key: "i", Help: "stats"}, {Key: "esc", Help: "close"}, {Key: "?", Help: "help"}, {Key: "q", Help: "quit"},
		{Key: "tab", Help: "grid"}}
}

// Focused reports whether the pane has focus.
func (p *Pane) Focused() bool { return p.focused }

// Selected is the column of the selected entry, "" for none.
func (p *Pane) Selected() string {
	if p.sel >= 0 && p.sel < len(p.entries) {
		return p.entries[p.sel].name
	}
	return ""
}

// Names are the entries' columns, in order.
func (p *Pane) Names() []string {
	out := make([]string, len(p.entries))
	for i, e := range p.entries {
		out[i] = e.name
	}
	return out
}

// Value is the text shown for column name (lines joined by "\n"), and
// whether there is an entry for it.
func (p *Pane) Value(name string) (string, bool) {
	for _, e := range p.entries {
		if e.name == name {
			return e.value.Plain, true
		}
	}
	return "", false
}

// Top is the first line shown (the scroll position).
func (p *Pane) Top() int { return p.top }

type fetchTick struct {
	p   *Pane
	seq int
}

// fetched is the background read's result.
type fetched struct {
	p        *Pane
	view     kit.View
	fileRows []int64
	cols     []string
	w        data.Window
	err      error
}

// Update implements kit.Pane.
func (p *Pane) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return p.onKey(msg)
	case tea.MouseClickMsg:
		return p.onClick(msg.Mouse())
	case tea.MouseWheelMsg:
		switch msg.Mouse().Button {
		case tea.MouseWheelUp:
			p.scroll(-wheelStep)
		case tea.MouseWheelDown:
			p.scroll(wheelStep)
		}
		return nil
	case fetchTick:
		if msg.p != p || msg.seq != p.seq {
			return nil
		}
		return p.fetch()
	case kit.DoneMsg:
		if r, ok := msg.Msg.(fetched); ok && r.p == p {
			return tea.Batch(p.onFetched(r), p.refresh())
		}
		return p.refresh() // the grid's reads have landed
	case kit.ColumnChangedMsg:
		if msg.From != Part {
			p.follow()
		}
		return nil
	case kit.ViewChangedMsg:
		p.env.Tasks.Cancel("detail") // for a view that is gone
		return p.refresh()
	case kit.CancelledMsg:
		p.last = "" // Esc stopped the read: the next move reads again
		return nil
	case kit.ToggleDetailMsg, kit.CursorMsg, kit.TotalMsg, kit.ColumnsChangedMsg:
		return p.refresh()
	}
	return nil
}

// refresh shows the grid's current record (Python's _update_detail) and,
// if columns are missing, starts the wait before reading them.
func (p *Pane) refresh() tea.Cmd {
	if !p.st.DetailOpen || p.env.Grid == nil {
		return nil
	}
	rec := p.env.Grid.Record()
	p.rec = rec
	if rec.Row < 0 {
		p.title = p.dim("no rows")
		p.setEntries(nil)
		return nil
	}
	switch {
	case rec.Pending:
		p.title = p.dim("finding record… · file row " + commas(rec.FileRow))
	case rec.FileRow >= 0:
		p.title = p.dim("row " + commas(rec.Row) + " · file row " + commas(rec.FileRow))
	default:
		p.title = p.dim("row " + commas(rec.Row))
	}
	p.setEntries(p.build(rec))
	p.follow()
	if len(rec.Missing) > 0 && !rec.Pending && p.grid != nil {
		p.seq++
		seq := p.seq
		return tea.Tick(fetchDelay, func(time.Time) tea.Msg { return fetchTick{p, seq} })
	}
	return nil
}

// build makes the entries for the columns shown, in order.
func (p *Pane) build(rec kit.Record) []entry {
	dim := p.env.Look.Style("dim")
	missing := map[string]bool{}
	for _, n := range rec.Missing {
		missing[n] = true
	}
	units := map[string]string{}
	for _, c := range p.env.DS.Columns() { // the file's units (Python's ds._by_name)
		if _, ok := units[c.Name]; !ok {
			units[c.Name] = c.Unit
		}
	}
	cols := p.shown()
	out := make([]entry, 0, len(cols))
	for _, c := range cols {
		e := entry{name: c.Name, label: fmtx.Sanitize(c.Name, false)}
		v, have := rec.Values[c.Name]
		_, failed := rec.Failed[c.Name]
		switch {
		case failed:
			e.value = styled.New(cells.FailedMark, styled.Style{Fg: "red"})
			e.value.Append(" couldn't load", dim)
		case !have || missing[c.Name]:
			e.value = styled.New(cells.Placeholder, dim)
		default:
			e.value = p.valueText(c, units[c.Name], v)
		}
		out = append(out, e)
	}
	return out
}

// shown are the view's columns the grid shows (all of them if every one
// is hidden, as the grid does).
func (p *Pane) shown() []data.Column {
	var out []data.Column
	for _, c := range p.st.Columns {
		if !p.st.Hidden[c.Name] {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return p.st.Columns
	}
	return out
}

// valueText is a value as the pane shows it: in full, its unit after it
// and a derived reading below it, dim.
func (p *Pane) valueText(c data.Column, unit string, v data.Value) styled.Text {
	dim := p.env.Look.Style("dim")
	kind := fmtx.KindFor(c.Name, c.Arrow, c.Unit)
	full := fmtx.Format(v, kind, fmtx.Opts{Raw: true})
	if n := utf8.RuneCountInString(full); n > cutAt {
		full = string([]rune(full)[:cutAt]) + "… (" + commas(int64(n)) + " chars; y copies it all)"
	}
	var t styled.Text
	if v == nil {
		t = styled.New(full, dim)
	} else {
		t = styled.New(full, styled.Style{})
	}
	if unit != "" && v != nil {
		t.Append("  "+fmtx.Sanitize(unit, false), dim)
	}
	if extra := fmtx.Derived(c.Name, kind, v, unit); extra != "" {
		t.Append("\n· "+fmtx.Sanitize(extra, false), dim)
	}
	return t
}

// setEntries shows entries (DetailList.set_entries): with the same columns
// as before the scroll position and the selection stay; otherwise the list
// starts over.
func (p *Pane) setEntries(es []entry) {
	same := len(es) == len(p.entries)
	for i := 0; same && i < len(es); i++ {
		same = es[i].name == p.entries[i].name
	}
	p.entries = es
	nw := 0
	for _, e := range es {
		nw = max(nw, utf8.RuneCountInString(e.name))
	}
	p.nameW = min(nameMax, nw)
	p.layW = -1
	if !same {
		p.sel, p.top = -1, 0
	}
}

// follow selects the current column (the grid's), scrolling it into view
// if the selection moves; a column the pane doesn't show leaves it put.
func (p *Pane) follow() {
	for i, e := range p.entries {
		if e.name == p.st.Current {
			p.selectEntry(i)
			return
		}
	}
	if p.sel < 0 && len(p.entries) > 0 {
		p.selectEntry(0)
	}
}

// selectEntry selects entry i, scrolling it into view if the selection
// changes.
func (p *Pane) selectEntry(i int) {
	if i == p.sel || i < 0 || i >= len(p.entries) {
		return
	}
	p.sel = i
	p.layout(p.w)
	p.scrollTo(i)
}

// moveTo is the user's move to entry i: the grid follows (Python's
// detail_highlighted; only the user's own moves drive the grid).
func (p *Pane) moveTo(i int) tea.Cmd {
	if len(p.entries) == 0 {
		return nil
	}
	i = max(0, min(len(p.entries)-1, i))
	p.layout(p.w)
	p.sel = i
	p.scrollTo(i)
	name := p.entries[i].name
	if name == p.st.Current {
		return nil
	}
	p.st.Current = name
	return kit.Send(kit.ColumnChangedMsg{From: Part})
}

// layout measures the entries for a pane w wide: with a scrollbar if they
// don't fit its height, a column narrower.
func (p *Pane) layout(w int) {
	if w == p.layW+btoi(p.bar) && p.layW >= 0 {
		return
	}
	p.bar = false
	p.measure(w)
	if p.total > p.h && w > 1 {
		p.bar = true
		p.measure(w - 1)
	}
	p.top = max(0, min(p.top, p.total-p.h))
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (p *Pane) measure(cw int) {
	p.layW = cw
	p.starts = make([]int, len(p.entries))
	p.hts = make([]int, len(p.entries))
	line := 0
	for i, e := range p.entries {
		p.starts[i] = line
		p.hts[i] = entryHeight(e.value, p.nameW, cw)
		line += p.hts[i]
	}
	p.total = line
}

// scrollTo scrolls entry i into view (OptionList.scroll_to_highlight).
func (p *Pane) scrollTo(i int) {
	if i < 0 || i >= len(p.starts) {
		return
	}
	a, b := p.starts[i], p.starts[i]+p.hts[i]
	if b > p.top+p.h {
		p.top = b - p.h
	}
	if a < p.top {
		p.top = a
	}
	p.top = max(0, min(p.top, p.total-p.h))
}

// scroll scrolls d lines without moving the selection (the wheel).
func (p *Pane) scroll(d int) {
	p.layout(p.w)
	p.top = max(0, min(p.top+d, p.total-p.h))
}

// lineAt is the entry on line y of the pane as drawn, -1 if none.
func (p *Pane) lineAt(y int) int {
	l := p.top + y
	if y < 0 || y >= p.h || l >= p.total {
		return -1
	}
	i := sort.Search(len(p.starts), func(i int) bool { return p.starts[i] > l }) - 1
	return i
}

func (p *Pane) onKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	switch s {
	case "up":
		return p.moveTo(p.sel - 1)
	case "down":
		return p.moveTo(p.sel + 1)
	case "home":
		return p.moveTo(0)
	case "end":
		return p.moveTo(len(p.entries) - 1)
	case "pgup", "pgdown":
		return p.page(map[string]int{"pgup": -1, "pgdown": 1}[s])
	case "enter", "tab":
		// back to the grid, on the selected column (the grid is on it)
		return kit.Send(kit.FocusMsg{Pane: "grid"})
	case "d":
		return kit.Send(kit.ToggleDetailMsg{})
	case "=":
		return p.equals()
	case "i":
		if name := p.Selected(); name != "" {
			return kit.Send(kit.ColumnStatsMsg{Column: name})
		}
	case "y", "F", "<", ">":
		if name := p.Selected(); name != "" && p.grid != nil {
			return p.grid.FieldKey(name, k)
		}
	case "x", "ctrl+x":
		// the app's clear-filter key, which hands focus to the grid
		if p.grid != nil && !p.st.View.Plain() {
			return tea.Batch(p.grid.FieldKey(p.Selected(), k), kit.Send(kit.FocusMsg{Pane: "grid"}))
		}
	}
	return nil
}

// page moves the selection a page of lines (OptionList._move_page).
func (p *Pane) page(d int) tea.Cmd {
	if len(p.entries) == 0 {
		return nil
	}
	if p.sel < 0 {
		if d < 0 {
			return p.moveTo(0)
		}
		return p.moveTo(len(p.entries) - 1)
	}
	p.layout(p.w)
	y := max(0, min(p.total-1, p.starts[p.sel]+d*p.h))
	i := sort.Search(len(p.starts), func(i int) bool { return p.starts[i] > y }) - 1
	return p.moveTo(i)
}

func (p *Pane) onClick(m tea.Mouse) tea.Cmd {
	if m.Button != tea.MouseLeft || (p.bar && m.X >= p.w-1) {
		return nil
	}
	p.layout(p.w)
	if i := p.lineAt(m.Y); i >= 0 {
		return p.moveTo(i)
	}
	return nil
}

// equals is "=" on the selected field: the view narrows to the records
// with its value, keeping this one (Python's _filter_value via
// action_detail_key).
func (p *Pane) equals() tea.Cmd {
	name := p.Selected()
	if name == "" {
		return nil
	}
	if p.st.View.IsSQL() {
		return kit.Send(kit.NotifyMsg{Severity: kit.Warning, Text: "= filtering works on the table, not on SQL results"})
	}
	rec := p.rec
	if _, bad := rec.Failed[name]; bad {
		return kit.Send(kit.NotifyMsg{Severity: kit.Error, Text: fmtx.Sanitize(name, false) + " couldn't be loaded for these rows",
			Timeout: 4 * time.Second})
	}
	view, keep := p.st.View, rec.FileRow
	if v, ok := rec.Values[name]; ok {
		return p.filterOn(name, v, view, keep)
	}
	if rec.Pending || p.grid == nil {
		return kit.Send(kit.NotifyMsg{Severity: kit.Warning, Text: "= not applied: the value isn't loaded yet", Timeout: 3 * time.Second})
	}
	// not loaded: the grid reads it, then the filter is made for the view
	// and record of now
	return p.grid.FieldValue(name, func(v data.Value) tea.Cmd { return p.filterOn(name, v, view, keep) })
}

func (p *Pane) filterOn(name string, v data.Value, view data.View, keep int64) tea.Cmd {
	sqlName := name
	if c, ok := p.st.Column(name); ok && c.SQLName != "" {
		sqlName = c.SQLName
	}
	cond := condition(sqlName, v)
	if cond == "" {
		return kit.Send(kit.NotifyMsg{Severity: kit.Warning, Text: "Can't filter on this value type"})
	}
	nv := data.View{Where: combine(view.Where, cond), OrderBy: view.OrderBy}
	return kit.Send(kit.SetViewMsg{View: nv, KeepFileRow: keep})
}

// fetch starts the background read of the columns the record lacks (with
// the rest of the rows read near the screen: Python's
// _fetch_detail_columns, once per page), unless one is running.
func (p *Pane) fetch() tea.Cmd {
	if !p.st.DetailOpen || p.grid == nil || p.env.Tasks.Running("detail") {
		return nil
	}
	view, fileRows, cols, ok := p.grid.DetailRead()
	if !ok {
		return nil
	}
	// the same read again would bring nothing new (a value the grid
	// couldn't take): once is enough
	key := readKey(view, fileRows, cols)
	if key == p.last {
		return nil
	}
	p.last = key
	ds := p.env.DS
	return p.env.Tasks.Run("detail", "loading columns", false, func(ctx context.Context) tea.Msg {
		w, err := ds.FetchColumns(ctx, fileRows, cols)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return fetched{p: p, view: view, fileRows: fileRows, cols: cols, w: w, err: err}
	})
}

// onFetched merges the read into the grid's cache; columns that couldn't
// be read are marked so there (✗, not read again) and said once.
func (p *Pane) onFetched(r fetched) tea.Cmd {
	if errors.Is(r.err, context.Canceled) {
		p.last = "" // read again on the next move
		return nil
	}
	w := r.w
	if r.err != nil {
		w = data.Window{FileRows: r.fileRows, Len: len(r.fileRows), Failed: map[string]error{}}
		for _, c := range r.cols {
			w.Failed[c] = r.err
		}
	}
	if w.FileRows == nil {
		w.FileRows = r.fileRows
	}
	p.env.Grid.Merge(r.view, w)
	if len(w.Failed) == 0 {
		return nil
	}
	names := make([]string, 0, len(w.Failed))
	for n := range w.Failed {
		names = append(names, n)
	}
	sort.Strings(names)
	s := "s"
	if len(names) == 1 {
		s = ""
	}
	why := w.Failed[names[0]].Error()
	if i := strings.IndexByte(why, '\n'); i >= 0 {
		why = why[:i]
	}
	if rs := []rune(why); len(rs) > 200 {
		why = string(rs[:200])
	}
	return kit.Send(kit.NotifyMsg{Severity: kit.Error, Title: "✗ Columns", Timeout: failedNoticeTime,
		Text: "Couldn't load " + strconv.Itoa(len(names)) + " column" + s + ": " + fmtx.Sanitize(why, false)})
}

// readKey identifies a background read.
func readKey(v kit.View, fileRows []int64, cols []string) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(v.Gen))
	for _, r := range fileRows {
		b.WriteString(",")
		b.WriteString(strconv.FormatInt(r, 10))
	}
	b.WriteString("|")
	b.WriteString(strings.Join(cols, "\x00"))
	return b.String()
}

// View implements kit.Pane.
func (p *Pane) View(w, h int) string {
	if w != p.w || h != p.h {
		p.w, p.h = w, h
		p.layW = -1
	}
	p.layout(w)
	cw := p.layW
	look := p.env.Look
	lines := make([]string, 0, h)
	var bar []styled.Text
	if p.bar {
		bar = scrollbar(h, p.total, p.top, look.Style("border"))
	}
	i := p.lineAt(0)
	for y := 0; y < h; {
		if i < 0 || i >= len(p.entries) {
			for ; y < h; y++ {
				lines = append(lines, p.withBar(strings.Repeat(" ", cw), bar, y))
			}
			break
		}
		e := p.entries[i]
		nameSt := styled.Style{Bold: true}
		whole := false
		if i == p.sel {
			if p.focused {
				whole = true
			} else {
				nameSt.Reverse = true
			}
		}
		el := entryLines(e.label, e.value, p.nameW, cw, nameSt, whole)
		skip := 0
		if y == 0 {
			skip = p.top - p.starts[i]
		}
		for _, l := range el[min(skip, len(el)):] {
			if y >= h {
				break
			}
			lines = append(lines, p.withBar(fit(look.Render(l), cw), bar, y))
			y++
		}
		i++
	}
	return strings.Join(lines, "\n")
}

func (p *Pane) withBar(line string, bar []styled.Text, y int) string {
	if bar == nil {
		return line
	}
	return line + p.env.Look.Render(bar[y])
}

// fit pads or cuts s (which may hold SGR sequences) to exactly w cells.
func fit(s string, w int) string {
	n := ansi.StringWidth(s)
	if n > w {
		return ansi.Truncate(s, w, "")
	}
	return s + strings.Repeat(" ", w-n)
}

// commas is n with thousands separators.
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
