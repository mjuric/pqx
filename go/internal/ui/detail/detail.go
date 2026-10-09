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
	// exactBar: up to this many entries all are measured, so the
	// scrollbar is exact; beyond, entries not drawn yet count as a line.
	exactBar = 500
	// failedNoticeTime is how long "✗ Columns" shows.
	failedNoticeTime = 6 * time.Second
)

// Grid is the grid as the pane uses it: kit.RecordSource and the grid's
// part of the pane's work (internal/ui/grid implements it).
type Grid interface {
	kit.RecordSource
	// DetailRead names what the background read is to bring: the view
	// (with its generation) and reads of file rows × columns, the cells
	// the rows read near the screen lack. ok is false if there is nothing
	// to read. The caller runs the reads under the tag "detail" right
	// away; the grid leaves those cells to it.
	DetailRead() (view kit.View, fileRows [][]int64, cols [][]string, ok bool)
	// FieldKey runs the grid's own action for k (=, y, F, <, >) on column
	// name of the record under the cursor.
	FieldKey(name string, k tea.KeyPressMsg) tea.Cmd
}

// state of an entry's value.
const (
	loaded = iota
	loading
	failed
)

// entry is one column of the record. Its text and height are worked out
// when needed: a record change costs what is on screen, not every column
// (Python's _LazyEntry).
type entry struct {
	name  string // the column's name, as in the file
	label string // the name as shown (sanitized)
	col   data.Column
	state int
	v     data.Value

	text *styled.Text // value, unit, derived reading, once made
	h    int          // lines at the pane's content width; 0 until measured
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
	title   styled.Text
	rec     kit.Record

	// the scroll position: the entry on the first line shown, and how many
	// of its lines are above it
	ti, to int

	cw   int  // the content width entries are measured at; -1: not laid out
	bar  bool // a scrollbar shows (the content is a column narrower)
	w, h int  // the size drawn last

	kinds map[string]fmtx.Kind // by column (fmtx.KindFor is slow)
	units map[string]string    // the file's units by column (Python's ds._by_name)

	seq  int    // debounce generation of the background read
	last string // what the last background read asked for (readKey)
}

// New makes the details pane over env; its grid is env.Grid.
func New(env *kit.Env) *Pane {
	p := &Pane{env: env, st: env.State, sel: -1, h: 20, w: 49, cw: -1, kinds: map[string]fmtx.Kind{},
		units: map[string]string{}}
	if g, ok := env.Grid.(Grid); ok {
		p.grid = g
	}
	for _, c := range env.DS.Columns() {
		if _, ok := p.units[c.Name]; !ok {
			p.units[c.Name] = c.Unit
		}
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
	for i := range p.entries {
		if p.entries[i].name == name {
			return p.textOf(i).Plain, true
		}
	}
	return "", false
}

// Top is the first line shown (the scroll position), measuring the
// entries above it (for tests).
func (p *Pane) Top() int {
	n := p.to
	for i := 0; i < p.ti; i++ {
		n += p.height(i)
	}
	return n
}

type fetchTick struct {
	p   *Pane
	seq int
}

// fetched is the background read's result.
type fetched struct {
	p        *Pane
	view     kit.View
	fileRows [][]int64
	cols     [][]string
	ws       []data.Window // by read; Failed holds a read's error for all its columns
	err      error         // set if the reads were stopped
}

// gridReads reports whether a task tag is one of the grid's reads, after
// which the record may have more values.
func gridReads(tag string) bool {
	switch tag {
	case "page", "cols", "locate":
		return true
	}
	return strings.HasPrefix(tag, "cell:")
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
			if errors.Is(r.err, context.Canceled) {
				p.last = "" // stopped: read again on the next move
				return nil
			}
			return tea.Batch(p.onFetched(r), p.refresh())
		}
		if gridReads(msg.Tag) {
			return p.refresh()
		}
		return nil
	case kit.ColumnChangedMsg:
		if msg.From != Part && !p.follow() && p.Selected() == p.st.Current {
			p.scrollTo(p.sel) // asked for again: shown even if scrolled away
		}
		return nil
	case kit.ViewChangedMsg:
		p.env.Tasks.Cancel("detail") // for a view that is gone
		p.kinds = map[string]fmtx.Kind{}
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
	// a selection in view stays in view, whatever the new values' heights
	shown := p.sel >= 0 && p.inView(p.sel)
	p.setEntries(p.build(rec))
	p.clamp() // the top entry may be shorter now
	if !p.follow() && shown {
		p.scrollTo(p.sel)
	}
	if len(rec.Missing) > 0 && !rec.Pending && p.grid != nil {
		p.seq++
		seq := p.seq
		return tea.Tick(fetchDelay, func(time.Time) tea.Msg { return fetchTick{p, seq} })
	}
	return nil
}

