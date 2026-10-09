package grid

import (
	"github.com/mjuric/pqx/go/internal/cells"
	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
)

// cellTx is a formatted cell: its text (sanitized), width, style and
// justification, and its drawing (SGR included) once drawn.
type cellTx struct {
	plain string
	w     int
	style styled.Style
	just  styled.Justify
	spans []styled.Span
	out   string // drawn: plain with its style, without padding
}

// opts are the format options for column c.
func (g *Grid) opts(c column) fmtx.Opts {
	return fmtx.Opts{Raw: g.st.Raw, Width: fmtx.DefaultWidth, Override: g.st.Formats[c.Name]}
}

// format formats one value of column c.
func (g *Grid) format(c column, v any) cellTx {
	g.formats++
	switch v.(type) {
	case failedCell:
		return cellTx{plain: cells.FailedMark, w: 1, style: g.look.Style("error"), just: just(c)}
	}
	t := fmtx.Cell(v, c.kind, g.opts(c))
	if fmtx.HasControls(t.Plain, false) {
		// fmtx's text is safe, but a cell is one line: tabs and newlines too
		// are shown as symbols (their spans would no longer line up)
		t.Plain, t.Spans = fmtx.Sanitize(t.Plain, false), nil
	}
	return cellTx{plain: t.Plain, w: cells.Width(t.Plain), style: t.Style, just: t.Justify, spans: t.Spans}
}

func just(c column) styled.Justify {
	if c.right {
		return styled.Right
	}
	return styled.Left
}

// placeholder is a cell not read yet.
func (g *Grid) placeholder(c column) cellTx {
	return cellTx{plain: cells.Placeholder, w: 1, style: g.look.Style("dim"), just: just(c)}
}

// cellText is the formatted cell (i, r), cached, widening its column if it
// doesn't fit; ok is false if the row isn't read.
func (g *Grid) cellText(i int, r int64) (*cellTx, bool) {
	c := g.cols[i]
	d := g.v
	tx := d.text[c.Name]
	if t, ok := tx[r]; ok {
		return t, true
	}
	if !d.loaded(r) {
		return nil, false
	}
	v, ok := d.cell(c.Name, r)
	if !ok {
		t := g.placeholder(c) // not cached: it changes when the column arrives
		return &t, true
	}
	t := g.format(c, v)
	if tx == nil {
		tx = map[int64]*cellTx{}
		d.text[c.Name] = tx
	}
	tx[r] = &t
	g.grow(c.Name, t.w)
	return &t, true
}

// grow widens column name to w cells if it is narrower: columns only grow.
func (g *Grid) grow(name string, w int) bool {
	if w > g.colWidth(name) {
		g.v.colW[name] = w
		if g.drawing {
			g.lateGrowth++ // a cell drawn wider than its column: fitVisible missed it
		}
		return true
	}
	return false
}

// colWidth is a column's text width: at least its header's.
func (g *Grid) colWidth(name string) int {
	d := g.v
	if w, ok := d.colW[name]; ok {
		return w
	}
	w := 1
	if i, ok := g.byName[name]; ok {
		w = max(1, g.headerWidth(g.cols[i]))
	}
	d.colW[name] = w
	return w
}

// headerWidth is the width of a column's two-line header.
func (g *Grid) headerWidth(c column) int {
	a, b := g.header(c)
	return max(cells.Width(a), cells.Width(b))
}

// header is a column's two header lines: its name and sort arrow; its type,
// unit and format override (Python's _column_label).
func (g *Grid) header(c column) (string, string) {
	name := fmtx.Sanitize(c.Name, false)
	for _, s := range g.v.view.OrderBy {
		if s.Column == c.Name {
			if s.Desc {
				name += " ↓"
			} else {
				name += " ↑"
			}
			break
		}
	}
	// (a type names the fields of a struct: text from the file)
	sub := fmtx.Sanitize(fmtx.ShortType(c.Arrow), false)
	if c.Unit != "" {
		sub += "·" + fmtx.Sanitize(c.Unit, false)
	}
	if o := fmtx.DescribeOverride(g.st.Formats[c.Name], c.kind); o != "" {
		sub += "·" + fmtx.Sanitize(o, false)
	}
	return name, sub
}

