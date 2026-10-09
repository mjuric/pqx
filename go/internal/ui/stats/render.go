package stats

import (
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/plots"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/analysis"
	"github.com/mjuric/pqx/go/internal/ui/cursorlist"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Layout of the body (#stats-body has padding 0 2; the root's frame pads
// one cell, so the pane pads one more on each side).
const (
	padX      = 1
	histH     = 10 // histogram rows
	topBarW   = 30 // the most-frequent bars' full width
	topValueW = 40
)

var (
	bold    = styled.Style{Bold: true}
	nameSt  = styled.Style{Bold: true, Fg: "cyan"}
	yellow  = styled.Style{Fg: "yellow"}
	green   = styled.Style{Fg: "green"}
	eighths = []rune(" ▏▎▍▌▋▊▉")
)

// fileColumn is the file's column by name (for its unit and description;
// a SQL result's own columns have none).
func (p *Pane) fileColumn(name string) (data.Column, bool) {
	for _, c := range p.env.DS.Columns() {
		if c.Name == name {
			return c, true
		}
	}
	return data.Column{}, false
}

// headLines is _stats_head: the name, type, [unit] and description, then
// the status.
func (p *Pane) headLines(name string, status styled.Text) []styled.Text {
	dim := p.env.Look.Style("dim")
	var t styled.Text
	t.Append(fmtx.Sanitize(name, false), nameSt)
	if c, ok := p.env.State.Column(name); ok && c.Arrow != nil {
		t.Append("   "+fmtx.ShortType(c.Arrow), dim)
	}
	if fc, ok := p.fileColumn(name); ok {
		if fc.Unit != "" {
			t.Append("   ["+fmtx.Sanitize(fc.Unit, false)+"]", styled.Style{})
		}
		if fc.Description != "" {
			t.Append("   "+fmtx.Sanitize(fc.Description, true), dim)
		}
	}
	return []styled.Text{t, status}
}

func (p *Pane) runningStatus(name string) styled.Text {
	var t styled.Text
	t.Append(analysis.Spinner, p.env.Look.Style("accent"))
	t.Append(" Profiling "+fmtx.Sanitize(name, false), styled.Style{})
	t.Append("   "+analysis.Scope(p.env.State.View), p.env.Look.Style("dim"))
	return t
}

// cell is one cell of a grid; own says the text's Justify wins over the
// column's (a formatted value: numbers right, text left).
type cell struct {
	t   styled.Text
	own bool
}

func txt(s string) cell { return cell{t: styled.Text{Plain: s}} }

type column struct {
	style   styled.Style
	justify styled.Justify
	maxW    int // 0 for no limit
}

// grid lays rows out as Rich's Table.grid(padding=(0, 2)): columns as wide
// as their widest cell, two cells apart.
func grid(cols []column, rows [][]cell) []styled.Text {
	widths := make([]int, len(cols))
	for _, r := range rows {
		for i, c := range r {
			w := ansi.StringWidth(c.t.Plain)
			if cols[i].maxW > 0 {
				w = min(w, cols[i].maxW)
			}
			widths[i] = max(widths[i], w)
		}
	}
	// Rich gives an empty last column one cell
	if n := len(widths); n > 0 && widths[n-1] == 0 {
		widths[n-1] = 1
	}
	out := make([]styled.Text, 0, len(rows))
	for _, r := range rows {
		var line styled.Text
		for i, c := range r {
			if i > 0 {
				// Rich pads a cell with its column's style: the gap takes
				// the style of the column to its left
				line.Append("  ", cols[i-1].style)
			}
			t := c.t
			if n := ansi.StringWidth(t.Plain); n > widths[i] {
				t.Plain = ansi.Truncate(t.Plain, widths[i], "…")
			}
			j := cols[i].justify
			if c.own {
				j = t.Justify
			}
			gap := widths[i] - ansi.StringWidth(t.Plain)
			left := 0
			switch j {
			case styled.Right:
				left = gap
			case styled.Center:
				left = gap / 2
			}
			var u styled.Text
			u.Append(strings.Repeat(" ", left), styled.Style{})
			u.AppendText(t)
			u.Append(strings.Repeat(" ", gap-left), styled.Style{})
			if cols[i].style != (styled.Style{}) {
				// the column's style under the cell's own
				u.Spans = append([]styled.Span{{Start: 0, End: len([]rune(u.Plain)), Style: cols[i].style}}, u.Spans...)
			}
			line.AppendText(u)
		}
		out = append(out, line)
	}
	return out
}

// render shows a profile (_render_stats).
func (p *Pane) render(r *result) {
	p.last = r
	p.stale = false
	p.shown = r.name
	look := p.env.Look
	dim := look.Style("dim")
	st := r.st
	name := r.name
	scope := analysis.Scope(p.env.State.View)
	secs := strconv.FormatFloat(r.elapsed.Seconds(), 'f', 2, 64)

	var status styled.Text
	if st.Sampled {
		status.Append("!", yellow)
		status.Append(" Profiled "+fmtx.Sanitize(name, false)+", sampled", styled.Style{})
		status.Append("   "+fmtx.HumanCount(float64(st.Count))+" rows  ·  "+secs+" s  ·  "+scope+"  ·  m scans everything", dim)
	} else {
		status.Append("✓", green)
		status.Append(" Profiled "+fmtx.Sanitize(name, false), styled.Style{})
		status.Append("   "+analysis.Commas(st.Count)+" rows  ·  "+secs+" s  ·  "+scope, dim)
	}
	p.head = p.headLines(name, status)

	kind := fmtx.KindFor(name, r.col.Arrow, r.col.Unit)
	opts := fmtx.Opts{Width: fmtx.DefaultWidth, Override: p.env.State.Override(name)}
	fm := func(v data.Value) cell { return cell{t: fmtx.Cell(v, kind, opts), own: true} }
	pct := func(n int64) string { return fmtx.Percent(float64(n), float64(st.Count)) }
	isint := isInt(r.col.Arrow)
	num := func(v float64) cell {
		if isint {
			return txt(intText(v))
		}
		return fm(v)
	}
	unit := ""
	if fc, ok := p.fileColumn(name); ok {
		unit = fc.Unit
	}

	var left [][3]cell
	sampled := ""
	if st.Sampled {
		sampled = "sampled"
	}
	left = append(left, [3]cell{txt("rows"), txt(analysis.Commas(st.Count)), txt(sampled)})
	nullPct := ""
	if st.Nulls != 0 {
		nullPct = pct(st.Nulls)
	}
	left = append(left, [3]cell{txt("nulls"), txt(analysis.Commas(st.Nulls)), txt(nullPct)})
	if st.NaNs >= 0 {
		nanPct := ""
		if st.NaNs != 0 {
			nanPct = pct(st.NaNs)
		}
		left = append(left, [3]cell{txt("NaN"), txt(analysis.Commas(st.NaNs)), txt(nanPct)})
	}
	if st.Distinct >= 0 {
		d := analysis.Commas(st.Distinct)
		if !st.DistinctExact {
			d = "≈" + d
		}
		left = append(left, [3]cell{txt("distinct"), txt(d), txt("")})
	}
	if st.Min != nil {
		left = append(left, [3]cell{txt("min"), fm(st.Min), txt(fmtx.Derived(name, kind, st.Min, unit))})
		left = append(left, [3]cell{txt("max"), fm(st.Max), txt(fmtx.Derived(name, kind, st.Max, unit))})
	}
	if st.Mean != nil {
		c := fm(*st.Mean)
		if isint {
			c = txt(analysis.FmtFloat(*st.Mean, 15))
		}
		left = append(left, [3]cell{txt("mean"), c, txt("")})
	}
	if st.Std != nil {
		left = append(left, [3]cell{txt("std"), txt(fmtx.Format(*st.Std, fmtx.KindErr, fmtx.Opts{Width: fmtx.DefaultWidth})), txt("")})
	}
	var right [][3]cell
	qs := make([]float64, 0, len(st.Quantiles))
	for q := range st.Quantiles {
		qs = append(qs, q)
	}
	sort.Float64s(qs)
	for _, q := range qs {
		label := ""
		if q == 0.5 {
			label = "median"
		}
		right = append(right, [3]cell{txt("p" + strconv.FormatFloat(q*100, 'g', 6, 64)), num(st.Quantiles[q]), txt(label)})
	}
	cols := []column{{dim, styled.Right, 0}, {styled.Style{}, styled.Right, 0}, {dim, styled.Left, 0},
		{dim, styled.Right, 0}, {styled.Style{}, styled.Right, 0}, {dim, styled.Left, 0}}
	var rows [][]cell
	for i := 0; i < max(len(left), len(right)); i++ {
		a := [3]cell{txt(""), txt(""), txt("")}
		b := a
		if i < len(left) {
			a = left[i]
		}
		if i < len(right) {
			b = right[i]
		}
		rows = append(rows, []cell{a[0], a[1], a[2], b[0], b[1], b[2]})
	}
	summary := []styled.Text{{}}
	summary = append(summary, grid(cols, rows)...)
	if len(st.Top) > 0 && r.hist == nil {
		summary = append(summary, styled.Text{}, styled.New("most frequent", bold))
		var m int64 = 1
		for _, vc := range st.Top {
			m = max(m, vc.Count)
		}
		var trows [][]cell
		for _, vc := range st.Top {
			trows = append(trows, []cell{fm(vc.Value), txt(bar(vc.Count, m)), txt(analysis.Commas(vc.Count)), txt(pct(vc.Count))})
		}
		tcols := []column{{styled.Style{}, styled.Right, topValueW}, {styled.Style{}, styled.Left, 0},
			{styled.Style{}, styled.Right, 0}, {dim, styled.Right, 0}}
		summary = append(summary, grid(tcols, trows)...)
	}
	p.summary = summary

	p.plot = nil
	if h := r.hist; h != nil {
		_, temporal := numericOrTemporal(r.col.Arrow)
		width := max(30, p.bodyW-6)
		label := fmtx.Sanitize(name, false)
		o := plots.HistOpts{Width: width, Height: histH, LogY: p.logY, LogX: p.logX && !temporal, XLabel: label}
		if temporal {
			o.XLabel += "  (UTC)"
			o.XFmt = epochLabel
		}
		plot := []styled.Text{{}, styled.New("distribution", bold)}
		plot = append(plot, plots.Histogram(h.Edges, h.Counts, o, analysis.PlotLook(look)).Lines()...)
		foot := strconv.Itoa(len(h.Counts)) + " bins"
		if p.logY {
			foot += "  ·  log counts"
		}
		if p.logX && !temporal {
			foot += "  ·  log values"
		}
		p.plot = append(plot, styled.New(foot, dim))
	}
}

// bar is a count as a bar of eighth blocks, topBarW cells for the most
// frequent value.
func bar(n, m int64) string {
	w := float64(n) / float64(m) * topBarW
	s := strings.Repeat("█", int(w))
	if w < topBarW {
		s += string(eighths[int((w-math.Floor(w))*8)])
	}
	return strings.TrimRight(s, " ")
}

// intText is an integer column's quantile: rounded, with thousands
// separators below 1e15.
func intText(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	r := math.RoundToEven(v)
	if math.Abs(v) < 1e15 {
		return analysis.Commas(int64(r))
	}
	i, _ := new(big.Float).SetFloat64(r).Int(nil)
	return i.String()
}

// epochLabel is a temporal histogram's axis label (_epoch_label).
func epochLabel(v float64) string {
	s := math.Floor(v)
	return time.Unix(int64(s), int64((v-s)*1e9)).UTC().Format("2006-01-02 15:04")
}

// View implements kit.Pane: the profile alone (the root draws the tab
// through Panels).
func (p *Pane) View(w, h int) string { return p.bodyView(w, h) }

// Panels implements kit.Paneled: the profile on the left, the column list
// in its own panel at the right, one column apart (Python's #stats-body
// and #stats-cols-panel). The list has focus.
func (p *Pane) Panels(w, h int) []kit.Panel {
	p.paneW = w
	if !p.sided(w) {
		return []kit.Panel{{X: 0, Y: 0, W: w, H: h, Content: p.bodyView(max(1, w-4), max(0, h-2))}}
	}
	bw := w - SideWidth - 1
	return []kit.Panel{
		{X: 0, Y: 0, W: bw, H: h, Content: p.bodyView(bw-4, max(0, h-2))},
		{X: w - SideWidth, Y: 0, W: SideWidth, H: h, Focused: true, Title: p.sideTitle(),
			Content: p.list.View(SideWidth-4, max(0, h-2), p.env.Look.Render)},
	}
}

// sided reports whether a body w wide has room for the column list.
func (p *Pane) sided(w int) bool { return w >= SideWidth+20 }

// bodyLines is everything in the body, unclipped, for content width cw.
func (p *Pane) bodyLines(cw int) []styled.Text {
	var out []styled.Text
	for _, t := range p.head {
		out = append(out, analysis.Wrap(t, cw)...)
	}
	out = append(out, p.summary...)
	out = append(out, p.plot...)
	return out
}

// bodyView draws the profile in w × h cells (the panel's inside).
func (p *Pane) bodyView(w, h int) string {
	cw := max(1, w-2*padX)
	p.bodyW, p.bodyH = cw, h
	lines := p.bodyLines(cw)
	p.scroll = max(0, min(p.scroll, len(lines)-h))
	out := make([]string, h)
	pad := strings.Repeat(" ", padX)
	for i := range out {
		s := ""
		if j := p.scroll + i; j < len(lines) {
			s = p.env.Look.Render(lines[j])
		}
		out[i] = pad + cursorlist.Fit(s, cw) + pad
	}
	return strings.Join(out, "\n")
}

// sideTitle is the column list panel's title: "columns  N".
func (p *Pane) sideTitle() styled.Text {
	return styled.New("columns  "+strconv.Itoa(p.list.Len()), p.env.Look.Style("dim"))
}

// inSide reports whether body column x is in the column list's panel.
func (p *Pane) inSide(x, w int) bool { return p.sided(w) && x >= w-SideWidth }

func (p *Pane) onWheel(m tea.MouseWheelMsg) tea.Cmd {
	d := 3
	if m.Button == tea.MouseWheelUp {
		d = -3
	}
	if p.inSide(m.X, p.paneW) {
		p.list.Scroll(d)
		return nil
	}
	p.scroll = max(0, p.scroll+d)
	return nil
}

// onClick: a click on an item of the list highlights it.
func (p *Pane) onClick(m tea.MouseClickMsg) tea.Cmd {
	if m.Button != tea.MouseLeft || !p.inSide(m.X, p.paneW) {
		return nil
	}
	if i := p.list.At(m.Y - 1); i >= 0 {
		p.list.Highlight(i)
		return p.highlighted()
	}
	return nil
}
