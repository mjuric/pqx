package grid

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/config"
	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

func (g *Grid) onKey(k tea.KeyPressMsg) tea.Cmd {
	n := int64(g.bodyH())
	switch k.String() {
	case "up":
		g.curRow--
	case "down":
		g.curRow++
	case "left":
		g.curCol--
	case "right":
		g.curCol++
	case "pgup":
		g.curRow -= n
		g.top -= n
	case "pgdown":
		g.curRow += n
		g.top += n
	case "home":
		g.curCol = 0
	case "end":
		g.curCol = len(g.cols) - 1
	case "ctrl+home":
		g.curRow = 0
	case "ctrl+end":
		if g.v.limit() < 0 {
			return notice(kit.Info, "Still counting rows…", 2*time.Second)
		}
		g.curRow = g.v.lastRow()
	case "enter", "d":
		return kit.Send(kit.ToggleDetailMsg{})
	case "s":
		return g.sortBy(g.curName())
	case "-":
		return g.hideColumn()
	case "p":
		return g.pin()
	case "c":
		return g.pickColumns()
	case "f":
		g.st.Raw = !g.st.Raw
		return kit.Send(kit.RawChangedMsg{})
	case "<":
		return g.stepDigits(-1)
	case ">":
		return g.stepDigits(1)
	case "F":
		return g.formatDialog()
	case "y":
		return g.withCursorValue(g.copyValue)
	case "i":
		if name := g.curName(); name != "" {
			return kit.Send(kit.ColumnStatsMsg{Column: name})
		}
		return nil
	case "g":
		return g.gotoDialog()
	case "x", "ctrl+x":
		return g.clearFilter()
	default:
		return nil
	}
	return g.moved()
}

// notice is a toast of severity sev for d (0 for the default).
func notice(sev kit.Severity, text string, d time.Duration) tea.Cmd {
	return kit.Send(kit.NotifyMsg{Severity: sev, Text: text, Timeout: d})
}

func notBuilt() tea.Cmd { return notice(kit.Warning, "not built yet", 0) }

func (g *Grid) onWheel(ms tea.Mouse) tea.Cmd {
	switch ms.Button {
	case tea.MouseWheelUp:
		g.top -= wheelStep
	case tea.MouseWheelDown:
		g.top += wheelStep
	case tea.MouseWheelLeft:
		g.curCol--
	case tea.MouseWheelRight:
		g.curCol++
	default:
		return nil
	}
	n := int64(g.bodyH())
	g.top = max(0, min(g.top, max(0, g.v.lastRow()-n+1)))
	// the wheel scrolls the screen; the cursor is dragged along at the edges
	g.curRow = max(g.top, min(g.curRow, g.top+n-1))
	return g.moved()
}

// onClick: the edge markers page sideways; a header sorts by its column; a
// cell takes the cursor, or toggles the details pane if it has it already
// (DataTable's CellSelected).
func (g *Grid) onClick(ms tea.Mouse) tea.Cmd {
	if ms.Button != tea.MouseLeft || !g.sized() {
		return nil
	}
	if ms.X < edgeCells {
		return g.pageColumns(-1)
	}
	if ms.X >= g.right() {
		return g.pageColumns(1)
	}
	for _, s := range g.layout() {
		if ms.X < s.x || ms.X >= s.x+s.sw {
			continue
		}
		if ms.Y < headerRows {
			return g.sortBy(g.cols[s.col].Name)
		}
		row := g.top + int64(ms.Y-headerRows)
		if lim := g.v.limit(); (lim >= 0 && row >= lim) || row > g.v.lastRow() {
			return nil
		}
		if row == g.curRow && s.col == g.curCol {
			return kit.Send(kit.ToggleDetailMsg{})
		}
		g.curRow, g.curCol = row, s.col
		return g.moved()
	}
	return nil
}

// pageColumns scrolls a screen of columns sideways without moving the
// cursor (a click on ‹ or ›).
func (g *Grid) pageColumns(dir int) tea.Cmd {
	first, last, hl, hr := g.colWindow()
	p := g.pinned()
	if dir > 0 && hr > 0 {
		g.left = max(p, last+1)
	} else if dir < 0 && hl > 0 {
		// back until the old first column is the last that fits
		target := max(p, first-1)
		l := target
		for l > p && g.fits(l-1, target) {
			l--
		}
		g.left = l
	} else {
		return nil
	}
	return g.ensure()
}

