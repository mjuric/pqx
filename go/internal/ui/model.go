// Package ui is the terminal UI of the pqx Go prototype: a lazily loaded grid
// over a data.Dataset, a filter bar, a go-to-row prompt, a status line and a
// key bar, built on Bubble Tea v2.
package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mjuric/pqx/go/internal/data"
)

// maxColWidth caps a column's width in cells; longer values end in "…".
const maxColWidth = 24

// cacheLimit is the number of rows the cache keeps per view before it drops
// rows far from the screen.
const cacheLimit = 50_000

type focusKind int

const (
	focusGrid focusKind = iota
	focusFilter
	focusGoto
)

type msgKind int

const (
	msgNone msgKind = iota
	msgOK
	msgWarn
	msgErr
)

// fetchReq is one window read: rows [start, start+n) of view generation gen,
// for cols.
type fetchReq struct {
	id    int
	gen   int
	start int64
	n     int
	cols  []string
}

func (r fetchReq) end() int64 { return r.start + int64(r.n) }

// covers reports whether r asks for at least what o asks for.
func (r fetchReq) covers(o fetchReq) bool {
	if r.gen != o.gen || r.start > o.start || r.end() < o.end() {
		return false
	}
	have := make(map[string]bool, len(r.cols))
	for _, c := range r.cols {
		have[c] = true
	}
	for _, c := range o.cols {
		if !have[c] {
			return false
		}
	}
	return true
}

type fetchMsg struct {
	req fetchReq
	win data.Window
	err error
}

type countMsg struct {
	gen int
	n   int64
	err error
}

type tickMsg struct{}

// viewData is what is known about one view: its row count and the cells
// fetched so far. A new filter makes a new viewData with a new gen; results
// for another gen are dropped.
type viewData struct {
	gen   int
	view  data.View
	total int64 // rows in the view; -1 while unknown (counting)
	known int64 // rows known to exist (a lower bound while total is -1)
	upper int64 // rows at or past upper are known not to exist; -1 if not known

	fileRow map[int64]int64             // view row -> file row
	cells   map[string]map[int64]string // column -> view row -> text
	colW    []int                       // column widths, grown as values arrive
	labelW  int                         // row-label width, grown likewise

	confirmed bool // a fetch of this view has succeeded
}

func newViewData(gen int, v data.View, cols []data.Column) *viewData {
	d := &viewData{
		gen: gen, view: v, total: -1, upper: -1,
		fileRow: map[int64]int64{},
		cells:   map[string]map[int64]string{},
		colW:    make([]int, len(cols)),
		labelW:  1,
	}
	for i, c := range cols {
		d.colW[i] = min(maxColWidth, max(1, textWidth(c.Name), textWidth(c.Type)))
	}
	return d
}

// lastRow is the last row the cursor may go to.
func (d *viewData) lastRow() int64 {
	if d.total >= 0 {
		return max(d.total-1, 0)
	}
	return max(d.known-1, 0)
}

// limit is the exclusive end of the rows that may exist, or -1 if unknown.
func (d *viewData) limit() int64 {
	if d.total >= 0 {
		return d.total
	}
	return d.upper
}

// saved is a view and the cursor on it, kept so a filter that fails on its
// first read can be undone.
type saved struct {
	v            *viewData
	curRow, top  int64
	curCol, left int
}

// Model is the Bubble Tea model.
type Model struct {
	ds     data.Dataset
	name   string
	cols   []data.Column
	right  []bool // numeric columns are right aligned
	colIdx map[string]int

	v    *viewData
	prev *saved
	gen  int

	curRow, top  int64
	curCol, left int
	w, h         int

	inflight    *fetchReq
	fetchCancel context.CancelFunc
	fetchStart  time.Time
	failed      *fetchReq
	nextID      int

	counting    bool
	countCancel context.CancelFunc
	countStart  time.Time

	focus     focusKind
	filter    textinput.Model
	filterErr string
	history   []string
	histPos   int
	histDraft string
	gotoIn    textinput.Model
	hint      string
	hintSet   bool

	msg     string
	msgKind msgKind

	spin    int
	ticking bool
	// tick returns the command that schedules the next spinner frame; tests
	// replace it.
	tick func() tea.Cmd

	now func() time.Time
}

