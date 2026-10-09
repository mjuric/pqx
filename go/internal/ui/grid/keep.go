package grid

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/sqllit"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Keeping the cursor's record across view changes ("=", clear, the details
// pane's "="), as Python pqx's _set_view, _keep_record, _land_keep,
// _drop_keep, _queue_for_keep, _locate_record and _seek_record do.
//
// A SetViewMsg with KeepFileRow names the record. In the plain view a file
// row is its position, so the cursor goes there at once. In a filtered or
// sorted view the view's first rows are shown with the cursor at the top
// while the record is looked for: if the first read holds it, it lands
// there; else FindRow ("locate", a user task Esc cancels) gives its
// position and the rows around it are read (FetchAround, by file row). The
// record lands on the screen row it had (or as near as the view allows),
// and the cell keys pressed meanwhile (= y i F < >) act on it then. Moving
// the cursor off the top, jumping, sorting or another view gives up on it
// ("<keys> not applied: <why>"). Views without file rows (a SQL result, a
// file with its own file_row_number column) start at the top.

// queued is a cell key waiting for the record, with the column it was
// pressed on.
type queued struct {
	key      tea.KeyPressMsg
	column   string
	fromPane bool // pressed in the details pane (its notices say so)
}

// keepReq is a view on its way to the filter part (a SetViewMsg) that keeps
// a record: what is known of the record when it was asked for.
type keepReq struct {
	view      data.View
	fileRow   int64
	screenRow int
	values    map[string]data.Value
	queue     []queued
}

// keeping is a record being looked for in the view shown (Python's _keep,
// _keep_phase, _keep_values and _keep_queue).
type keeping struct {
	gen       int // the viewData it is for
	view      data.View
	fileRow   int64
	screenRow int // where on the screen it goes
	// phase: "first" (waiting for the view's first rows), "locating"
	// (FindRow) or "seeking" (reading the rows around it)
	phase  string
	values map[string]data.Value // its values in the view it came from
	queue  []queued
	seq    int // of its lookup: a stale lookup's result is ignored
}

// located is a lookup's result.
type located struct {
	gen, seq int
	pos      int64
	found    bool
	err      error
}

// onSetView notes a view on its way to the filter part, so the cursor's
// record and screen row can be kept when it arrives (Python's _set_view
// with keep_file_row, and _anchor_row).
func (g *Grid) onSetView(m kit.SetViewMsg) tea.Cmd {
	var then tea.Cmd
	if g.inflight != nil && sameView(m.View, *g.inflight) {
		// an "=" view arrived: the one made on it goes out now, after it
		g.inflight = nil
		if a := g.after; a != nil {
			g.after = nil
			g.inflight = &a.View
			then = kit.Send(*a)
		} else {
			g.sent = nil
		}
	} else if g.inflight == nil {
		g.sent = nil
	}
	g.next = nil
	v := m.View
	g.onTheWay = &v
	if m.KeepFileRow < 0 {
		return then
	}
	k := &keepReq{view: m.View, fileRow: m.KeepFileRow, screenRow: g.screenRow()}
	if p := g.kept; p != nil && p.fileRow == m.KeepFileRow {
		// a record still on its way: where it is to go, and what it holds
		k.screenRow, k.values = p.screenRow, p.values
	} else {
		k.values = g.recordValues(m.KeepFileRow)
	}
	for _, key := range m.Keys {
		k.queue = append(k.queue, queued{key: key, column: g.st.Current})
	}
	g.next = k
	return then
}

// screenRow is the cursor's row on the screen.
func (g *Grid) screenRow() int {
	return int(max(0, min(g.curRow-g.top, int64(g.bodyH()-1))))
}