// sortBy cycles the view's sort on column name: ascending, descending, off
// (Python's _sort_by).
func (g *Grid) sortBy(name string) tea.Cmd {
	if name == "" {
		return nil
	}
	v := g.st.View
	if v.IsSQL() {
		return notice(kit.Warning, "Sort SQL results with ORDER BY in the query", 0)
	}
	var order []data.Sort
	cur := -1
	for i, s := range v.OrderBy {
		if s.Column == name {
			cur = i
		}
	}
	switch {
	case cur < 0:
		order = []data.Sort{{Column: name}}
	case !v.OrderBy[cur].Desc:
		order = []data.Sort{{Column: name, Desc: true}}
	}
	return kit.Send(kit.SetViewMsg{View: data.View{Where: v.Where, OrderBy: order}, KeepFileRow: -1})
}

// clearFilter goes back to the whole file, the cursor on the same file row
// (Python's action_clear_filter; the sort goes with the filter).
func (g *Grid) clearFilter() tea.Cmd {
	if g.st.View.Plain() {
		return nil
	}
	fr := int64(-1)
	if g.v.loaded(g.curRow) {
		fr = g.v.fileRow[g.curRow]
	}
	return kit.Send(kit.SetViewMsg{View: data.View{}, KeepFileRow: fr})
}

// hideColumn hides the cursor's column, never the last one (Python's
// action_hide_column).
func (g *Grid) hideColumn() tea.Cmd {
	if len(g.cols) <= 1 {
		return nil
	}
	name := g.curName()
	g.st.Hidden[name] = true
	return tea.Batch(g.onColumnsChanged(), kit.Send(kit.ColumnsChangedMsg{}),
		notice(kit.Info, "Hid "+fmtx.Sanitize(name, false)+" · c brings it back", 2*time.Second))
}

// pin pins the columns up to the cursor's, or unpins.
func (g *Grid) pin() tea.Cmd {
	if g.st.Pinned > 0 {
		g.st.Pinned = 0
	} else {
		g.st.Pinned = g.curCol + 1
	}
	return tea.Batch(g.onColumnsChanged(), kit.Send(kit.ColumnsChangedMsg{}))
}

// onColumnsChanged takes State.Hidden and Pinned: the cursor stays on its
// column, or the one now in its place; the view keeps its leftmost column,
// or the first one right of it still shown.
func (g *Grid) onColumnsChanged() tea.Cmd {
	curName, leftName := g.curName(), ""
	idx := g.curCol
	if g.left < len(g.cols) {
		leftName = g.cols[g.left].Name
	}
	old := g.cols
	g.setColumns()
	if i, ok := g.byName[curName]; ok {
		g.curCol = i
	} else {
		g.curCol = min(idx, len(g.cols)-1)
	}
	g.left = g.pinned()
	if leftName != "" {
		for _, c := range old[indexOf(old, leftName):] {
			if i, ok := g.byName[c.Name]; ok {
				g.left = max(i, g.pinned())
				break
			}
		}
	}
	return g.moved()
}

func indexOf(cols []column, name string) int {
	for i, c := range cols {
		if c.Name == name {
			return i
		}
	}
	return len(cols)
}

// pickColumns opens the column picker.
func (g *Grid) pickColumns() tea.Cmd {
	if g.env.Dialogs == nil {
		return notBuilt()
	}
	return kit.Send(kit.OpenDialogMsg{Dialog: g.env.Dialogs.Columns(g.st.Columns, g.st.Hidden, g.st.Current)})
}

// onColumnsPicked shows the columns picked, landing on the current column
// if it is among them.
func (g *Grid) onColumnsPicked(visible []string) tea.Cmd {
	if len(visible) == 0 {
		return nil
	}
	show := map[string]bool{}
	for _, n := range visible {
		show[n] = true
	}
	g.st.Hidden = map[string]bool{}
	for _, c := range g.st.Columns {
		if !show[c.Name] {
			g.st.Hidden[c.Name] = true
		}
	}
	cmd := g.onColumnsChanged()
	if i, ok := g.byName[g.st.Current]; ok {
		g.curCol = i
	}
	g.hiddenHint = ""
	return tea.Batch(cmd, g.moved(), kit.Send(kit.ColumnsChangedMsg{}))
}

// onColumnChanged follows another part's current column: the cursor goes
// to it, same row; a hidden one leaves the grid put, with a status hint
// until the cursor moves.
func (g *Grid) onColumnChanged(m kit.ColumnChangedMsg) tea.Cmd {
	if m.From == Part {
		return nil
	}
	name := g.st.Current
	if i, ok := g.byName[name]; ok {
		g.curCol = i
		return g.moved()
	}
	if g.st.Hidden[name] {
		g.hiddenHint = name
		return kit.Send(kit.StatusMsg{Severity: kit.Warning, Text: fmtx.Sanitize(name, false) + " is hidden · c to show"})
	}
	return nil
}