// New makes the model for an open dataset.
func New(ds data.Dataset) *Model {
	cols := ds.Columns()
	m := &Model{
		ds:     ds,
		name:   filepath.Base(ds.Path()),
		cols:   cols,
		right:  make([]bool, len(cols)),
		colIdx: make(map[string]int, len(cols)),
		now:    time.Now,
	}
	for i, c := range cols {
		m.right[i] = isNumericType(c.Type)
		m.colIdx[c.Name] = i
	}
	m.tick = func() tea.Cmd {
		return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
	}
	m.v = newViewData(0, data.View{}, cols)
	m.v.total = ds.NumRows()
	m.v.known = m.v.total
	m.filter = newInput("")
	m.gotoIn = newInput("1234, 1.5M, 50%, -1")
	m.hint = "SQL WHERE expression, e.g. x > 0"
	m.filter.Placeholder = m.hint
	return m
}

func newInput(placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = placeholder
	ti.SetVirtualCursor(false) // the terminal's own cursor; no blink timer
	s := textinput.Styles{}
	s.Focused.Placeholder = lipgloss.NewStyle().Faint(true)
	s.Blurred.Placeholder = lipgloss.NewStyle().Faint(true)
	s.Cursor.Shape = tea.CursorBar
	ti.SetStyles(s)
	km := textinput.DefaultKeyMap()
	km.Paste = key.NewBinding(key.WithDisabled()) // bracketed paste still works
	km.NextSuggestion = key.NewBinding(key.WithDisabled())
	km.PrevSuggestion = key.NewBinding(key.WithDisabled())
	ti.KeyMap = km
	return ti
}

func isNumericType(t string) bool {
	t = strings.ToUpper(t)
	for _, p := range []string{"TINYINT", "SMALLINT", "INTEGER", "BIGINT", "HUGEINT",
		"UTINYINT", "USMALLINT", "UINTEGER", "UBIGINT", "UHUGEINT", "FLOAT", "DOUBLE", "REAL", "DECIMAL"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// Init implements tea.Model. The first fetch waits for the window size.
func (m *Model) Init() tea.Cmd { return nil }

// layout: the filter box takes rows 0-2, the grid box starts at row 3 with
// its border, then two header rows, the body, its bottom border, and the
// status and key lines.
const (
	filterTop  = 0
	gridTop    = 3
	headerRows = 2
	bodyTop    = gridTop + 1 + headerRows
	chromeRows = bodyTop + 1 + 2 // grid bottom border, status, keys
)

func (m *Model) bodyH() int { return max(1, m.h-chromeRows) }

// innerW is the width inside the grid box (border and one cell of padding on
// each side).
func (m *Model) innerW() int { return max(1, m.w-4) }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.filter.SetWidth(max(1, m.w-12))
		m.gotoIn.SetWidth(max(1, m.w-20))
		m.scrollToCursor()
		return m, m.ensure()
	case tea.KeyPressMsg:
		return m, m.onKey(msg)
	case tea.PasteMsg:
		return m, m.toInput(msg)
	case tea.MouseClickMsg:
		return m, m.onClick(msg.Mouse())
	case tea.MouseWheelMsg:
		return m, m.onWheel(msg.Mouse())
	case fetchMsg:
		return m, m.onFetch(msg)
	case countMsg:
		return m, m.onCount(msg)
	case tickMsg:
		m.spin = (m.spin + 1) % len(spinner)
		if m.busy() {
			return m, m.tick()
		}
		m.ticking = false
		return m, nil
	}
	return m, nil
}

func (m *Model) busy() bool { return m.counting || m.inflight != nil }

// startTick starts the spinner unless it runs already.
func (m *Model) startTick() tea.Cmd {
	if m.ticking || !m.busy() {
		return nil
	}
	m.ticking = true
	return m.tick()
}

func (m *Model) setMsg(k msgKind, s string) { m.msgKind, m.msg = k, s }

func (m *Model) toInput(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch m.focus {
	case focusFilter:
		m.filter, cmd = m.filter.Update(msg)
		m.filterErr = ""
	case focusGoto:
		m.gotoIn, cmd = m.gotoIn.Update(msg)
	}
	return cmd
}