// fitValues widens column name for vals, a window just read, formatting as
// few cells as possible (Python's _fit_columns): its likely widest values
// (cells.WidestCandidates), or a sample of rows spread through the window.
func (g *Grid) fitValues(name string, vals []data.Value) {
	i, ok := g.byName[name]
	if !ok || len(vals) == 0 {
		return
	}
	c := g.cols[i]
	cand := cells.WidestCandidates(vals, c.kind, g.st.Raw, 3)
	if cand == nil {
		step := max(1, len(vals)/widthSampleRows)
		for j := 0; j < len(vals); j += step {
			cand = append(cand, vals[j])
		}
	}
	w := 0
	for _, v := range cand {
		g.formats++
		w = max(w, cells.Width(fmtx.Format(v, c.kind, g.opts(c))))
	}
	g.grow(name, w)
}

// invalidateColumn makes column name format again when next drawn; its
// width starts over from its header and refits the cached rows (Python's
// _set_format lets the column shrink to fit).
func (g *Grid) invalidateColumn(name string) {
	d := g.v
	delete(d.text, name)
	delete(d.colW, name)
	i, ok := g.byName[name]
	if !ok {
		return
	}
	c := g.cols[i]
	w := g.colWidth(name)
	a, b := g.nearRows()
	for r := a; r < b; r++ {
		if v, ok := d.cell(name, r); ok {
			w = max(w, cells.Width(g.format(c, v).plain))
		}
	}
	d.colW[name] = w
	g.reserve([]string{name})
}

// nearRows are the rows a screen above and below the screen: those a
// re-format fits columns to at once (the others when drawn).
func (g *Grid) nearRows() (int64, int64) {
	n := int64(g.bodyH())
	return max(0, g.top-n), g.top + 2*n
}

// slot is one column on screen: its index (-1 for the row labels), x within
// the pane, the width of its text (w) and of the whole slot with padding
// (sw); clipped if the right edge cuts it.
type slot struct {
	col, x, w, sw int
	clipped       bool
}

// right is the x where the table ends (the right edge marker's column).
func (g *Grid) right() int { return max(edgeCells, g.w-edgeCells) }

// labelSlot is the row labels' slot.
func (g *Grid) labelSlot() slot {
	lw := g.v.labelW
	return slot{col: -1, x: edgeCells, w: lw, sw: lw + 2*pad}
}

// layout places the pinned columns, then the scrollable ones from g.left
// that fit; the last may be clipped.
func (g *Grid) layout() []slot {
	lab := g.labelSlot()
	x := lab.x + lab.sw
	right := g.right()
	out := make([]slot, 0, 32)
	add := func(i int) {
		w := g.colWidth(g.cols[i].Name)
		s := slot{col: i, x: x, w: w, sw: w + 2*pad}
		if x+s.sw > right {
			s.sw = right - x
			s.w = max(0, s.sw-2*pad)
			s.clipped = true
		}
		out = append(out, s)
		x += s.sw
	}
	p := g.pinned()
	for i := 0; i < p && x < right; i++ {
		add(i)
	}
	for i := max(g.left, p); i < len(g.cols) && x < right; i++ {
		add(i)
	}
	return out
}

// scrollX is where scrollable columns start on screen.
func (g *Grid) scrollX() int {
	lab := g.labelSlot()
	x := lab.x + lab.sw
	for i := 0; i < g.pinned(); i++ {
		x += g.colWidth(g.cols[i].Name) + 2*pad
	}
	return x
}

// fits reports whether scrollable column c is wholly on screen with left
// as the first scrollable column.
func (g *Grid) fits(left, c int) bool {
	if c < left {
		return false
	}
	x := g.scrollX()
	for i := left; i <= c; i++ {
		x += g.colWidth(g.cols[i].Name) + 2*pad
	}
	return x <= g.right()
}

// cursorInView reports whether the cursor's cell is wholly on screen
// horizontally (a pinned one always is).
func (g *Grid) cursorInView() bool {
	if g.curCol < g.pinned() {
		return true
	}
	return g.fits(g.left, g.curCol)
}

// colWindow is (first, last, hidden left, hidden right) over the scrollable
// columns (Python's column_window): first and last are the wholly visible
// ones; a column cut by the edge counts as hidden. last < first if none.
func (g *Grid) colWindow() (int, int, int, int) {
	p := g.pinned()
	first, last := -1, -2
	for _, s := range g.layout() {
		if s.col < p || s.clipped {
			continue
		}
		if first < 0 {
			first = s.col
		}
		last = s.col
	}
	if first < 0 {
		left := max(g.left, p)
		return 0, -1, left - p, len(g.cols) - left
	}
	return first, last, first - p, len(g.cols) - 1 - last
}