// recordValues are the values the cache holds for file row fr (Python's
// _record_values).
func (g *Grid) recordValues(fr int64) map[string]data.Value {
	d := g.v
	out := map[string]data.Value{}
	if fr < 0 {
		return out // (rows without file rows aren't a record to keep)
	}
	r := int64(-1)
	if g.fileRowAt(g.curRow) == fr {
		r = g.curRow
	} else {
		for row, f := range d.fileRow {
			if f == fr {
				r = row
				break
			}
		}
	}
	if r < 0 || !d.loaded(r) {
		return out
	}
	for name, col := range d.vals {
		if v, ok := col[r]; ok {
			if _, bad := v.(failedCell); !bad {
				out[name] = v
			}
		}
	}
	return out
}

// startKeep takes the record a new view keeps (next, already checked to be
// for it), with the keys still waiting from the record kept before:
// in the plain view the cursor goes to it at once (the keys returned act on
// it once the view is set up); in a view with file rows it is looked for;
// in others it can't be kept.
func (g *Grid) startKeep(next *keepReq, waiting []queued) ([]queued, tea.Cmd) {
	queue := append(waiting, next.queue...)
	switch {
	case g.st.View.Plain():
		g.curRow = max(0, min(next.fileRow, g.v.lastRow()))
		g.top = g.curRow - int64(next.screenRow)
		return queue, nil
	case g.v.ids:
		g.kept = &keeping{gen: g.v.gen, view: g.st.View, fileRow: next.fileRow, screenRow: next.screenRow,
			phase: "first", values: next.values, queue: queue}
		return nil, nil
	}
	return nil, notApplied(queue, "the record isn't kept in this view")
}

// notApplied says which waiting keys were dropped, and why.
func notApplied(queue []queued, why string) tea.Cmd {
	if len(queue) == 0 {
		return nil
	}
	keys := make([]string, len(queue))
	for i, q := range queue {
		keys[i] = q.key.String()
	}
	return notice(kit.Warning, strings.Join(keys, " ")+" not applied: "+why, 3*time.Second)
}

// dropKeep gives up on the record (the user went elsewhere, or it wasn't
// found or read): the cursor stays where it is, and the keys waiting for it
// aren't applied. moved: the user moved, so the read on its way to the
// record is stopped (its rows mustn't take the cursor back).
func (g *Grid) dropKeep(why string, moved bool) tea.Cmd {
	k := g.kept
	if k == nil {
		return nil
	}
	g.kept = nil
	switch k.phase {
	case "locating":
		g.env.Tasks.Cancel("locate")
	case "seeking":
		if moved && g.page != nil && g.page.around {
			g.env.Tasks.Cancel("page")
			g.page = nil
		}
	}
	// (the details pane shows the cursor's row again)
	return tea.Batch(notApplied(k.queue, why), kit.Send(kit.CursorMsg{}))
}

// keepOnPage looks for the record in rows just read (Python's
// _keep_record): it lands if they hold it; after the view's first rows
// it is looked up unless they were all the view has.
func (g *Grid) keepOnPage(req fetchReq, start int64, w data.Window) tea.Cmd {
	k := g.kept
	if k == nil || k.gen != g.v.gen {
		return nil
	}
	if k.phase == "seeking" && !req.around {
		return nil
	}
	for i := 0; i < w.Len && i < len(w.FileRows); i++ {
		if w.FileRows[i] == k.fileRow {
			return g.land(start + int64(i))
		}
	}
	switch {
	case k.phase == "first" && w.Len >= req.n:
		return g.locate()
	case k.phase == "first" || k.phase == "seeking":
		return g.dropKeep("the record isn't in the view", false)
	}
	return nil
}

// locate looks the record up (FindRow, task "locate").
func (g *Grid) locate() tea.Cmd {
	k := g.kept
	k.phase = "locating"
	g.locateSeq++
	k.seq = g.locateSeq
	ds, gen, seq, view, fr := g.ds, k.gen, k.seq, k.view, k.fileRow
	return tea.Batch(kit.Send(kit.CursorMsg{}), g.env.Tasks.Run("locate", "finding record", true, func(ctx context.Context) tea.Msg {
		pos, found, err := ds.FindRow(ctx, view, fr)
		return located{gen: gen, seq: seq, pos: pos, found: found, err: ctxErr(ctx, err)}
	}))
}