func (m *Model) onKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "ctrl+c" {
		return m.quit()
	}
	switch m.focus {
	case focusFilter:
		return m.onFilterKey(k, s)
	case focusGoto:
		return m.onGotoKey(k, s)
	}
	n := int64(m.bodyH())
	switch s {
	case "q":
		return m.quit()
	case "up":
		m.curRow--
	case "down":
		m.curRow++
	case "left":
		m.curCol--
	case "right":
		m.curCol++
	case "pgup":
		m.curRow -= n
		m.top -= n
	case "pgdown":
		m.curRow += n
		m.top += n
	case "home":
		m.curCol = 0
	case "end":
		m.curCol = len(m.cols) - 1
	case "ctrl+home":
		m.curRow = 0
	case "ctrl+end":
		if m.v.total < 0 {
			m.setMsg(msgWarn, "still counting rows: went to the last row loaded so far")
		}
		m.curRow = m.v.lastRow()
	case "/":
		return m.openFilter()
	case "x", "ctrl+x":
		return m.clearFilter()
	case "g":
		m.focus = focusGoto
		m.gotoIn.SetValue("")
		m.gotoIn.Focus()
		return nil
	case "esc":
		m.escape()
		return nil
	default:
		return nil
	}
	m.clampCursor()
	m.scrollToCursor()
	return m.ensure()
}

func (m *Model) quit() tea.Cmd {
	if m.fetchCancel != nil {
		m.fetchCancel()
	}
	if m.countCancel != nil {
		m.countCancel()
	}
	return tea.Quit
}

// escape cancels user-started work (the count); with nothing running it
// clears the message.
func (m *Model) escape() {
	if m.counting {
		m.countCancel()
		m.counting = false
		m.setMsg(msgWarn, "count cancelled; the row count is unknown")
		return
	}
	m.setMsg(msgNone, "")
}

func (m *Model) openFilter() tea.Cmd {
	m.focus = focusFilter
	m.filter.SetValue(m.v.view.Where)
	m.filter.CursorEnd()
	m.filter.Focus()
	m.histPos = len(m.history)
	return nil
}

func (m *Model) closeFilter() {
	m.focus = focusGrid
	m.filter.Blur()
	m.filterErr = ""
}

func (m *Model) onFilterKey(k tea.KeyPressMsg, s string) tea.Cmd {
	switch s {
	case "esc":
		if m.counting {
			m.escape()
			return nil
		}
		m.closeFilter()
		return nil
	case "enter":
		return m.applyFilter(m.filter.Value())
	case "ctrl+x":
		m.closeFilter()
		return m.clearFilter()
	case "up":
		if m.histPos > 0 {
			if m.histPos == len(m.history) {
				m.histDraft = m.filter.Value()
			}
			m.histPos--
			m.filter.SetValue(m.history[m.histPos])
			m.filter.CursorEnd()
		}
		return nil
	case "down":
		if m.histPos < len(m.history) {
			m.histPos++
			if m.histPos == len(m.history) {
				m.filter.SetValue(m.histDraft)
			} else {
				m.filter.SetValue(m.history[m.histPos])
			}
			m.filter.CursorEnd()
		}
		return nil
	}
	return m.toInput(k)
}

func (m *Model) onGotoKey(k tea.KeyPressMsg, s string) tea.Cmd {
	switch s {
	case "esc":
		m.focus = focusGrid
		m.gotoIn.Blur()
		return nil
	case "enter":
		m.focus = focusGrid
		m.gotoIn.Blur()
		return m.goTo(m.gotoIn.Value())
	}
	return m.toInput(k)
}

// goTo moves the cursor to the row the user typed after g.
func (m *Model) goTo(spec string) tea.Cmd {
	if strings.TrimSpace(spec) == "" {
		return nil
	}
	total := m.v.total
	unknown := total < 0
	if unknown {
		t := strings.TrimSpace(spec)
		if strings.HasSuffix(t, "%") || strings.HasPrefix(t, "-") {
			m.setMsg(msgWarn, "the row count isn't known yet: wait for the count, or give a row number")
			return nil
		}
		total = 1 << 62
	}
	r, err := ParseRowSpec(spec, total)
	if err != nil {
		m.setMsg(msgErr, err.Error())
		return nil
	}
	if unknown && r > m.v.lastRow() {
		m.setMsg(msgWarn, "still counting rows: went to the last row loaded so far")
	}
	m.curRow = r
	m.clampCursor()
	// put the target in the middle of the screen, as a jump lands
	m.top = m.curRow - int64(m.bodyH()/2)
	m.scrollToCursor()
	return m.ensure()
}

