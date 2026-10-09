// Package grid is the Data tab's table (Python pqx's GridTable and the app's
// grid actions): a window onto a view of any size, read lazily. Rows are
// read around the screen as it moves and kept in a cache; only the columns
// near the screen are read, the others when they come near (lazy columns,
// docs/design/wide-tables.md section E). Cells are formatted when first
// drawn and cached; columns only grow and are fitted to what is on screen
// before each draw, so a number is never shown cut off.
//
// The grid draws only its cells: the root (internal/ui/app) draws the
// panel's border, the tab strip, the status line and the key bar. Reads run
// as kit.Tasks under the tags "page" (rows), "cols" (lazy columns) and
// "cell:<column>" (a column an action waits for).
package grid

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Part is the grid's name for focus and ColumnChangedMsg.
const Part = "grid"

// Layout of the root (internal/ui/app), to know the grid's size before it
// is first drawn: a one-column margin each side; the title bar and a blank
// row, the filter box (3 rows) and the key bar around the tab panel; the
// panel's border (2 rows), a blank row and the status line inside it; 2
// cells of border and padding each side of the panel; the details pane and
// the column between it and the grid.
const (
	margin    = 1
	aboveRows = 2 + 3 + 1
	keyRows   = 1
	panelRows = 2 + 1 + 1
	sideCells = 4
	detailW   = 53 + 1
)

const (
	headerRows = 2
	// edges are the one-cell columns left and right of the table where
	// ‹ and › show columns hidden that way (Python pqx's EdgeMarker).
	edgeCells = 1
	// pad is the space on each side of a cell (DataTable's cell_padding).
	pad = 1
	// cacheLimit is the number of rows the cache keeps per view before it
	// drops rows far from the screen (each row holds a value, and once drawn
	// a formatted cell, per column read).
	cacheLimit = 4_000
	// widthSampleRows: rows spread through a window that size a column
	// whose widest value can't be guessed (Python's WIDTH_SAMPLE_ROWS).
	widthSampleRows = 16
	// fitPasses: widening a column can bring others into view; fitting the
	// screen repeats at most this often (Python's FIT_PASSES).
	fitPasses = 4
	// wheelStep is the rows one notch of the mouse wheel scrolls.
	wheelStep = 3
	// reserveCap is the widest a column not loaded yet is reserved.
	reserveCap = 40
)

// column is one column the grid shows.
type column struct {
	data.Column
	kind  fmtx.Kind
	right bool // numbers sit right
}

// Grid is the Data tab's table. It implements kit.Pane, kit.Framed and
// kit.Focusable.
type Grid struct {
	env  *kit.Env
	st   *kit.State
	ds   data.Dataset
	look kit.Look

	focused bool
	w, h    int // the size drawn last (or estimated from the terminal's)
	x, y    int // where the root draws it (kit.Placed)

	all    []column       // the view's columns (State.Columns)
	cols   []column       // those shown: all minus State.Hidden
	byName map[string]int // index in cols

	v    *viewData
	prev *saved // the view before a filter, kept until it reads (revert)
	gen  int

	curRow, top int64
	curCol, sx  int // sx: the columns' scroll, in cells (DataTable's scroll_x)

	page          *fetchReq // the "page" task running, if any
	failed        *fetchReq // the last page read that failed (not retried)
	cols1         *colsReq  // the "cols" task running, if any
	cellTasks     map[string]bool
	waiters       []waiter
	lastRow       int64 // State.Row and FileRow as last announced
	lastFileRow   int64
	hiddenHint    string          // the current column is hidden in the grid (status hint)
	next          *keepReq        // a view on its way that keeps a record (keep.go)
	kept          *keeping        // the record being looked for in the view shown
	sent          *data.View      // a view "=" asked for, not yet seen as a SetViewMsg
	inflight      *data.View      // the "=" view sent, not yet seen
	after         *kit.SetViewMsg // an "=" view made on the one in flight, sent once that arrives
	onTheWay      *data.View      // the last view asked for (SetViewMsg), while it is checked
	locateSeq     int             // the last lookup's number
	anchorLeft    string          // the column a new view shows leftmost
	revertErr     error           // why the view was reverted, for the status line once it is back
	foundEnd      bool            // a short read found the view's end before its count
	footer        map[string][2]data.Value
	footerStarted bool

	detail   *detailReq // the details pane's read while it runs (record.go)
	fromPane bool       // a key from the details pane is being run (FieldKey)

	sgr     map[styled.Style]sgrPair // see render.go
	formats int                      // values formatted (for tests)
	// drawing is set while View draws; lateGrowth counts columns that
	// widened then (none should: fitVisible fits first; for tests)
	drawing    bool
	lateGrowth int
	// the scrollable width and whether the cursor was on screen when last
	// drawn (View keeps it there when the width changes)
	lastViewW  int
	window     int64 // pageRows, if set (tests of reads near the screen only)
	lastInView bool
}