// onLocated takes the lookup's result (Python's _record_located): the
// record lands if its rows are read, else they are read around it.
func (g *Grid) onLocated(r located) tea.Cmd {
	k := g.kept
	if k == nil || r.seq != k.seq || r.gen != k.gen || k.gen != g.v.gen || k.phase != "locating" {
		return nil // a newer lookup runs, or the record was given up on
	}
	switch {
	case r.err != nil && errors.Is(r.err, context.Canceled):
		return g.dropKeep("finding the record was cancelled", false)
	case r.err != nil:
		return g.dropKeep("finding the record failed", false)
	case !r.found:
		return g.dropKeep("the record isn't in the view", false)
	}
	if fr, ok := g.v.fileRow[r.pos]; ok && fr == k.fileRow {
		return g.land(r.pos)
	}
	return g.seek(r.pos)
}

// seek reads the rows around the record, at position pos of the view, by
// its file row (Python's _seek_record).
func (g *Grid) seek(pos int64) tea.Cmd {
	k := g.kept
	k.phase = "seeking"
	d := g.v
	n := int64(g.bodyH())
	top := max(0, pos-int64(k.screenRow))
	fa, fb := g.rowRange(top-n, top+2*n)
	fa, fb = min(fa, pos), max(fb, pos+1)
	cols := g.allNames()
	if d.ids {
		cols = g.nearNames(0)
	}
	req := fetchReq{gen: d.gen, start: fa, n: int(fb - fa), cols: cols, around: true}
	g.page = &req
	ds, view, fr := g.ds, d.view, k.fileRow
	return g.env.Tasks.Run("page", "loading rows", false, func(ctx context.Context) tea.Msg {
		w, err := ds.FetchAround(ctx, view, fr, pos, req.start, req.n, req.cols)
		if errors.Is(err, data.ErrNotImplemented) {
			w, err = ds.Fetch(ctx, view, req.start, req.n, req.cols)
		}
		return pageResult{req: req, win: w, err: ctxErr(ctx, err)}
	})
}

// land puts the cursor on the record, at view row r, on the screen row it
// had (or as near as the view allows), then the keys that waited for it act
// on it (Python's _land_keep).
func (g *Grid) land(r int64) tea.Cmd {
	k := g.kept
	g.kept = nil
	g.curRow = r
	g.top = r - int64(k.screenRow)
	g.clampCursor()
	g.scrollRows()
	return tea.Batch(g.ensure(), g.announce(false), kit.Send(kit.CursorMsg{}), g.replay(k.queue, k.values))
}

// replay runs the keys that waited for the record, each on its column (if
// it is still shown), all at once as Python pqx does: keys after an "="
// act on the same record, and a second "=" adds to the view the first one
// asked for.
//
// values are the record's values from the view it came from: an "=" or y
// on a cell its new view hasn't read yet acts on them at once, rather than
// waiting for the read, which the view an earlier "=" makes would cancel
// (dropping the key).
func (g *Grid) replay(queue []queued, values map[string]data.Value) tea.Cmd {
	var cmds []tea.Cmd
	for _, q := range queue {
		col, ok := g.byName[q.column]
		if !ok {
			continue
		}
		g.curCol = col
		cmds = append(cmds, g.moved())
		g.fromPane = q.fromPane
		cmds = append(cmds, g.replayKey(q, values))
		g.fromPane = false
	}
	return tea.Batch(cmds...)
}

// replayKey runs a queued key on the record under the cursor.
func (g *Grid) replayKey(q queued, values map[string]data.Value) tea.Cmd {
	key := q.key.String()

	if key == "=" || key == "y" {
		_, loaded := g.v.cell(q.column, g.curRow)
		if v, known := values[q.column]; known && !(loaded && g.v.loaded(g.curRow)) {
			if key == "=" {
				return g.filterValue(q.column, v)
			}
			return g.copyValue(q.column, v)
		}
	}
	return g.cellKey(q.key)
}