func (m *Model) applyFilter(text string) tea.Cmd {
	where := strings.TrimSpace(text)
	if where == "" {
		m.closeFilter()
		return m.clearFilter()
	}
	if err := m.ds.CheckWhere(where); err != nil {
		m.filterErr = err.Error()
		return nil
	}
	if len(m.history) == 0 || m.history[len(m.history)-1] != where {
		m.history = append(m.history, where)
	}
	m.closeFilter()
	if where == m.v.view.Where {
		return nil
	}
	return m.setView(data.View{Where: where})
}

func (m *Model) clearFilter() tea.Cmd {
	if m.v.view.Plain() {
		return nil
	}
	m.prev = nil
	m.setMsg(msgNone, "")
	return m.setView(data.View{})
}

// setView switches to a new view: its cache starts empty, the cursor goes to
// the top row (keeping the column), a filtered view starts its count.
func (m *Model) setView(v data.View) tea.Cmd {
	m.cancelFetch()
	if m.counting {
		m.countCancel()
		m.counting = false
	}
	if !v.Plain() {
		m.prev = &saved{v: m.v, curRow: m.curRow, top: m.top, curCol: m.curCol, left: m.left}
	}
	m.gen++
	m.v = newViewData(m.gen, v, m.cols)
	m.failed = nil
	m.curRow, m.top = 0, 0
	var cmds []tea.Cmd
	if v.Plain() {
		m.v.total = m.ds.NumRows()
		m.v.known = m.v.total
		m.v.confirmed = true
		m.setMsg(msgNone, "")
	} else {
		cmds = append(cmds, m.startCount())
	}
	m.scrollToCursor()
	cmds = append(cmds, m.ensure(), m.startTick())
	return tea.Batch(cmds...)
}

func (m *Model) startCount() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.counting, m.countCancel, m.countStart = true, cancel, m.now()
	ds, view, gen := m.ds, m.v.view, m.v.gen
	m.setMsg(msgNone, "")
	return func() tea.Msg {
		n, err := ds.Count(ctx, view)
		return countMsg{gen: gen, n: n, err: err}
	}
}

func (m *Model) onCount(msg countMsg) tea.Cmd {
	if msg.gen != m.v.gen || !m.counting {
		return nil // an old view's count, or one cancelled with Esc
	}
	m.counting = false
	m.countCancel()
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return nil
		}
		if !m.v.confirmed {
			return m.revert(msg.err)
		}
		m.setMsg(msgErr, "count failed: "+msg.err.Error())
		return nil
	}
	m.v.total = msg.n
	m.v.known = msg.n
	if msg.n == 0 {
		m.setMsg(msgWarn, "no matching rows  → x clears the filter")
	} else {
		pct := 100 * float64(msg.n) / float64(max(m.ds.NumRows(), 1))
		m.setMsg(msgOK, fmt.Sprintf("%s rows match (%.3g%% of %s)", commas(msg.n), pct, commas(m.ds.NumRows())))
	}
	m.clampCursor()
	m.scrollToCursor()
	return m.ensure()
}

// revert goes back to the view before a filter that failed on its first read,
// and reopens the filter bar with the error.
func (m *Model) revert(err error) tea.Cmd {
	where := m.v.view.Where
	m.cancelFetch()
	if m.counting {
		m.countCancel()
		m.counting = false
	}
	p := m.prev
	m.prev = nil
	if p == nil {
		m.setMsg(msgErr, err.Error())
		return nil
	}
	m.v = p.v
	m.curRow, m.top, m.curCol, m.left = p.curRow, p.top, p.curCol, p.left
	m.failed = nil
	m.setMsg(msgErr, "query failed: "+err.Error()+"  · previous view kept")
	m.focus = focusFilter
	m.filter.SetValue(where)
	m.filter.CursorEnd()
	m.filter.Focus()
	m.histPos = len(m.history)
	m.filterErr = err.Error()
	var cmds []tea.Cmd
	if !m.v.view.Plain() && m.v.total < 0 {
		cmds = append(cmds, m.startCount())
	}
	cmds = append(cmds, m.ensure(), m.startTick())
	return tea.Batch(cmds...)
}

