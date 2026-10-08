// Package stats is the Stats tab (Python pqx's stats tab in app.py): a
// profile of one column of the current view (head line, a two-column
// summary, a histogram or the most frequent values) and the list of the
// view's columns beside it, linked to the current column.
package stats

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/apache/arrow-go/v18/arrow"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/analysis"
	"github.com/mjuric/pqx/go/internal/ui/cursorlist"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Settings of the histogram (Python's _hist_bins and its limits).
const (
	DefaultBins = 60
	MinBins     = 5
	MaxBins     = 400
	// SideWidth is the column list's panel width, borders included
	// (#stats-cols-panel), and ListWidth its items' width.
	SideWidth = 36
	ListWidth = 32
	// debounce is how long a highlight in the column list waits before
	// profiling (the cursor may be passing through).
	debounce = 150 * time.Millisecond
	// histMinDistinct: columns with more distinct values get a histogram,
	// the others their most frequent values.
	histMinDistinct = 12
)

// Pane is the Stats tab's body.
type Pane struct {
	env *kit.Env

	list  cursorlist.List
	col   string // the column profiled or to profile (_stats_col), "" for none
	shown string // the column whose profile is shown (_stats_shown)
	stale bool   // the profile shown is out of date (_stats_stale)

	bins       int
	logY, logX bool

	visible, focused bool
	gen              int // bumped on each view change; results of older views are dropped
	seq              int // debounce generation
	running          string

	head    []styled.Text // name line and status
	summary []styled.Text // the two-column summary and the most frequent values
	plot    []styled.Text // the histogram
	last    *result       // the last profile rendered (to reformat in place)
	scroll  int           // the body's scroll offset
	bodyW   int           // the body's content width at the last View
	bodyH   int
	paneW   int // the body's width at the last Panels
}

// result is a profile, as the task returns it.
type result struct {
	gen     int
	name    string
	col     data.Column
	st      data.ColumnStats
	hist    *data.Histogram
	elapsed time.Duration
	err     error
}

type debounceMsg struct {
	p    *Pane
	seq  int
	name string
}

// New makes the Stats pane.
func New(env *kit.Env) *Pane {
	p := &Pane{env: env, bins: DefaultBins, stale: true, bodyW: 80}
	p.list.Width = ListWidth
	p.buildList()
	return p
}

// numericOrTemporal reports whether a column gets a histogram (integer or
// floating point, timestamp or date), and whether it is temporal.
func numericOrTemporal(t arrow.DataType) (numeric, temporal bool) {
	if t == nil {
		return false, false
	}
	id := t.ID()
	numeric = arrow.IsInteger(id) || arrow.IsFloating(id)
	temporal = id == arrow.TIMESTAMP || id == arrow.DATE32 || id == arrow.DATE64
	return
}

func isInt(t arrow.DataType) bool { return t != nil && arrow.IsInteger(t.ID()) }

// buildList fills the column list from the view's columns
// (_build_stats_list); the highlight comes back with sync.
func (p *Pane) buildList() {
	dim := p.env.Look.Style("dim")
	items := make([]cursorlist.Item, 0, len(p.env.State.Columns))
	found := false
	for _, c := range p.env.State.Columns {
		var t styled.Text
		name := fmtx.Sanitize(c.Name, false)
		if n := len([]rune(name)); n < 18 {
			name += strings.Repeat(" ", 18-n)
		}
		t.Append(name, styled.Style{Bold: true})
		t.Append("  ", styled.Style{})
		t.Append(fmtx.ShortType(c.Arrow), dim)
		items = append(items, cursorlist.Item{ID: c.Name, Text: t})
		found = found || c.Name == p.col
	}
	p.list.SetItems(items)
	if !found {
		p.col = ""
	}
}

// Column is the column profiled or about to be (for tests and the root).
func (p *Pane) Column() string { return p.col }

// Shown is the column whose profile is shown.
func (p *Pane) Shown() string { return p.shown }

// sync highlights the current column in the list and profiles it unless its
// profile is already shown (_sync_stats).
func (p *Pane) sync() tea.Cmd {
	st := p.env.State
	if _, ok := st.Column(st.Current); ok {
		p.col = st.Current
	} else if p.col == "" && len(st.Columns) > 0 {
		p.col = st.Columns[0].Name
	}
	if p.col == "" {
		return nil
	}
	p.list.Highlight(p.list.Index(p.col)) // p.col is set first: not taken for a move
	if p.stale || p.shown != p.col {
		p.seq++ // a highlight's debounced profile is superseded by this one
		return p.compute(p.col)
	}
	return nil
}

// refresh is _refresh_analysis for Stats: recompute now if shown, else
// when next shown.
func (p *Pane) refresh() tea.Cmd {
	p.stale = true
	if p.visible {
		return p.sync()
	}
	p.summary = nil
	return nil
}

// setVisible is called when the tab is shown or hidden.
func (p *Pane) setVisible(v bool) tea.Cmd {
	p.visible = v
	if v {
		return p.sync()
	}
	return nil
}

// compute profiles name in the background (compute_stats).
func (p *Pane) compute(name string) tea.Cmd {
	st := p.env.State
	col, ok := st.Column(name)
	if !ok {
		return nil
	}
	if p.running == name && p.env.Tasks.Running("stats") {
		return nil
	}
	p.running = name
	view, sample, gen := st.View, st.Sample(), p.gen
	bins, logX := p.bins, p.logX
	ds := p.env.DS
	p.head = p.headLines(name, p.runningStatus(name))
	numeric, temporal := numericOrTemporal(col.Arrow)
	return p.env.Tasks.Run("stats", "profiling "+fmtx.Sanitize(name, false), true, func(ctx context.Context) tea.Msg {
		t0 := time.Now()
		r := &result{gen: gen, name: name, col: col}
		r.st, r.err = ds.ColumnStats(ctx, view, name, sample)
		if r.err == nil && (numeric || temporal) && r.st.Count-r.st.Nulls > 0 && r.st.Distinct > histMinDistinct {
			var h data.Histogram
			h, r.err = ds.Histogram(ctx, view, name, data.HistOptions{Bins: bins, Sample: sample,
				Log: logX && !temporal, Temporal: temporal})
			r.hist = &h
		}
		r.elapsed = time.Since(t0)
		return r
	})
}

// Update implements kit.Pane.
func (p *Pane) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case kit.TabChangedMsg:
		return p.setVisible(m.Tab == kit.TabStats)
	case kit.DoneMsg:
		if r, ok := m.Msg.(*result); ok && m.Tag == "stats" {
			return p.done(r)
		}
	case kit.CancelledMsg:
		for _, t := range m.Tags {
			if t == "stats" {
				p.running = ""
			}
		}
	case debounceMsg:
		if m.p == p && m.seq == p.seq && p.visible {
			return p.compute(m.name)
		}
	case kit.ViewChangedMsg:
		p.gen++
		p.running = ""
		p.buildList()
		return p.refresh()
	case kit.SamplingChangedMsg:
		return p.refresh()
	case kit.ColumnChangedMsg:
		if m.From != "stats" && p.visible {
			return p.sync()
		}
	case kit.FormatChangedMsg:
		// reformat what's shown; no need to re-profile
		if r := p.last; r != nil && r.gen == p.gen && r.name == m.Column && !p.stale {
			p.render(r)
		}
	case tea.KeyPressMsg:
		return p.onKey(m)
	case tea.MouseWheelMsg:
		return p.onWheel(m)
	case tea.MouseClickMsg:
		return p.onClick(m)
	}
	return nil
}

