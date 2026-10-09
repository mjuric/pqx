package grid

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// failedCell is the value of a cell whose column couldn't be read for its
// row (shown as ✗ and not read again while the row is cached).
type failedCell struct{}

// viewData is what is known about one view: its row count, the rows and
// cells read so far, and the columns' widths. A new view makes a new
// viewData with a new gen; results for another gen are dropped.
type viewData struct {
	gen   int
	view  data.View
	ids   bool  // rows have file row numbers (lazy columns can be read for them)
	total int64 // rows in the view; -1 while unknown (counting)
	known int64 // rows known to exist (a lower bound while total is -1)
	upper int64 // rows at or past upper are known not to exist; -1 if not known
	hope  int64 // rows taken to exist until a read says (a jump past the rows known)

	fileRow map[int64]int64              // view row -> file row (-1 if it has none)
	vals    map[string]map[int64]any     // column -> view row -> value (or failedCell)
	text    map[string]map[int64]*cellTx // column -> view row -> formatted cell
	colW    map[string]int               // column text widths, grown as cells are drawn
	labelW  int                          // row-label width, grown likewise

	confirmed bool // a read of this view has succeeded

	noticed    map[string]bool // columns whose failure was reported (once per view)
	failedCols map[string]bool // columns that couldn't be read: not read again in this view
}

func (g *Grid) newViewData(v data.View) *viewData {
	g.gen++
	d := &viewData{
		gen: g.gen, view: v, ids: g.hasRowIDs(v), total: -1, upper: -1,
		fileRow:    map[int64]int64{},
		vals:       map[string]map[int64]any{},
		text:       map[string]map[int64]*cellTx{},
		colW:       map[string]int{},
		noticed:    map[string]bool{},
		labelW:     1,
		failedCols: map[string]bool{},
	}
	return d
}

func (d *viewData) setTotal(n int64) {
	d.total = n
	if n >= 0 {
		d.known = n
	}
}

// lastRow is the last row the cursor may go to.
func (d *viewData) lastRow() int64 {
	if d.total >= 0 {
		return max(d.total-1, 0)
	}
	m := max(d.known, d.hope)
	if d.upper >= 0 {
		m = min(m, d.upper)
	}
	return max(m-1, 0)
}

// limit is the exclusive end of the rows that may exist, or -1 if unknown.
func (d *viewData) limit() int64 {
	if d.total >= 0 {
		return d.total
	}
	return d.upper
}

// loaded reports whether row r has been read.
func (d *viewData) loaded(r int64) bool { _, ok := d.fileRow[r]; return ok }

// cell is the value of (column, row): ok false if not read yet.
func (d *viewData) cell(name string, r int64) (any, bool) {
	v, ok := d.vals[name][r]
	return v, ok
}

func (d *viewData) set(name string, r int64, v any) {
	col := d.vals[name]
	if col == nil {
		col = map[int64]any{}
		d.vals[name] = col
	}
	col[r] = v
	delete(d.text[name], r)
}

// fetchReq is one read of rows [start, start+n) of view generation gen,
// for cols.
type fetchReq struct {
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
	for _, c := range o.cols {
		if !slices.Contains(r.cols, c) {
			return false
		}
	}
	return true
}

type pageResult struct {
	req fetchReq
	win data.Window
	err error
}

// colsReq is a read of columns for rows already read: view rows and their
// file rows, in order.
type colsReq struct {
	gen      int
	tag      string
	rows     []int64
	fileRows []int64
	cols     []string
}

type colsResult struct {
	req colsReq
	win data.Window
	err error
}

// ctxErr is err, or the context's error if it was cancelled meanwhile
// (DuckDB answers an interrupt with its own "INTERRUPT Error").
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// ensure starts the reads the screen needs: rows near the screen that
// aren't read ("page"), and for rows read, the columns near the screen that
// they lack ("cols").
func (g *Grid) ensure() tea.Cmd {
	if !g.sized() || len(g.cols) == 0 {
		return nil
	}
	if cmd := g.ensureRows(); cmd != nil {
		return cmd
	}
	return g.ensureCols()
}