// QueueKey implements kit.RecordSource: a cell key (= y i F < >) pressed
// elsewhere (the details pane) waits for the record on its way, to act on
// column then; false if no record is on its way, and the key should act now.
func (g *Grid) QueueKey(k tea.KeyPressMsg, column string) bool {
	return g.queueKey(k, column, true)
}

func (g *Grid) queueKey(k tea.KeyPressMsg, column string, fromPane bool) bool {
	if g.kept == nil {
		return false
	}
	g.kept.queue = append(g.kept.queue, queued{key: k, column: column, fromPane: fromPane})
	return true
}

// pendingRecord is the record on its way, while one is looked for after a
// view change: Pending, its file row, and the values it had in the view it
// came from (the details pane shows "finding record… · file row N"). Row
// is the cursor's, where it waits (its place in the view isn't known yet).
// Record returns this while ok.
func (g *Grid) pendingRecord() (kit.Record, bool) {
	k := g.kept
	if k == nil {
		return kit.Record{}, false
	}
	rec := kit.Record{Row: g.curRow, FileRow: k.fileRow, Values: map[string]data.Value{}, Pending: true}
	for _, c := range g.cols { // (the columns shown, as Record has them)
		if v, ok := k.values[c.Name]; ok {
			rec.Values[c.Name] = v
		} else {
			rec.Missing = append(rec.Missing, c.Name)
		}
	}
	return rec, true
}

// cellKey runs a cell key on the cursor's cell.
func (g *Grid) cellKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "=":
		return g.withCursorValue("=", g.filterValue)
	case "y":
		return g.withCursorValue("y", g.copyValue)
	case "i":
		if name := g.curName(); name != "" {
			return kit.Send(kit.ColumnStatsMsg{Column: name})
		}
	case "F":
		return g.formatDialog()
	case "<":
		return g.stepDigits(-1)
	case ">":
		return g.stepDigits(1)
	}
	return nil
}

// filterValue is "=": the filter narrowed to the cursor's value, keeping
// the record (Python's _filter_value). The condition is added to the
// filter applied, or to the one on its way if a view is being checked
// (Python adds it to the box's text).
func (g *Grid) filterValue(name string, v data.Value) tea.Cmd {
	if g.st.View.IsSQL() {
		return notice(kit.Warning, "= filtering works on the table, not on SQL results", 0)
	}
	col, ok := g.st.Column(name)
	if !ok {
		return nil
	}
	name = col.SQLName // (DuckDB's name for a case duplicate)
	if name == "" {
		name = col.Name
	}
	cond, ok := sqllit.Equals(name, v)
	if !ok {
		return notice(kit.Warning, "Can't filter on this value type", 0)
	}
	base := g.pendingView()
	nv := data.View{Where: sqllit.And(base.Where, cond), OrderBy: base.OrderBy}
	if base.IsSQL() {
		nv = data.View{Where: cond}
	}
	return g.sendView(kit.SetViewMsg{View: nv, KeepFileRow: g.fileRowAt(g.curRow)})
}

// pendingView is the view a key that changes it ("=", s) builds on: one it
// asked for a moment ago and hasn't seen yet, or one being checked, else
// the view shown.
func (g *Grid) pendingView() data.View {
	switch {
	case g.sent != nil:
		return *g.sent // asked for a moment ago, not yet seen
	case g.onTheWay != nil && g.env.Tasks.Running("validate"):
		return *g.onTheWay // being checked
	}
	return g.st.View
}

// sendView sends a view the grid makes ("=", s), in order: if one it sent
// is still on its way, this one (made on it) follows when that arrives
// (two commands' messages can arrive in either order).
func (g *Grid) sendView(m kit.SetViewMsg) tea.Cmd {
	nv := m.View
	g.sent = &nv
	if g.inflight != nil {
		g.after = &m
		return nil
	}
	g.inflight = &nv
	return kit.Send(m)
}

// keptFileRow is the file row x keeps: the record on its way, else the
// cursor's (-1 if it has none).
func (g *Grid) keptFileRow() int64 {
	if g.kept != nil {
		return g.kept.fileRow
	}
	return g.fileRowAt(g.curRow)
}