func (p *Pane) onKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "l":
		p.logY = !p.logY
		return p.refresh()
	case "L":
		p.logX = !p.logX
		return p.refresh()
	case "[":
		return p.setBins(-1)
	case "]":
		return p.setBins(1)
	}
	if used, moved := p.list.Key(k); used && moved {
		return p.highlighted()
	}
	return nil
}

// setBins is action_bins: ×1.5 or ÷1.5 within MinBins..MaxBins.
func (p *Pane) setBins(d int) tea.Cmd {
	f := 1 / 1.5
	if d > 0 {
		f = 1.5
	}
	p.bins = max(MinBins, min(MaxBins, int(float64(p.bins)*f)))
	return p.refresh()
}

// highlighted is stats_col_highlighted: the user moved the highlight.
func (p *Pane) highlighted() tea.Cmd {
	name := p.list.HighlightedID()
	if name == "" || name == p.col {
		return nil
	}
	p.col = name
	if !p.visible {
		return nil
	}
	p.env.State.Current = name
	p.seq++
	seq := p.seq
	return tea.Batch(kit.Send(kit.ColumnChangedMsg{From: "stats"}),
		tea.Tick(debounce, func(time.Time) tea.Msg { return debounceMsg{p, seq, name} }))
}

func (p *Pane) done(r *result) tea.Cmd {
	if r.name == p.running {
		p.running = ""
	}
	if r.err != nil {
		return analysis.ShowError(p.env, r.err)
	}
	if r.gen != p.gen {
		return nil
	}
	p.render(r)
	return nil
}

// Focus implements kit.Focusable. The pane has focus only while its tab
// shows (visibility itself follows kit.TabChangedMsg).
func (p *Pane) Focus() tea.Cmd {
	p.focused = true
	if p.visible {
		return nil
	}
	return p.setVisible(true)
}

// Blur implements kit.Focusable: the tab may still show (focus on the
// filter), so the pane stays visible.
func (p *Pane) Blur() { p.focused = false }

// Keys implements kit.Pane.
func (p *Pane) Keys() []kit.KeyHint {
	return []kit.KeyHint{{Key: "↑↓", Help: "column"}, {Key: "l", Help: "log counts"}, {Key: "L", Help: "log values"},
		{Key: "[ ]", Help: "bins"}, {Key: "m", Help: "sampling"}}
}