// colsNear are the pinned columns and the scrollable ones within screens
// screens of the view or, if the cursor is off it, of where scrolling to
// the cursor would bring it (Python's columns_near).
func (g *Grid) colsNear(screens int) []int {
	p := g.pinned()
	out := make([]int, 0, 64)
	for i := 0; i < p; i++ {
		out = append(out, i)
	}
	n := len(g.cols)
	if p >= n {
		return out
	}
	view := max(1, g.right()-g.scrollX())
	starts := make([]int, n+1)
	for i := p; i < n; i++ {
		starts[i+1] = starts[i] + g.colWidth(g.cols[i].Name) + 2*pad
	}
	x1 := starts[max(g.left, p)]
	x2 := x1 + view
	if c := g.curCol; c >= p && c < n {
		if starts[c] < x1 {
			x1, x2 = starts[c], starts[c]+view
		} else if starts[c+1] > x2 {
			x1, x2 = starts[c+1]-view, starts[c+1]
		}
	}
	lo, hi := x1-screens*view, x2+screens*view
	for i := p; i < n; i++ {
		if starts[i+1] > lo && starts[i] < hi {
			out = append(out, i)
		}
	}
	return out
}

// keepCursorInView runs fn (which may widen columns) and, if that pushed
// the cursor's cell, on screen until then, off it, scrolls it back.
func (g *Grid) keepCursorInView(fn func()) {
	was := g.cursorInView()
	fn()
	if was && !g.cursorInView() {
		g.scrollToColumn()
	}
}

// clampCursor keeps the cursor on a row and column that exist.
func (g *Grid) clampCursor() {
	g.curRow = max(0, min(g.curRow, g.v.lastRow()))
	g.curCol = max(0, min(g.curCol, len(g.cols)-1))
}

// scrollToCursor moves the view so the cursor's cell is on screen. A
// cursor in a pinned column scrolls only vertically.
func (g *Grid) scrollToCursor() {
	g.scrollRows()
	g.scrollToColumn()
}

// scrollRows moves the view up or down so the cursor's row is on screen.
func (g *Grid) scrollRows() {
	n := int64(g.bodyH())
	if g.curRow < g.top {
		g.top = g.curRow
	}
	if g.curRow >= g.top+n {
		g.top = g.curRow - n + 1
	}
	// don't leave the bottom of the screen empty when the end is known
	if lim := g.v.limit(); lim >= 0 {
		g.top = min(g.top, max(0, lim-n))
	}
	g.top = max(0, g.top)
}

func (g *Grid) scrollToColumn() {
	p := g.pinned()
	g.left = max(g.left, p)
	if g.curCol >= p {
		if g.curCol < g.left {
			g.left = g.curCol
		}
		for g.left < g.curCol && !g.fits(g.left, g.curCol) {
			g.left++
		}
	}
	g.left = max(p, min(g.left, max(p, len(g.cols)-1)))
	g.fillRight()
}

// fillRight scrolls back left while the columns from the leftmost to the
// last still fit, so a wider grid (a larger terminal, the details pane
// closed) shows no empty space at the right (DataTable clamps its scroll
// offset the same way).
func (g *Grid) fillRight() {
	p, n := g.pinned(), len(g.cols)
	for g.left > p && g.fits(g.left-1, n-1) {
		g.left--
	}
}

// fitVisible formats the cells about to be drawn and widens any column
// they outgrow, before the draw: a number is never shown cut off. If that
// pushes the cursor's cell (on screen until then) off it, the view
// scrolls it back (Python's fit_visible).
func (g *Grid) fitVisible() {
	if !g.sized() || len(g.cols) == 0 {
		return
	}
	wasInView := g.cursorInView()
	for round := 0; round < 2; round++ {
		grew := false
		for pass := 0; pass < fitPasses; pass++ {
			changed := false
			n := int64(g.bodyH())
			for _, s := range g.layout() {
				before := g.colWidth(g.cols[s.col].Name)
				for r := g.top; r < g.top+n; r++ {
					g.cellText(s.col, r)
				}
				if g.colWidth(g.cols[s.col].Name) != before {
					changed = true
				}
			}
			if !changed {
				break
			}
			grew = true
		}
		if !grew || !wasInView || g.cursorInView() {
			return
		}
		g.scrollToColumn()
	}
}