// saved is a view and the cursor on it, kept so a view that fails on its
// first read can be undone.
type saved struct {
	v           *viewData
	curRow, top int64
	curCol, sx  int
}

// New makes the grid over env's dataset and state.
func New(env *kit.Env) *Grid {
	g := &Grid{
		env: env, st: env.State, ds: env.DS, look: env.Look,
		focused:   true, // the root starts with the grid focused
		cellTasks: map[string]bool{}, lastRow: -1, lastFileRow: -2,
		sgr: map[styled.Style]sgrPair{},
	}
	if g.st.Hidden == nil {
		g.st.Hidden = map[string]bool{}
	}
	if g.st.Formats == nil {
		g.st.Formats = map[string]fmtx.Override{}
	}
	if g.st.Columns == nil {
		g.st.Columns = g.ds.Columns()
	}
	g.setColumns()
	if _, ok := g.byName[g.st.Current]; !ok && len(g.cols) > 0 {
		g.st.Current = g.cols[0].Name
	}
	g.v = g.newViewData(g.st.View)
	g.v.setTotal(g.st.Total)
	if g.st.View.Plain() {
		g.v.setTotal(g.ds.NumRows())
		g.v.confirmed = true
	}
	return g
}

// setColumns takes the columns shown from State.Columns and Hidden.
func (g *Grid) setColumns() {
	g.all = g.all[:0]
	for _, c := range g.st.Columns {
		k := fmtx.KindFor(c.Name, c.Arrow, c.Unit)
		g.all = append(g.all, column{Column: c, kind: k, right: fmtx.RightJustified(k)})
	}
	g.cols = make([]column, 0, len(g.all))
	g.byName = make(map[string]int, len(g.all))
	for _, c := range g.all {
		if !g.st.Hidden[c.Name] {
			g.byName[c.Name] = len(g.cols)
			g.cols = append(g.cols, c)
		}
	}
	if len(g.cols) == 0 && len(g.all) > 0 { // never hide every column
		for _, c := range g.all {
			delete(g.st.Hidden, c.Name)
		}
		g.setColumns()
	}
}

// pinned is the number of pinned columns.
func (g *Grid) pinned() int { return max(0, min(g.st.Pinned, len(g.cols))) }

// hasRowIDs reports whether the view's rows have file row numbers, so lazy
// columns can be read for them: not a SQL view, nor a filtered or sorted
// view of a file with its own file_row_number column (data.Window).
func (g *Grid) hasRowIDs(v data.View) bool {
	if v.IsSQL() {
		return false
	}
	if v.Plain() {
		return true
	}
	for _, c := range g.ds.Columns() {
		if strings.EqualFold(c.Name, "file_row_number") {
			return false
		}
	}
	return true
}

// Title implements kit.Framed: the root shows the tab strip there.
func (g *Grid) Title() styled.Text { return styled.Text{} }

// Place implements kit.Placed.
func (g *Grid) Place(x, y int) { g.x, g.y = x, y }

// Focus implements kit.Focusable.
func (g *Grid) Focus() tea.Cmd { g.focused = true; return nil }

// Blur implements kit.Focusable.
func (g *Grid) Blur() { g.focused = false }

// Keys implements kit.Pane (Python's KEYS["tab-data"]).
func (g *Grid) Keys() []kit.KeyHint {
	keys := []kit.KeyHint{{Key: "/", Help: "filter"}, {Key: "x", Help: "clear filter"}, {Key: "1-5", Help: "tabs"},
		{Key: "?", Help: "help"}, {Key: "q", Help: "quit"}, {Key: "s", Help: "sort"}, {Key: "=", Help: "match cell"},
		{Key: "d", Help: "detail"}, {Key: "tab", Help: "into detail"}, {Key: "c", Help: "columns"},
		{Key: "g", Help: "go to"}, {Key: "e", Help: "export"}, {Key: "< > F", Help: "format"}}
	if !g.st.DetailOpen { // Tab goes to the filter then
		keys = slices.DeleteFunc(keys, func(k kit.KeyHint) bool { return k.Key == "tab" })
	}
	return keys
}

// bodyH is the number of body rows on screen.
func (g *Grid) bodyH() int { return max(1, g.h-headerRows) }

// sized reports whether the grid knows its size.
func (g *Grid) sized() bool { return g.w > 2*edgeCells && g.h > headerRows }

