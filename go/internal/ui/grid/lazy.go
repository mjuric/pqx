package grid

import (
	"context"
	"math"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/apache/arrow-go/v18/arrow"

	"github.com/mjuric/pqx/go/internal/cells"
	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// waiter is an action waiting for the value of (column, row) in view gen.
type waiter struct {
	gen    int
	row    int64
	name   string
	action string // the key: one waits per (row, column, key)
	fn     func(name string, v data.Value) tea.Cmd
}

// withCursorValue calls fn with the value under the cursor, reading its
// column first if need be (Python's _with_cursor_value): one read per
// column, which every action on it waits for.
func (g *Grid) withCursorValue(action string, fn func(name string, v data.Value) tea.Cmd) tea.Cmd {
	name := g.curName()
	if name == "" {
		return nil
	}
	d := g.v
	r := g.curRow
	if v, ok := d.cell(name, r); ok && d.loaded(r) {
		if _, bad := v.(failedCell); bad {
			return notice(kit.Error, fmtx.Sanitize(name, false)+" couldn't be loaded for these rows", 4*time.Second)
		}
		return fn(name, v)
	}
	w := waiter{gen: d.gen, row: r, name: name, action: action, fn: fn}
	for _, o := range g.waiters {
		if o.gen == w.gen && o.row == w.row && o.name == w.name && o.action == w.action {
			return nil // pressed again while it waits: once is enough
		}
	}
	g.waiters = append(g.waiters, w)
	if !d.loaded(r) || g.cellTasks[name] {
		return g.ensure() // the row's read brings it
	}
	a, b := g.nearRows()
	a, b = g.rowRange(a, b)
	req := g.colsRequest("cell:"+name, []string{name}, a, b)
	if len(req.rows) == 0 {
		return nil
	}
	g.cellTasks[name] = true
	return g.runCols(req, "loading "+fmtx.Sanitize(name, false))
}

// runWaiters runs the actions whose value has arrived.
func (g *Grid) runWaiters() tea.Cmd {
	var cmds []tea.Cmd
	keep := g.waiters[:0]
	for _, w := range g.waiters {
		if w.gen != g.v.gen {
			continue
		}
		v, ok := g.v.cell(w.name, w.row)
		if !ok || !g.v.loaded(w.row) {
			keep = append(keep, w)
			continue
		}
		if _, bad := v.(failedCell); bad {
			cmds = append(cmds, notice(kit.Error, fmtx.Sanitize(w.name, false)+" couldn't be loaded for these rows", 4*time.Second))
			continue
		}
		cmds = append(cmds, w.fn(w.name, v))
	}
	g.waiters = keep
	return tea.Batch(cmds...)
}

// dropUnloadedWaiters drops the actions waiting for rows not read: their
// read was cancelled (Esc) or failed, and they must not fire later when
// the rows arrive for another reason (Python pops _cell_waiters).
func (g *Grid) dropUnloadedWaiters() {
	keep := g.waiters[:0]
	for _, w := range g.waiters {
		if w.gen == g.v.gen && g.v.loaded(w.row) {
			keep = append(keep, w)
		}
	}
	g.waiters = keep
}

// dropWaiters drops the actions waiting for column name (its read was
// cancelled or failed).
func (g *Grid) dropWaiters(name string) {
	keep := g.waiters[:0]
	for _, w := range g.waiters {
		if w.name != name {
			keep = append(keep, w)
		}
	}
	g.waiters = keep
}

// kindSamples are typical values per kind, to size a column not read yet
// that has no statistics (Python's KIND_SAMPLES).
var kindSamples = map[fmtx.Kind]data.Value{
	fmtx.KindFloat: -1.2345678901234567, fmtx.KindFloat32: float32(-1.2345678), fmtx.KindFlux: -1234.5678901,
	fmtx.KindErr: 0.012345678, fmtx.KindMag: 21.123456, fmtx.KindAngle: 123.4567891, fmtx.KindMJD: 60000.123456789,
	fmtx.KindBool: true,
	fmtx.KindTime: data.Timestamp{T: time.Date(2026, 1, 2, 3, 4, 5, 678901000, time.UTC), Unit: time.Microsecond},
}

// sigDigits are the kinds shown with significant digits (fmt.SIG_DIGITS).
var sigDigits = map[fmtx.Kind]bool{fmtx.KindFlux: true, fmtx.KindErr: true, fmtx.KindFloat32: true, fmtx.KindFloat: true}

// reserve makes the columns names (nil: all) that have no values read near
// the screen as wide as their values will likely be, so the grid doesn't
// shift when they arrive (Python's _reserve_widths).
func (g *Grid) reserve(names []string) {
	if names == nil {
		names = g.allNames()
	}
	a, b := g.nearRows()
	d := g.v
	for _, name := range names {
		i, ok := g.byName[name]
		if !ok {
			continue
		}
		col := d.vals[name]
		have := false
		for r := a; r < b && !have; r++ {
			_, have = col[r]
		}
		if have {
			continue
		}
		g.grow(name, g.reservedWidth(g.cols[i]))
	}
}

// reservedWidth is how wide column c's values likely format: from its
// min and max statistics (for a float kind with all its significant
// digits, as a real value has), else a typical value of its kind; at most
// reserveCap.
func (g *Grid) reservedWidth(c column) int {
	var vals []data.Value
	if mm, ok := g.footer[c.Name]; ok {
		vals = append(vals, mm[0], mm[1])
	}
	if sigDigits[c.kind] {
		lo, hi := math.Inf(1), math.Inf(-1)
		for i, v := range vals {
			f, ok := asFloat(v)
			if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
				continue
			}
			lo, hi = min(lo, f), max(hi, f)
			if f != 0 {
				vals[i] = math.Copysign(1.2345678901234567, f) * math.Pow(10, math.Floor(math.Log10(math.Abs(f))))
			}
		}
		if lo < 0 && 0 < hi { // values near zero, with leading zeros
			vals = append(vals, -0.012345678901234567, 0.012345678901234567)
		}
	}
	if len(vals) == 0 {
		if s, ok := kindSamples[c.kind]; ok {
			vals = []data.Value{s}
		}
	}
	w := 0
	o := g.opts(c)
	for _, v := range vals {
		if v == nil {
			continue
		}
		w = max(w, cells.Width(fmtx.Format(v, c.kind, o)))
	}
	return min(w, reserveCap)
}

func asFloat(v data.Value) (float64, bool) {
	switch v := v.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	}
	return 0, false
}

type footerResult struct {
	sums []data.ChunkSummary
	err  error
}

// startFooter reads the footer's column statistics once, after the first
// rows are shown, for reserved widths. The data layer caches the scan.
func (g *Grid) startFooter() tea.Cmd {
	if g.footerStarted {
		return nil
	}
	g.footerStarted = true
	ds := g.ds
	return g.env.Tasks.RunBackground("widths", func(ctx context.Context) tea.Msg {
		s, err := ds.FooterSummary(ctx)
		return footerResult{sums: s, err: err}
	})
}

func (g *Grid) onFooter(r footerResult) {
	if r.err != nil {
		return
	}
	top := map[string]bool{}
	for _, c := range g.ds.Columns() {
		if c.Arrow == nil || !arrow.IsNested(c.Arrow.ID()) {
			top[c.Name] = true
		}
	}
	g.footer = map[string][2]data.Value{}
	for _, s := range r.sums {
		if top[s.Path] && s.HasStats && s.Min != nil && s.Max != nil {
			g.footer[s.Path] = [2]data.Value{s.Min, s.Max}
		}
	}
	g.keepCursorInView(func() { g.reserve(nil) })
}