// onRaw: "f" switched raw values: every cell formats again.
func (g *Grid) onRaw() {
	d := g.v
	d.text = map[string]map[int64]*cellTx{}
	a, b := g.nearRows()
	for _, c := range g.cols {
		w := 0
		for r := a; r < b; r++ {
			if v, ok := d.cell(c.Name, r); ok {
				w = max(w, len([]rune(g.format(c, v).plain)))
			}
		}
		g.grow(c.Name, w)
	}
	g.reserve(nil) // raw values are wider: so are the ones still loading
	g.fitVisible()
	g.scrollToColumn()
}

// stepDigits is "<" and ">": one digit fewer or more for the cursor's
// column (Python's action_step_digits).
func (g *Grid) stepDigits(delta int) tea.Cmd {
	name := g.curName()
	if name == "" {
		return nil
	}
	c := g.cols[g.curCol]
	cur := g.st.Formats[name]
	o := fmtx.StepOverride(cur, c.kind, delta)
	if !o.Set {
		return notice(kit.Warning, fmtx.Sanitize(name, false)+" has no digits to change · F sets a format spec", 3*time.Second)
	}
	return g.setFormat(name, o, "")
}

// formatDialog opens the format dialog for the cursor's column, with a
// sample value from it (read first if need be).
func (g *Grid) formatDialog() tea.Cmd {
	if g.env.Dialogs == nil {
		return notBuilt()
	}
	if g.curName() == "" {
		return nil
	}
	c := g.cols[g.curCol]
	return g.withCursorValue(func(_ string, v data.Value) tea.Cmd {
		return kit.Send(kit.OpenDialogMsg{Dialog: g.env.Dialogs.Format(c.Column, g.st.Formats[c.Name], v)})
	})
}

// setFormat applies a column's format (the zero Override is automatic),
// redraws, and remembers it in formats.yaml unless it is a --format of this
// session (Python's _set_format).
func (g *Grid) setFormat(name string, o fmtx.Override, where string) tea.Cmd {
	if o.Set {
		g.st.Formats[name] = o
	} else {
		delete(g.st.Formats, name)
	}
	g.invalidateColumn(name)
	g.fitVisible()
	g.scrollToColumn()
	kind := fmtx.KindStr
	if i, ok := g.byName[name]; ok {
		kind = g.cols[i].kind
	} else if c, ok := g.st.Column(name); ok {
		kind = fmtx.KindFor(c.Name, c.Arrow, c.Unit)
	}
	safe := fmtx.Sanitize(name, false)
	shown := fmtx.DescribeOverride(o, kind)
	if !o.Set || shown == "" {
		shown = "automatic"
	}
	shown = fmtx.Sanitize(shown, false)
	changed := kit.Send(kit.FormatChangedMsg{Column: name})
	if _, session := g.env.Opts.SessionFormats[name]; session {
		return tea.Batch(changed, notice(kit.Info, "✓ "+safe+": "+shown+where+" (this session)", 2*time.Second))
	}
	if _, err := config.SaveFormat(name, o, ""); err != nil {
		return tea.Batch(changed, notice(kit.Warning, safe+": "+shown+where+", for this session only — not saved: "+
			fmtx.Sanitize(err.Error(), false), 6*time.Second))
	}
	return tea.Batch(changed, notice(kit.Info, "✓ "+safe+": "+shown+where, 2*time.Second))
}

// copyValue copies a value as the details pane shows it, at full
// precision; the root makes control characters visible (␛) before it
// reaches the clipboard (Python's _copy_value).
func (g *Grid) copyValue(name string, v data.Value) tea.Cmd {
	raw := ""
	if v != nil {
		raw = fmtx.Format(v, fmtx.KindFloat, fmtx.Opts{Raw: true, Unsafe: true})
	}
	s := fmtx.Sanitize(raw, true)
	note := ""
	d := 2 * time.Second
	if s != raw {
		note = "  (control and invisible characters copied as visible symbols such as ␛)"
		d = 5 * time.Second
	}
	return tea.Batch(kit.Send(kit.CopyMsg{Text: raw}),
		notice(kit.Info, "✓ Copied "+fmtx.Sanitize(name, false)+" = "+truncRunes(s, 60)+note, d))
}

// gotoDialog opens the go-to dialog.
func (g *Grid) gotoDialog() tea.Cmd {
	if g.env.Dialogs == nil {
		return notBuilt()
	}
	return kit.Send(kit.OpenDialogMsg{Dialog: g.env.Dialogs.Goto(g.st.Total)})
}

// gotoRow puts the cursor on row r, in the middle of the screen. While the
// view's end isn't known the rows up to r are taken to exist until a read
// says otherwise.
func (g *Grid) gotoRow(r int64) tea.Cmd {
	r = max(0, r)
	if lim := g.v.limit(); lim >= 0 {
		r = min(r, max(0, lim-1))
	} else {
		g.v.hope = max(g.v.hope, r+1)
	}
	g.curRow = r
	g.top = r - int64(g.bodyH()/2)
	return g.moved()
}