// rowRange is [a, b) clipped to the rows that may exist.
func (g *Grid) rowRange(a, b int64) (int64, int64) {
	a = max(a, 0)
	if lim := g.v.limit(); lim >= 0 {
		b = min(b, lim)
	}
	return a, b
}

// ensureRows reads the rows within half a screen of the screen if any of
// them isn't read, a screen each side, for the columns on screen. A read already running that covers
// them is left alone; otherwise it is replaced.
func (g *Grid) ensureRows() tea.Cmd {
	n := int64(g.bodyH())
	d := g.v
	ca, cb := g.rowRange(g.top-n/2, g.top+n+n/2)
	missing := false
	for r := ca; r < cb; r++ {
		if !d.loaded(r) {
			missing = true
			break
		}
	}
	if !missing {
		return nil
	}
	fa, fb := g.rowRange(g.top-n, g.top+2*n)
	if fa >= fb {
		return nil
	}
	// only the columns on screen: the first draw waits for this read, whose
	// cost follows the columns; those near the screen follow ("cols")
	cols := g.allNames()
	if d.ids {
		cols = g.nearNames(0)
	}
	req := fetchReq{gen: d.gen, start: fa, n: int(fb - fa), cols: cols}
	if g.page != nil && g.env.Tasks.Running("page") && g.page.covers(req) {
		return nil
	}
	if g.failed != nil && g.failed.covers(req) {
		return nil // don't retry a read that just failed
	}
	g.page = &req
	ds, view := g.ds, d.view
	return g.env.Tasks.Run("page", "loading rows", false, func(ctx context.Context) tea.Msg {
		w, err := ds.Fetch(ctx, view, req.start, req.n, req.cols)
		return pageResult{req: req, win: w, err: ctxErr(ctx, err)}
	})
}

// ensureCols reads, for the rows read near the screen, the columns near it
// they lack: when a column within a screen is missing, the missing ones
// within two screens are read, so scrolling sideways reads about once a
// screen. One read at a time: while one runs, columns it brings are left
// to it; if it brings none of those needed, a new read replaces it.
func (g *Grid) ensureCols() tea.Cmd {
	d := g.v
	if !d.ids || (g.page != nil && g.env.Tasks.Running("page")) {
		return nil
	}
	n := int64(g.bodyH())
	ca, cb := g.rowRange(g.top-n/2, g.top+n+n/2)
	need := g.missingNames(g.nearNames(1), ca, cb)
	if len(need) == 0 {
		return nil
	}
	if c := g.cols1; c != nil && c.gen == d.gen && g.env.Tasks.Running("cols") {
		for _, name := range need {
			if slices.Contains(c.cols, name) {
				return nil // its result checks again
			}
		}
	}
	fa, fb := g.rowRange(g.top-n, g.top+2*n)
	req := g.colsRequest("cols", g.missingNames(g.nearNames(2), fa, fb), fa, fb)
	if len(req.rows) == 0 || len(req.cols) == 0 {
		return nil
	}
	g.cols1 = &req
	return g.runCols(req, "loading columns")
}

// colsRequest is a read of cols for the rows read in [a, b).
func (g *Grid) colsRequest(tag string, cols []string, a, b int64) colsReq {
	d := g.v
	req := colsReq{gen: d.gen, tag: tag, cols: cols}
	for r := a; r < b; r++ {
		if fr, ok := d.fileRow[r]; ok {
			req.rows = append(req.rows, r)
			req.fileRows = append(req.fileRows, fr)
		}
	}
	return req
}

func (g *Grid) runCols(req colsReq, label string) tea.Cmd {
	ds, view := g.ds, g.v.view
	return g.env.Tasks.Run(req.tag, label, false, func(ctx context.Context) tea.Msg {
		w, err := readColumns(ctx, ds, view, req)
		return colsResult{req: req, win: w, err: ctxErr(ctx, err)}
	})
}