// build makes the entries for the columns shown, in order; their texts are
// made when drawn.
func (p *Pane) build(rec kit.Record) []entry {
	missing := make(map[string]bool, len(rec.Missing))
	for _, n := range rec.Missing {
		missing[n] = true
	}
	cols := p.shown()
	out := make([]entry, len(cols))
	for i, c := range cols {
		e := &out[i]
		e.name, e.col = c.Name, c
		if i < len(p.entries) && p.entries[i].name == c.Name {
			e.label = p.entries[i].label
		} else {
			e.label = fmtx.Sanitize(c.Name, false)
		}
		v, have := rec.Values[c.Name]
		_, bad := rec.Failed[c.Name]
		switch {
		case bad:
			e.state = failed
		case !have || missing[c.Name]:
			e.state = loading
		default:
			e.v = v
		}
	}
	return out
}

// shown are the view's columns the grid shows (all of them if every one
// is hidden, as the grid does).
func (p *Pane) shown() []data.Column {
	out := make([]data.Column, 0, len(p.st.Columns))
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

// textOf is entry i's text, made the first time it is needed.
func (p *Pane) textOf(i int) *styled.Text {
	e := &p.entries[i]
	if e.text != nil {
		return e.text
	}
	dim := p.env.Look.Style("dim")
	var t styled.Text
	switch e.state {
	case failed:
		t = styled.New(cells.FailedMark, styled.Style{Fg: "red"})
		t.Append(" couldn't load", dim)
	case loading:
		t = styled.New(cells.Placeholder, dim)
	default:
		t = p.valueText(e.col, e.v)
	}
	e.text = &t
	return e.text
}

// valueText is a value as the pane shows it: in full, its unit after it
// and a derived reading below it, dim.
func (p *Pane) valueText(c data.Column, v data.Value) styled.Text {
	dim := p.env.Look.Style("dim")
	kind, ok := p.kinds[c.Name]
	if !ok {
		kind = fmtx.KindFor(c.Name, c.Arrow, c.Unit)
		p.kinds[c.Name] = kind
	}
	unit := p.units[c.Name]
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
	nw := p.nameW
	if !same {
		nw = 0
		for _, e := range es {
			nw = max(nw, utf8.RuneCountInString(e.name))
		}
		nw = min(nameMax, nw)
	}
	p.entries, p.nameW = es, nw
	p.cw = -1
	if !same {
		p.sel, p.ti, p.to = -1, 0, 0
	}
}

// follow selects the current column (the grid's), scrolling it into view
// if the selection moves, and reports whether it moved; a column the pane
// doesn't show leaves it put.
func (p *Pane) follow() bool {
	for i := range p.entries {
		if p.entries[i].name == p.st.Current {
			return p.selectEntry(i)
		}
	}
	if p.sel < 0 && len(p.entries) > 0 {
		return p.selectEntry(0)
	}
	return false
}

// selectEntry selects entry i, scrolling it into view if the selection
// changes, and reports whether it did.
func (p *Pane) selectEntry(i int) bool {
	if i == p.sel || i < 0 || i >= len(p.entries) {
		return false
	}
	p.sel = i
	p.scrollTo(i)
	return true
}

// moveTo is the user's move to entry i: the grid follows (Python's
// detail_highlighted; only the user's own moves drive the grid).
func (p *Pane) moveTo(i int) tea.Cmd {
	if len(p.entries) == 0 {
		return nil
	}
	i = max(0, min(len(p.entries)-1, i))
	p.sel = i
	p.scrollTo(i)
	name := p.entries[i].name
	if name == p.st.Current {
		return nil
	}
	p.st.Current = name
	return kit.Send(kit.ColumnChangedMsg{From: Part})
}

// layout decides the content width for a pane w × h: a column narrower,
// for the scrollbar, if the entries don't fit its height (surely so if
// there are more entries than lines; otherwise they are measured).
func (p *Pane) layout() {
	w := max(1, p.w)
	if p.cw >= 0 && p.cw+btoi(p.bar) == w {
		return
	}
	p.bar = false
	p.setWidth(w)
	if w > 1 {
		if len(p.entries) > p.h {
			p.bar = true
		} else {
			total := 0
			for i := range p.entries {
				total += p.height(i)
			}
			p.bar = total > p.h
		}
		if p.bar {
			p.setWidth(w - 1)
			if len(p.entries) <= exactBar {
				for i := range p.entries {
					p.height(i) // all measured: the scrollbar's thumb is exact
				}
			}
		}
	}
}

func (p *Pane) setWidth(cw int) {
	p.cw = cw
	for i := range p.entries {
		p.entries[i].h = 0
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// height is entry i's lines, measured the first time it is needed.
func (p *Pane) height(i int) int {
	p.layout()
	e := &p.entries[i]
	if e.h == 0 {
		e.h = entryHeight(*p.textOf(i), p.nameW, p.cw)
	}
	return e.h
}

// inView reports whether entry i is wholly on screen.
func (p *Pane) inView(i int) bool {
	if i < p.ti || (i == p.ti && p.to > 0) || i >= len(p.entries) {
		return false
	}
	lines := -p.to
	for k := p.ti; k <= i; k++ {
		lines += p.height(k)
		if lines > p.h {
			return false
		}
	}
	return true
}

// scrollTo scrolls entry i into view, at the top if it is above the
// screen or taller than it, else at the bottom
// (OptionList.scroll_to_highlight).
func (p *Pane) scrollTo(i int) {
	if i < 0 || i >= len(p.entries) || p.inView(i) {
		return
	}
	if i < p.ti || (i == p.ti && p.to > 0) || p.height(i) >= p.h {
		p.ti, p.to = i, 0
		p.clamp()
		return
	}
	// its last line on the screen's last line
	k, lines := i, p.height(i)
	for k > 0 && lines+p.height(k-1) <= p.h {
		k--
		lines += p.height(k)
	}
	if lines < p.h && k > 0 {
		p.ti, p.to = k-1, p.height(k-1)-(p.h-lines)
	} else {
		p.ti, p.to = k, 0
	}
}

// clamp keeps the scroll position on the entries, with no blank lines
// below the last one unless they all fit.
func (p *Pane) clamp() {
	n := len(p.entries)
	if n == 0 {
		p.ti, p.to = 0, 0
		return
	}
	p.ti = max(0, min(p.ti, n-1))
	p.to = max(0, min(p.to, p.height(p.ti)-1))
	lines := -p.to
	for k := p.ti; k < n && lines < p.h; k++ {
		lines += p.height(k)
	}
	if lines >= p.h {
		return
	}
	// the end shows: scroll back so it is on the last line
	for p.to > 0 && lines < p.h {
		p.to--
		lines++
	}
	for p.ti > 0 && lines < p.h {
		p.ti--
		hk := p.height(p.ti)
		if lines+hk <= p.h {
			lines += hk
			continue
		}
		p.to = hk - (p.h - lines)
		return
	}
}

// scroll scrolls d lines without moving the selection (the wheel).
func (p *Pane) scroll(d int) {
	if len(p.entries) == 0 {
		return
	}
	p.to += d
	for p.to < 0 && p.ti > 0 {
		p.ti--
		p.to += p.height(p.ti)
	}
	for p.ti < len(p.entries)-1 && p.to >= p.height(p.ti) {
		p.to -= p.height(p.ti)
		p.ti++
	}
	p.clamp()
}

// lineAt is the entry on line y of the pane as drawn, -1 if none.
func (p *Pane) lineAt(y int) int {
	if y < 0 || y >= p.h {
		return -1
	}
	l := p.to + y
	for i := p.ti; i < len(p.entries); i++ {
		if l < p.height(i) {
			return i
		}
		l -= p.height(i)
	}
	return -1
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
	case "pgup":
		return p.page(-1)
	case "pgdown":
		return p.page(1)
	case "enter", "tab":
		// back to the grid, on the selected column (the grid is on it)
		return kit.Send(kit.FocusMsg{Pane: "grid"})
	case "d":
		return kit.Send(kit.ToggleDetailMsg{})
	case "=", "i", "y", "F", "<", ">":
		name := p.Selected()
		if name == "" || p.grid == nil {
			return nil
		}
		// a record on its way (Pending): the key waits to act on it
		// (Python's _queue_for_keep)
		if p.rec.Pending && p.grid.QueueKey(k, name) {
			return nil
		}
		if s == "i" {
			return kit.Send(kit.ColumnStatsMsg{Column: name})
		}
		// the grid's own action on that column of the record: "=" builds
		// the filter there (internal/sqllit), adding to a view on its way
		return p.grid.FieldKey(name, k)
	case "x", "ctrl+x":
		// the app's clear-filter key: the filter part empties its box
		// (typed text too) and the view; focus goes to the grid (also when
		// there was nothing to clear: the pane can't see the box)
		return tea.Batch(kit.Send(kit.SetViewMsg{View: data.View{}, KeepFileRow: p.rec.FileRow}),
			kit.Send(kit.FocusMsg{Pane: "grid"}))
	}
	return nil
}

// page moves the selection a page of lines (OptionList._move_page): to the
// entry on the line a screen's height from the selection's first line.
func (p *Pane) page(d int) tea.Cmd {
	n := len(p.entries)
	if n == 0 {
		return nil
	}
	if p.sel < 0 {
		if d < 0 {
			return p.moveTo(0)
		}
		return p.moveTo(n - 1)
	}
	k, off := p.sel, p.h
	if d > 0 {
		for k < n-1 && off >= p.height(k) {
			off -= p.height(k)
			k++
		}
	} else {
		for off > 0 && k > 0 {
			k--
			off -= p.height(k)
		}
	}
	return p.moveTo(k)
}

func (p *Pane) onClick(m tea.Mouse) tea.Cmd {
	if m.Button != tea.MouseLeft {
		return nil
	}
	p.layout()
	if p.bar && m.X >= p.cw {
		return nil // the scrollbar
	}
	if i := p.lineAt(m.Y); i >= 0 {
		return p.moveTo(i)
	}
	return nil
}

// fetch starts the background read of the cells the rows near the screen
// lack (Python's _fetch_detail_columns, once per page), unless one runs.
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
		r := fetched{p: p, view: view, fileRows: fileRows, cols: cols}
		for i := range fileRows {
			w, err := ds.FetchColumns(ctx, fileRows[i], cols[i])
			if ctx.Err() != nil {
				return fetched{p: p, err: ctx.Err()}
			}
			if err != nil {
				// that read's columns failed; the others are read on
				w = data.Window{FileRows: fileRows[i], Len: len(fileRows[i]), Failed: map[string]error{}}
				for _, c := range cols[i] {
					w.Failed[c] = err
				}
			}
			r.ws = append(r.ws, w)
		}
		return r
	})
}