func (m *Model) clampCursor() {
	m.curRow = max(0, min(m.curRow, m.v.lastRow()))
	m.curCol = max(0, min(m.curCol, len(m.cols)-1))
}

// scrollToCursor moves the viewport so the cursor cell is on screen.
func (m *Model) scrollToCursor() {
	n := int64(m.bodyH())
	if m.curRow < m.top {
		m.top = m.curRow
	}
	if m.curRow >= m.top+n {
		m.top = m.curRow - n + 1
	}
	// don't leave the bottom of the screen empty when the end is known
	if lim := m.v.limit(); lim >= 0 {
		m.top = min(m.top, max(0, lim-n))
	}
	m.top = max(0, m.top)

	if m.curCol < m.left {
		m.left = m.curCol
	}
	for m.left < m.curCol {
		x := m.labelW() + 1
		fits := false
		for i := m.left; i < len(m.cols); i++ {
			x += m.v.colW[i] + 2
			if i == m.curCol {
				fits = x <= m.innerW()
				break
			}
		}
		if fits {
			break
		}
		m.left++
	}
	m.left = max(0, min(m.left, max(len(m.cols)-1, 0)))
}

func (m *Model) labelW() int { return m.v.labelW }

// slot is one column on screen: its index, x within the inner area, and the
// width of its text (w) and of the whole slot including padding (sw).
type slot struct {
	col, x, w, sw int
	clipped       bool
}

// layout places the columns from m.left that fit in the inner width; the last
// one may be clipped.
func (m *Model) layout() []slot {
	inner := m.innerW()
	x := m.labelW() + 1
	var out []slot
	for i := m.left; i < len(m.cols) && x < inner; i++ {
		w := m.v.colW[i]
		sw := w + 2
		s := slot{col: i, x: x, w: w, sw: sw}
		if x+sw > inner {
			s.sw = inner - x
			s.w = max(0, s.sw-2)
			s.clipped = true
		}
		out = append(out, s)
		x += sw
	}
	return out
}

// ensure starts a fetch when cells near the screen are missing from the
// cache: the rows on screen plus a margin, only the columns on screen. A
// fetch already running that covers them is left alone; otherwise it is
// cancelled and replaced.
func (m *Model) ensure() tea.Cmd {
	if m.w == 0 || len(m.cols) == 0 {
		return nil
	}
	n := int64(m.bodyH())
	d := m.v
	lim := d.limit()
	clip := func(a, b int64) (int64, int64) {
		a = max(a, 0)
		if lim >= 0 {
			b = min(b, lim)
		}
		return a, b
	}
	// check rows within half a screen of the screen; fetch a screen each side
	ca, cb := clip(m.top-n/2, m.top+n+n/2)
	if ca >= cb {
		return nil
	}
	var cols []string
	for _, s := range m.layout() {
		name := m.cols[s.col].Name
		col := d.cells[name]
		for r := ca; r < cb; r++ {
			if _, ok := col[r]; !ok {
				cols = append(cols, name)
				break
			}
		}
	}
	if len(cols) == 0 {
		return nil
	}
	fa, fb := clip(m.top-n, m.top+2*n)
	req := fetchReq{gen: d.gen, start: fa, n: int(fb - fa), cols: cols}
	if m.inflight != nil && m.inflight.covers(req) {
		return nil
	}
	if m.failed != nil && m.failed.covers(req) {
		return nil // don't retry a read that just failed
	}
	m.cancelFetch()
	m.nextID++
	req.id = m.nextID
	ctx, cancel := context.WithCancel(context.Background())
	m.inflight, m.fetchCancel, m.fetchStart = &req, cancel, m.now()
	ds, view := m.ds, d.view
	fetch := func() tea.Msg {
		w, err := ds.Fetch(ctx, view, req.start, req.n, req.cols)
		return fetchMsg{req: req, win: w, err: err}
	}
	return tea.Batch(fetch, m.startTick())
}

func (m *Model) cancelFetch() {
	if m.fetchCancel != nil {
		m.fetchCancel()
	}
	m.inflight, m.fetchCancel = nil, nil
}

