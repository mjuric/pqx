package grid

import (
	"errors"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// The grid as the details pane sees it: kit.RecordSource (the record under
// the cursor, and merging the pane's background read into the cache), plus
// the pane's own needs (internal/ui/detail.Grid): what the background read
// should bring, and the grid's cell actions on the pane's field.
//
// The pane's read runs under the tag "detail". While it runs, the grid's own
// column reads ("cols") leave the cells it brings to it, so nothing is read
// twice (Python's _inflight["detail"]); when it ends, the grid looks again
// at what the screen needs.

// errNotRead is the reason given for a cell that couldn't be read: the grid
// keeps only the mark, not the error.
var errNotRead = errors.New("couldn't be read")

// Record implements kit.RecordSource: the record under the cursor, with the
// values of the columns shown that are loaded. Pending is never set here;
// the record keeping across views (WP11) sets it.
func (g *Grid) Record() kit.Record {
	d := g.v
	if len(g.cols) == 0 || d.limit() == 0 {
		return kit.Record{Row: -1, FileRow: -1}
	}
	r := g.curRow
	rec := kit.Record{Row: r, FileRow: g.fileRowAt(r), Values: map[string]data.Value{}}
	loaded := d.loaded(r)
	for _, c := range g.cols {
		v, ok := d.cell(c.Name, r)
		switch {
		case !loaded || !ok:
			rec.Missing = append(rec.Missing, c.Name)
		case isFailed(v):
			if rec.Failed == nil {
				rec.Failed = map[string]error{}
			}
			rec.Failed[c.Name] = errNotRead
		default:
			rec.Values[c.Name] = v
		}
	}
	return rec
}

// detailReq is the pane's background read, while it runs: the columns it
// brings for the view rows it reads.
type detailReq struct {
	gen  int
	rows map[int64]bool
	cols []string
}

// coming reports whether the pane's read is bringing column name for view
// row r.
func (g *Grid) coming(name string, r int64) bool {
	q := g.detail
	if q == nil || q.gen != g.v.gen || !g.env.Tasks.Running("detail") {
		return false
	}
	return q.rows[r] && slices.Contains(q.cols, name)
}

// DetailRead is what the details pane's background read is to bring: for the
// rows read near the screen (those the grid's own column reads cover), the
// columns shown that some of them lack, except those a running column read
// is bringing and those that failed. ok is false if there is nothing to
// read, or the view's rows have no file rows. The pane runs the read under
// the tag "detail" right away: the grid leaves those cells to it.
func (g *Grid) DetailRead() (view kit.View, fileRows []int64, cols []string, ok bool) {
	d := g.v
	if !d.ids || !g.sized() || len(g.cols) == 0 {
		return kit.View{}, nil, nil, false
	}
	n := int64(g.bodyH())
	a, b := g.rowRange(g.top-n, g.top+2*n)
	var rows []int64
	for r := a; r < b; r++ {
		if fr, ok := d.fileRow[r]; ok && fr >= 0 {
			rows = append(rows, r)
			fileRows = append(fileRows, fr)
		}
	}
	if len(rows) == 0 {
		return kit.View{}, nil, nil, false
	}
	busy := map[string]bool{}
	if c := g.cols1; c != nil && c.gen == d.gen && g.env.Tasks.Running("cols") {
		for _, name := range c.cols {
			busy[name] = true
		}
	}
	for _, c := range g.cols {
		if busy[c.Name] || g.cellTasks[c.Name] {
			continue
		}
		col := d.vals[c.Name]
		for _, r := range rows {
			if _, ok := col[r]; !ok {
				cols = append(cols, c.Name)
				break
			}
		}
	}
	if len(cols) == 0 {
		return kit.View{}, nil, nil, false
	}
	q := &detailReq{gen: d.gen, rows: make(map[int64]bool, len(rows)), cols: cols}
	for _, r := range rows {
		q.rows[r] = true
	}
	g.detail = q
	return kit.View{View: d.view, Gen: d.gen}, fileRows, cols, true
}

// Merge implements kit.RecordSource: a window read by file row (the details
// pane's read) goes into the cache of the view it was read for, if that is
// still the one shown, for the rows still cached. Cells already there are
// kept; failed columns are marked so (✗, not read again for those rows).
func (g *Grid) Merge(view kit.View, w data.Window) {
	d := g.v
	if view.Gen != d.gen || len(w.FileRows) == 0 {
		return
	}
	// view row of each file row cached
	at := make(map[int64]int64, len(d.fileRow))
	if d.view.Plain() {
		for _, fr := range w.FileRows {
			if d.loaded(fr) {
				at[fr] = fr
			}
		}
	} else {
		for r, fr := range d.fileRow {
			if fr >= 0 {
				at[fr] = r
			}
		}
	}
	g.keepCursorInView(func() {
		for name, vals := range w.Cols {
			if _, shown := g.byName[name]; !shown {
				continue
			}
			for i, fr := range w.FileRows {
				r, ok := at[fr]
				if !ok || i >= len(vals) {
					continue // evicted meanwhile
				}
				if _, ok := d.cell(name, r); !ok {
					d.set(name, r, vals[i])
				}
			}
			g.fitValues(name, vals)
		}
		for name := range w.Failed {
			for _, fr := range w.FileRows {
				if r, ok := at[fr]; ok {
					if _, ok := d.cell(name, r); !ok {
						d.set(name, r, failedCell{})
					}
				}
			}
			g.v.noticed[name] = true // the pane said so
		}
	})
}

// detailDoneMsg: the pane's read ended (its result is merged by the pane
// meanwhile); the grid looks again at what the screen needs.
type detailDoneMsg struct{ g *Grid }

// onDetailDone runs after the pane's read ended, merged or not.
func (g *Grid) onDetailDone() tea.Cmd {
	g.detail = nil
	return tea.Batch(g.runWaiters(), g.refreshed())
}

// FieldKey runs the grid's own action for key k ("y", "F", "<", ">", "x",
// ctrl+x) as pressed in the details pane on its field name: the grid's
// cursor goes to that column first (Python's action_detail_key). The digit
// keys say where the change shows (" (grid)": the pane shows full
// precision).
func (g *Grid) FieldKey(name string, k tea.KeyPressMsg) tea.Cmd {
	move := g.toColumn(name)
	g.fromPane = true
	defer func() { g.fromPane = false }()
	return tea.Batch(move, g.onKey(k))
}

// FieldValue calls fn with the value of column name of the record under the
// cursor, reading the column first if need be (Python's
// _with_cursor_value, for "=" in the details pane).
func (g *Grid) FieldValue(name string, fn func(v data.Value) tea.Cmd) tea.Cmd {
	move := g.toColumn(name)
	if g.curName() != name {
		return move
	}
	return tea.Batch(move, g.withCursorValue("=", func(_ string, v data.Value) tea.Cmd { return fn(v) }))
}

// toColumn puts the cursor on column name, same row, if the grid shows it
// (Python's _move_grid_to_column).
func (g *Grid) toColumn(name string) tea.Cmd {
	i, ok := g.byName[name]
	if !ok || i == g.curCol {
		return nil
	}
	g.curCol = i
	return g.moved()
}

func isFailed(v data.Value) bool { _, bad := v.(failedCell); return bad }