// onFetched merges the reads into the grid's cache; columns that couldn't
// be read are marked so there (✗, not read again in this view) and said.
func (p *Pane) onFetched(r fetched) tea.Cmd {
	failedCols := map[string]error{}
	for i, w := range r.ws {
		if w.FileRows == nil {
			w.FileRows = r.fileRows[i]
		}
		p.env.Grid.Merge(r.view, w)
		for n, err := range w.Failed {
			failedCols[n] = err
		}
	}
	if len(failedCols) == 0 {
		return nil
	}
	names := make([]string, 0, len(failedCols))
	for n := range failedCols {
		names = append(names, n)
	}
	sort.Strings(names)
	s := "s"
	if len(names) == 1 {
		s = ""
	}
	why := failedCols[names[0]].Error()
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
func readKey(v kit.View, fileRows [][]int64, cols [][]string) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(v.Gen))
	for i := range fileRows {
		b.WriteString(";")
		for _, r := range fileRows[i] {
			b.WriteString(strconv.FormatInt(r, 10))
			b.WriteString(",")
		}
		b.WriteString("|")
		b.WriteString(strings.Join(cols[i], "\x00"))
	}
	return b.String()
}

// View implements kit.Pane: the entries on screen are laid out, the
// others not.
func (p *Pane) View(w, h int) string {
	w, h = max(1, w), max(0, h)
	if w != p.w || h != p.h {
		p.w, p.h = w, h
		p.cw = -1
	}
	p.layout()
	p.clamp()
	cw := p.cw
	look := p.env.Look
	lines := make([]string, 0, h)
	var bar []styled.Text
	if p.bar {
		bar = p.scrollbar(look.Style("border"))
	}
	i, skip := p.ti, p.to
	for y := 0; y < h; {
		if i >= len(p.entries) {
			for ; y < h; y++ {
				lines = append(lines, p.withBar(strings.Repeat(" ", cw), bar, y))
			}
			break
		}
		e := &p.entries[i]
		nameSt := styled.Style{Bold: true}
		whole := false
		if i == p.sel {
			if p.focused {
				whole = true
			} else {
				nameSt.Reverse = true
			}
		}
		el := entryLines(e.label, *p.textOf(i), p.nameW, cw, nameSt, whole)
		for _, l := range el[min(skip, len(el)):] {
			if y >= h {
				break
			}
			lines = append(lines, p.withBar(fit(look.Render(l), cw), bar, y))
			y++
		}
		skip = 0
		i++
	}
	return strings.Join(lines, "\n")
}

// scrollbar is the scrollbar for the scroll position, the lines of entries
// not measured yet counted as one each.
func (p *Pane) scrollbar(st styled.Style) []styled.Text {
	total, top := 0, p.to
	for i := range p.entries {
		hi := p.entries[i].h
		if hi == 0 {
			hi = 1
		}
		total += hi
		if i < p.ti {
			top += hi
		}
	}
	return scrollbar(p.h, total, top, st)
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
		s = ansi.Truncate(s, w, "")
		n = ansi.StringWidth(s) // short of w if a wide character was at the edge
	}
	return s + strings.Repeat(" ", max(0, w-n))
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