// readColumns reads req's columns for its rows, by file row; a data layer
// without FetchColumns yet is asked for the view's rows by position.
func readColumns(ctx context.Context, ds data.Dataset, view data.View, req colsReq) (data.Window, error) {
	lo, hi := req.rows[0], req.rows[len(req.rows)-1]+1
	byPos := func() (data.Window, error) {
		w, err := ds.Fetch(ctx, view, lo, int(hi-lo), req.cols)
		if err != nil {
			return w, err
		}
		// keep the requested rows only
		out := data.Window{Len: len(req.rows), FileRows: req.fileRows, Cols: map[string][]data.Value{}, Failed: w.Failed}
		for name, vals := range w.Cols {
			col := make([]data.Value, len(req.rows))
			for i, r := range req.rows {
				if j := int(r - lo); j < len(vals) {
					col[i] = vals[j]
				}
			}
			out.Cols[name] = col
		}
		if w.Len < int(hi-lo) {
			return out, errors.New("the view ended early")
		}
		// the view's rows must still be the file rows asked for
		if w.FileRows != nil {
			for i, r := range req.rows {
				if j := int(r - lo); j >= len(w.FileRows) || w.FileRows[j] != req.fileRows[i] {
					return out, errors.New("the view's rows moved")
				}
			}
		}
		return out, nil
	}
	w, err := ds.FetchColumns(ctx, req.fileRows, req.cols)
	if errors.Is(err, data.ErrNotImplemented) {
		return byPos()
	}
	return w, err
}

// allNames are the names of the columns shown.
func (g *Grid) allNames() []string {
	out := make([]string, len(g.cols))
	for i, c := range g.cols {
		out[i] = c.Name
	}
	return out
}

// nearNames are the pinned columns and the scrollable ones within screens
// screens of the view (Python's columns_near).
func (g *Grid) nearNames(screens int) []string {
	var out []string
	for _, i := range g.colsNear(screens) {
		out = append(out, g.cols[i].Name)
	}
	return out
}