// estimate sets the size from the terminal's, as the root lays it out,
// until the grid is drawn.
func (g *Grid) estimate(w, h int) {
	gw := max(1, w-2*margin)
	if g.st.DetailOpen {
		gw = max(10, gw-detailW)
	}
	g.w = max(1, gw-sideCells)
	g.h = max(1, max(3, h-aboveRows-keyRows)-panelRows)
}

// Update implements kit.Pane.
func (g *Grid) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		g.estimate(msg.Width, msg.Height)
		g.scrollToCursor()
		return g.ensure()
	case kit.ToggleDetailMsg:
		if g.w > 0 {
			if g.st.DetailOpen {
				g.w = max(1, g.w-detailW)
			} else {
				g.w += detailW
			}
		}
		g.scrollToCursor()
		return g.ensure()
	case tea.KeyPressMsg:
		return g.onKey(msg)
	case tea.MouseClickMsg:
		return g.onClick(msg.Mouse())
	case tea.MouseWheelMsg:
		return g.onWheel(msg.Mouse())
	case kit.DoneMsg:
		if msg.Tag == "detail" {
			// the pane merges its result after the grid sees it: look
			// again once it has
			return func() tea.Msg { return detailDoneMsg{g} }
		}
		return g.onDone(msg)
	case detailDoneMsg:
		if msg.g == g {
			return g.onDetailDone()
		}
		return nil
	case kit.CancelledMsg:
		return g.onCancelled(msg.Tags)
	case kit.ViewChangedMsg:
		return g.onViewChanged()
	case kit.TotalMsg:
		return g.onTotal()
	case kit.SetViewMsg:
		return g.onSetView(msg)
	case kit.ColumnChangedMsg:
		return g.onColumnChanged(msg)
	case kit.ColumnsChangedMsg:
		return g.onColumnsChanged()
	case kit.ColumnsPickedMsg:
		return g.onColumnsPicked(msg.Visible)
	case kit.FormatSetMsg:
		return g.setFormat(msg.Column, msg.Override, "")
	case kit.RawChangedMsg:
		g.onRaw()
		return nil
	case kit.GotoMsg:
		return g.gotoRow(msg.Row)
	}
	return nil
}

// moved runs after the user moved the cursor or the view: keep the cursor
// on screen, read what is missing, and announce the changes; the cursor's
// column becomes the current one (Python's cell_highlighted).
func (g *Grid) moved() tea.Cmd {
	g.clampCursor()
	var drop tea.Cmd
	if g.kept != nil && g.curRow != 0 {
		// the cursor waits at the top for a record on its way: moving off it
		// is the user's
		drop = g.dropKeep("the cursor moved before the record was found", true)
	}
	g.scrollToCursor()
	cmds := []tea.Cmd{drop, g.ensure(), g.announce(true)}
	if g.hiddenHint != "" {
		g.hiddenHint = ""
		cmds = append(cmds, kit.Send(kit.StatusMsg{}))
	}
	return tea.Batch(cmds...)
}

// refreshed runs after data arrived or the view changed: the cursor stays
// on rows that exist, but the view only scrolls sideways when the user
// moves (a click on ‹ › leaves the cursor off screen, as in Python pqx).
func (g *Grid) refreshed() tea.Cmd {
	g.clampCursor()
	g.scrollRows()
	return tea.Batch(g.ensure(), g.announce(false))
}

// announce tells the other parts that the cursor moved; sync makes the
// cursor's column the current one.
func (g *Grid) announce(sync bool) tea.Cmd {
	var cmds []tea.Cmd
	row, fr := g.curRow, g.fileRowAt(g.curRow)
	if row != g.lastRow || fr != g.lastFileRow {
		g.lastRow, g.lastFileRow = row, fr
		g.st.Row, g.st.FileRow = row, fr
		cmds = append(cmds, kit.Send(kit.CursorMsg{}))
	}
	if name := g.curName(); sync && name != "" && name != g.st.Current {
		g.st.Current = name
		cmds = append(cmds, kit.Send(kit.ColumnChangedMsg{From: Part}))
	}
	return tea.Batch(cmds...)
}

// curName is the name of the cursor's column, "" if there are none.
func (g *Grid) curName() string {
	if g.curCol >= 0 && g.curCol < len(g.cols) {
		return g.cols[g.curCol].Name
	}
	return ""
}

// fileRowAt is the file row of view row r: r itself in the plain view, -1
// if not known (not loaded, or a view without file rows).
func (g *Grid) fileRowAt(r int64) int64 {
	if g.v.view.Plain() {
		return r
	}
	if fr, ok := g.v.fileRow[r]; ok {
		return fr
	}
	return -1
}
