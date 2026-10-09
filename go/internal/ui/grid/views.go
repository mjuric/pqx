package grid

import (
	"reflect"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/ui/chrome"
)

// onViewChanged switches to State.View: a fresh cache (or the one kept for
// a view being reverted to), the cursor on the same column, and the same
// leftmost column if the columns are the same (Python's _rebuild_columns
// and _apply_page).
func (g *Grid) onViewChanged() tea.Cmd {
	st := g.st
	oldNames := g.allNames()
	leftName := ""
	if first, last, hl, _ := g.colWindow(); hl > 0 && first <= last {
		leftName = g.cols[first].Name // (Python's _rebuild_columns)
	}
	next := g.next
	g.next = nil
	if next != nil && !sameView(next.view, st.View) {
		next = nil
	}
	// a record on its way in the old view: still wanted if the new view
	// keeps it too (with the keys waiting for it), else given up on
	var waiting []queued
	var dropped tea.Cmd
	if k := g.kept; k != nil {
		if next != nil && next.fileRow == k.fileRow {
			waiting, k.queue = k.queue, nil
		}
		dropped = g.dropKeep("the view changed before the record was found", false)
	}
	g.cancelReads()
	g.setColumns()
	g.anchorLeft = ""
	if leftName != "" && reflect.DeepEqual(oldNames, g.allNames()) {
		g.anchorLeft = leftName
	}

	if p := g.prev; p != nil && sameView(p.v.view, st.View) {
		// back to the view before a filter that failed on its first read
		g.v = p.v
		g.prev = nil
		g.curRow, g.top, g.curCol, g.sx = p.curRow, p.top, p.curCol, p.sx
		if st.Total >= 0 {
			g.v.setTotal(st.Total)
		}
		var said tea.Cmd
		if g.revertErr != nil {
			said = chrome.QueryError(g.revertErr)
		}
		g.revertErr = nil
		return tea.Batch(dropped, g.refreshed(), said)
	}
	g.revertErr = nil
	if !st.View.Plain() {
		g.prev = &saved{v: g.v, curRow: g.curRow, top: g.top, curCol: g.curCol, sx: g.sx}
	} else {
		g.prev = nil
	}
	old := g.v
	g.v = g.newViewData(st.View)
	g.v.setTotal(st.Total)
	if !old.view.IsSQL() && !st.View.IsSQL() {
		// until the new rows arrive, row labels as wide as they were, so
		// the columns kept on screen don't move meanwhile
		g.v.labelW = g.labelWidthOf(old)
	}
	if st.View.Plain() {
		g.v.setTotal(g.ds.NumRows())
		g.v.confirmed = true
	}
	// widths as the values will likely be, so the leftmost column kept
	// stays leftmost when they arrive (Python's _reserve_widths)
	g.reserve(nil)
	// the cursor stays on the current column if the view shows it
	g.curCol = 0
	if i, ok := g.byName[st.Current]; ok {
		g.curCol = i
	}
	g.curRow, g.top = 0, 0
	var replay []queued
	var kept tea.Cmd
	if next != nil {
		replay, kept = g.startKeep(next, waiting)
	} else {
		kept = notApplied(waiting, "the record isn't kept in this view")
	}
	g.sx = 0
	g.applyAnchor()
	// (the current column stays as it is: a view without it, a SQL
	// result, doesn't make another current)
	g.scrollToCursor()
	var values map[string]data.Value
	if next != nil {
		values = next.values
	}
	return tea.Batch(dropped, kept, g.refreshed(), g.replay(replay, values))
}

// applyAnchor shows the column kept leftmost across a view change at the
// left edge. Python does it when the view's first rows are shown (their
// widths decide where the column starts), so it is applied again then.
func (g *Grid) applyAnchor() {
	if g.anchorLeft == "" {
		return
	}
	if i, ok := g.byName[g.anchorLeft]; ok && i >= g.pinned() {
		g.sx = g.colStart(i)
		g.clampSX()
	}
}

// sameView reports whether two views are the same.
func sameView(a, b data.View) bool {
	return reflect.DeepEqual(normal(a), normal(b))
}

func normal(v data.View) data.View {
	if len(v.OrderBy) == 0 {
		v.OrderBy = nil
	}
	return v
}

// onTotal takes the view's row count.
func (g *Grid) onTotal() tea.Cmd {
	if !sameView(g.v.view, g.st.View) {
		return nil
	}
	g.v.setTotal(g.st.Total)
	return g.refreshed()
}
