package grid

import (
	"reflect"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// onSetView notes a view on its way to the filter part, so the cursor's
// record and screen row can be kept when it arrives (Python's _set_view
// with keep_file_row, and _anchor_row).
func (g *Grid) onSetView(m kit.SetViewMsg) {
	g.keep = nil
	if m.KeepFileRow >= 0 {
		g.keep = &pendingKeep{view: m.View, fileRow: m.KeepFileRow, screenRow: int(g.curRow - g.top)}
	}
}

// onViewChanged switches to State.View: a fresh cache (or the one kept for
// a view being reverted to), the cursor on the same column, and the same
// leftmost column if the columns are the same (Python's _rebuild_columns
// and _apply_page).
func (g *Grid) onViewChanged() tea.Cmd {
	st := g.st
	oldNames := g.allNames()
	leftName := ""
	if g.left < len(g.cols) && g.left > g.pinned() {
		leftName = g.cols[g.left].Name
	}
	keep := g.keep
	g.keep = nil
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
		g.curRow, g.top, g.curCol, g.left = p.curRow, p.top, p.curCol, p.left
		if st.Total >= 0 {
			g.v.setTotal(st.Total)
		}
		msg := g.revertErr
		g.revertErr = ""
		return tea.Batch(g.refreshed(), kit.Send(kit.StatusMsg{Severity: kit.Error, Text: msg}))
	}
	g.revertErr = ""
	if !st.View.Plain() {
		g.prev = &saved{v: g.v, curRow: g.curRow, top: g.top, curCol: g.curCol, left: g.left}
	} else {
		g.prev = nil
	}
	g.v = g.newViewData(st.View)
	g.v.setTotal(st.Total)
	if st.View.Plain() {
		g.v.setTotal(g.ds.NumRows())
		g.v.confirmed = true
	}
	// the cursor stays on the current column if the view shows it
	g.curCol = 0
	if i, ok := g.byName[st.Current]; ok {
		g.curCol = i
	}
	g.curRow, g.top = 0, 0
	g.anchorRow = -1
	if keep != nil && sameView(keep.view, st.View) && st.View.Plain() {
		// in the plain view a file row is its position
		g.curRow = keep.fileRow
		g.top = g.curRow - int64(keep.screenRow)
	}
	if g.anchorLeft != "" {
		if i, ok := g.byName[g.anchorLeft]; ok && i >= g.pinned() {
			g.left = i
		}
	}
	// (the current column stays as it is: a view without it, a SQL
	// result, doesn't make another current)
	g.scrollToCursor()
	return g.refreshed()
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