func (m *Model) onFetch(msg fetchMsg) tea.Cmd {
	if msg.req.gen != m.v.gen {
		return nil // for a view that is gone
	}
	if m.inflight != nil && m.inflight.id == msg.req.id {
		m.fetchCancel()
		m.inflight, m.fetchCancel = nil, nil
	}
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return nil // superseded
		}
		if !m.v.confirmed {
			return m.revert(msg.err)
		}
		r := msg.req
		m.failed = &r
		m.setMsg(msgErr, "read failed: "+msg.err.Error())
		return nil
	}
	m.store(msg.req, msg.win)
	m.v.confirmed = true
	if m.prev != nil && m.v.view.Where != "" {
		m.prev = nil
	}
	m.clampCursor()
	m.scrollToCursor()
	return m.ensure()
}

// store adds a fetched window to the cache.
func (m *Model) store(req fetchReq, w data.Window) {
	d := m.v
	if w.Len < req.n && (w.Len > 0 || w.Start == 0) && d.total < 0 {
		// a short read reaches the end of the view: its row count is exact
		d.upper = w.Start + int64(w.Len)
	} else if w.Len == 0 && d.total < 0 && (d.upper < 0 || w.Start < d.upper) {
		d.upper = w.Start
	}
	if end := w.Start + int64(w.Len); end > d.known {
		d.known = end
	}
	for i := 0; i < w.Len && i < len(w.FileRows); i++ {
		r, fr := w.Start+int64(i), w.FileRows[i]
		d.fileRow[r] = fr
		d.labelW = max(d.labelW, textWidth(commas(fr)))
	}
	for name, vals := range w.Cols {
		ci, ok := m.colIdx[name]
		if !ok {
			continue
		}
		col := d.cells[name]
		if col == nil {
			col = make(map[int64]string, len(vals))
			d.cells[name] = col
		}
		cw := d.colW[ci]
		for i, s := range vals {
			col[w.Start+int64(i)] = s
			if cw < maxColWidth {
				cw = max(cw, min(maxColWidth, textWidth(s)))
			}
		}
		d.colW[ci] = cw
	}
	if !m.hintSet && d.view.Plain() && w.Start == 0 && w.Len > 0 {
		m.hintSet = true
		m.hint = filterHint(m.cols, d, m.right)
		m.filter.Placeholder = m.hint
	}
	if len(d.fileRow) > cacheLimit {
		m.evict()
	}
}

// evict drops cached rows far from the screen.
func (m *Model) evict() {
	n := int64(m.bodyH())
	lo, hi := m.top-5*n, m.top+6*n
	d := m.v
	for r := range d.fileRow {
		if r < lo || r >= hi {
			delete(d.fileRow, r)
		}
	}
	for _, col := range d.cells {
		for r := range col {
			if r < lo || r >= hi {
				delete(col, r)
			}
		}
	}
}

func (m *Model) onWheel(ms tea.Mouse) tea.Cmd {
	if m.focus == focusGoto {
		return nil
	}
	const step = 3
	switch ms.Button {
	case tea.MouseWheelUp:
		m.top -= step
	case tea.MouseWheelDown:
		m.top += step
	case tea.MouseWheelLeft:
		m.curCol--
	case tea.MouseWheelRight:
		m.curCol++
	default:
		return nil
	}
	n := int64(m.bodyH())
	m.top = max(0, min(m.top, max(0, m.v.lastRow()-n+1)))
	// the wheel scrolls the screen; the cursor is dragged along at the edges
	m.curRow = max(m.top, min(m.curRow, m.top+n-1))
	m.clampCursor()
	m.scrollToCursor()
	return m.ensure()
}

func (m *Model) onClick(ms tea.Mouse) tea.Cmd {
	if ms.Button != tea.MouseLeft {
		return nil
	}
	if ms.Y >= filterTop && ms.Y < gridTop {
		if m.focus != focusFilter {
			return m.openFilter()
		}
		return nil
	}
	n := m.bodyH()
	if ms.Y < bodyTop || ms.Y >= bodyTop+n {
		return nil
	}
	if m.focus == focusFilter {
		m.closeFilter()
	}
	row := m.top + int64(ms.Y-bodyTop)
	if lim := m.v.limit(); lim >= 0 && row >= lim {
		return nil
	}
	x := ms.X - 2 // border and padding
	for _, s := range m.layout() {
		if x >= s.x && x < s.x+s.sw {
			m.curCol = s.col
			m.curRow = row
			m.clampCursor()
			m.scrollToCursor()
			return m.ensure()
		}
	}
	return nil
}