// missingNames are the names among names with a cell not read yet in a row
// read in [a, b), leaving out the cells the details pane's read is bringing.
func (g *Grid) missingNames(names []string, a, b int64) []string {
	d := g.v
	var out []string
	for _, name := range names {
		col := d.vals[name]
		for r := a; r < b; r++ {
			if !d.loaded(r) {
				continue
			}
			if _, ok := col[r]; !ok && !g.coming(name, r) {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// onDone takes a task's result.
func (g *Grid) onDone(m kit.DoneMsg) tea.Cmd {
	switch r := m.Msg.(type) {
	case pageResult:
		return g.onPage(r)
	case colsResult:
		return g.onCols(r)
	case footerResult:
		g.onFooter(r)
	}
	return nil
}

// onCancelled: Esc stopped reads; what they were reading stays a
// placeholder and the next move reads it again. Actions waiting for a
// column are dropped.
func (g *Grid) onCancelled(tags []string) {
	for _, t := range tags {
		switch {
		case t == "page":
			g.page = nil
			g.dropUnloadedWaiters()
		case t == "cols":
			g.cols1 = nil
		case t == "widths":
			g.footerStarted = false
		case len(t) > 5 && t[:5] == "cell:":
			delete(g.cellTasks, t[5:])
			g.dropWaiters(t[5:])
		}
	}
}

func (g *Grid) onPage(r pageResult) tea.Cmd {
	if r.req.gen != g.v.gen {
		return nil // for a view that is gone
	}
	g.page = nil // (only the current task's result arrives)
	if r.err != nil {
		if errors.Is(r.err, context.Canceled) {
			return nil
		}
		if !g.v.confirmed && g.prev != nil {
			return g.revert(r.err)
		}
		req := r.req
		g.failed = &req
		g.dropUnloadedWaiters()
		msg := fmtx.Sanitize(truncRunes(firstLine(r.err), 160), false)
		return tea.Batch(
			kit.Send(kit.NotifyMsg{Severity: kit.Error, Title: "✗ Query failed", Text: fmtx.Sanitize(truncRunes(r.err.Error(), 600), true)}),
			kit.Send(kit.StatusMsg{Severity: kit.Error, Text: "read failed: " + msg}))
	}
	g.keepCursorInView(func() {
		g.store(r.req.start, r.req.n, r.win)
		g.reserve(nil)
	})
	if r.win.Len < r.req.n {
		req := r.req
		g.failed = &req // a short read: reading it again tells nothing new
	}
	g.v.confirmed = true
	g.prev = nil
	cmds := []tea.Cmd{g.failedNotice(r.win.Failed), g.runWaiters(), g.refreshed(), g.startFooter()}
	if g.foundEnd {
		g.foundEnd = false
		cmds = append(cmds, kit.Send(kit.TotalMsg{}))
	}
	return tea.Batch(cmds...)
}

func (g *Grid) onCols(r colsResult) tea.Cmd {
	req := r.req
	if req.tag == "cols" && g.cols1 != nil && g.cols1.gen == req.gen {
		g.cols1 = nil
	}
	if name, ok := cellTag(req.tag); ok {
		delete(g.cellTasks, name)
	}
	if req.gen != g.v.gen {
		return nil
	}
	if r.err != nil {
		if errors.Is(r.err, context.Canceled) {
			return nil
		}
		failed := map[string]error{}
		for _, c := range req.cols {
			failed[c] = r.err
		}
		g.markFailed(req, failed)
		if name, ok := cellTag(req.tag); ok {
			g.dropWaiters(name)
		}
		return tea.Batch(g.failedNotice(failed), g.refreshed())
	}
	d := g.v
	g.keepCursorInView(func() {
		for name, vals := range r.win.Cols {
			for i, row := range req.rows {
				if i >= len(vals) {
					break
				}
				if fr, ok := d.fileRow[row]; !ok || fr != req.fileRows[i] {
					continue // evicted meanwhile
				}
				if _, ok := d.cell(name, row); ok {
					continue // never replace what is there
				}
				d.set(name, row, vals[i])
			}
			g.fitValues(name, vals)
		}
	})
	g.markFailed(req, r.win.Failed)
	return tea.Batch(g.failedNotice(r.win.Failed), g.runWaiters(), g.refreshed())
}

// markFailed marks the columns failed for req's rows where they are still
// missing.
func (g *Grid) markFailed(req colsReq, failed map[string]error) {
	for name := range failed {
		g.failColumn(name)
	}
}

// failedNotice says which columns couldn't be read (Python's "✗ Columns").
func (g *Grid) failedNotice(failed map[string]error) tea.Cmd {
	if len(failed) == 0 {
		return nil
	}
	var first error
	names := make([]string, 0, len(failed))
	for n := range failed {
		names = append(names, n)
	}
	sort.Strings(names)
	fresh := false
	for _, n := range names {
		if !g.v.noticed[n] {
			g.v.noticed[n] = true
			fresh = true
		}
	}
	if !fresh {
		return nil // said once for this view
	}
	first = failed[names[0]]
	s := "s"
	if len(failed) == 1 {
		s = ""
	}
	msg := "Couldn't load " + strconv.Itoa(len(failed)) + " column" + s + ": " + fmtx.Sanitize(truncRunes(firstLine(first), 200), false)
	return kit.Send(kit.NotifyMsg{Severity: kit.Error, Title: "✗ Columns", Text: msg})
}

// store adds a read window to the cache. Positions come from the request
// (a Window starts where it was asked to); a short read (fewer than n rows)
// means the view ends there.
func (g *Grid) store(start int64, n int, w data.Window) {
	d := g.v
	if w.Len < n && d.total < 0 {
		end := start + int64(w.Len)
		if d.upper < 0 || end < d.upper {
			d.upper = end
		}
		// the end itself is found if rows were read up to it
		if (w.Len > 0 || start <= d.known) && g.st.Total < 0 && sameView(d.view, g.st.View) {
			g.st.Total = d.upper
			g.foundEnd = true
		}
		d.hope = min(d.hope, d.upper)
		if w.Len == 0 && start > d.known {
			d.hope = 0 // nothing where the jump went: back to the rows known
		}
	}
	if end := start + int64(w.Len); w.Len > 0 && end > d.known {
		d.known = end
	}
	if d.upper >= 0 && d.known > d.upper {
		d.known = d.upper
	}
	for i := 0; i < w.Len; i++ {
		r, fr := start+int64(i), int64(-1)
		if i < len(w.FileRows) {
			fr = w.FileRows[i]
		}
		if d.view.Plain() {
			fr = r
		}
		d.fileRow[r] = fr
		d.labelW = max(d.labelW, len(commas(g.labelOf(r))))
	}
	for name, vals := range w.Cols {
		for i, v := range vals {
			r := start + int64(i)
			if _, ok := d.cell(name, r); !ok {
				d.set(name, r, v)
			}
		}
		g.fitValues(name, vals)
	}
	for name := range w.Failed {
		d.failedCols[name] = true
	}
	for name := range d.failedCols {
		for i := 0; i < w.Len; i++ {
			if _, ok := d.cell(name, start+int64(i)); !ok {
				d.set(name, start+int64(i), failedCell{})
			}
		}
	}
	if len(d.fileRow) > cacheLimit {
		g.evict()
	}
}

// labelOf is the row label of view row r: its file row, or its position in
// a view without file rows.
func (g *Grid) labelOf(r int64) int64 {
	if fr, ok := g.v.fileRow[r]; ok && fr >= 0 {
		return fr
	}
	return r
}

// evict drops cached rows far from the screen. It copies what it keeps
// into new maps: Go's maps don't shrink when entries are deleted.
func (g *Grid) evict() {
	n := int64(g.bodyH())
	lo, hi := g.top-2*n, g.top+3*n
	keep := func(r int64) bool { return r >= lo && r < hi }
	d := g.v
	rows := map[int64]int64{}
	for r, fr := range d.fileRow {
		if keep(r) {
			rows[r] = fr
		}
	}
	d.fileRow = rows
	vals := map[string]map[int64]any{}
	for name, col := range d.vals {
		m := map[int64]any{}
		for r, v := range col {
			if keep(r) {
				m[r] = v
			}
		}
		vals[name] = m
	}
	d.vals = vals
	text := map[string]map[int64]*cellTx{}
	for name, col := range d.text {
		m := map[int64]*cellTx{}
		for r, t := range col {
			if keep(r) {
				m[r] = t
			}
		}
		text[name] = m
	}
	d.text = text
}

// revert goes back to the view before a filter that failed on its first
// read: the filter part applies it again, and its rows come back from the
// cache kept.
func (g *Grid) revert(err error) tea.Cmd {
	p := g.prev
	g.cancelReads()
	g.v.setTotal(0) // nothing more is read for it
	msg := firstLine(err)
	g.revertErr = "Query failed: " + fmtx.Sanitize(truncRunes(msg, 160), false) + " · previous view kept"
	return tea.Batch(
		kit.Send(kit.SetViewMsg{View: p.v.view, KeepFileRow: -1}),
		kit.Send(kit.NotifyMsg{Severity: kit.Error, Title: "✗ Query failed", Text: fmtx.Sanitize(truncRunes(msg, 600), true)}),
	)
}

// cancelReads stops the reads for the view on screen (a new one is coming).
func (g *Grid) cancelReads() {
	t := g.env.Tasks
	t.Cancel("page")
	t.Cancel("cols")
	for name := range g.cellTasks {
		t.Cancel("cell:" + name)
	}
	g.page, g.cols1, g.failed = nil, nil, nil
	g.cellTasks = map[string]bool{}
	g.waiters = nil
}

func cellTag(tag string) (string, bool) {
	if len(tag) > 5 && tag[:5] == "cell:" {
		return tag[5:], true
	}
	return "", false
}

func firstLine(err error) string {
	s := err.Error()
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
