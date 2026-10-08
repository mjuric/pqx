// Package plot is the Plot tab (Python pqx's plot tab and PlotControls in
// app.py): a settings line and a sky map (Mollweide) or a 2-D density of
// two numeric columns of the current view, binned in the background while
// the tab is shown.
package plot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/plots"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/analysis"
	"github.com/mjuric/pqx/go/internal/ui/cursorlist"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

const (
	// SkyRes is the sky map's binning, in degrees.
	SkyRes = 0.5
	// changeDelay and resizeDelay debounce replotting after a change of the
	// settings and of the window size.
	changeDelay = 200 * time.Millisecond
	resizeDelay = 300 * time.Millisecond
	// padX: #plot-panel has padding 0 2; the root's frame pads one cell.
	padX = 1
	// OriginX and OriginY are where the root draws a tab's body (the
	// drop-down opens under a field, in screen cells).
	OriginX, OriginY = 2, 5
)

// Pane is the Plot tab's body.
type Pane struct {
	env *kit.Env
	ctl controls

	visible, focused bool
	gen, seq         int

	status  styled.Text
	body    []styled.Text
	scroll  int
	areaW   int // the plot area at the last View (0 before it)
	areaH   int
	started string // settings of the plot running or shown, to skip a repeat
}

type debounceMsg struct {
	p   *Pane
	seq int
}

// pickedMsg is a drop-down's pick for field key.
type pickedMsg struct {
	p          *Pane
	key, value string
}

// result is a plot, as the task returns it.
type result struct {
	gen    int
	status styled.Text
	body   styled.Text
	err    error
}

// New makes the Plot pane.
func New(env *kit.Env) *Pane {
	p := &Pane{env: env, ctl: newControls()}
	p.initControls(false)
	return p
}

// Value is a setting: "mode", "x", "y", "centre" or "colour".
func (p *Pane) Value(key string) string { return p.ctl.value(key) }

// Status is the status line's text.
func (p *Pane) Status() string { return p.status.Plain }

func numeric(t arrow.DataType) bool {
	return t != nil && (arrow.IsInteger(t.ID()) || arrow.IsFloating(t.ID()))
}

// initControls fills the column pickers from the view's numeric columns
// and picks defaults: a sky map if there are sky columns, else the first
// two numeric columns. keep leaves x and y if both are still there
// (_init_plot_controls).
func (p *Pane) initControls(keep bool) {
	var names []string
	for _, c := range p.env.State.Columns {
		if numeric(c.Arrow) {
			names = append(names, c.Name)
		}
	}
	old := [2]string{p.ctl.value("x"), p.ctl.value("y")}
	p.ctl.setColumns(names)
	if keep && contains(names, old[0]) && contains(names, old[1]) {
		return
	}
	lon, lat := GuessSkyColumns(names)
	switch {
	case lon != "" && lat != "":
		p.ctl.values["mode"], p.ctl.values["x"], p.ctl.values["y"] = "sky", lon, lat
	case len(names) >= 2:
		p.ctl.values["mode"], p.ctl.values["x"], p.ctl.values["y"] = "xy", names[0], names[1]
	default:
		p.ctl.values["mode"], p.ctl.values["x"], p.ctl.values["y"] = "xy", "", ""
	}
	p.ctl.cur = min(p.ctl.cur, len(p.ctl.fields())-1)
}

// changed debounces a replot after a change of the settings.
func (p *Pane) debounced(d time.Duration) tea.Cmd {
	p.seq++
	seq := p.seq
	return tea.Tick(d, func(time.Time) tea.Msg { return debounceMsg{p, seq} })
}

// settings is what a plot depends on.
func (p *Pane) settings(w, h int) string {
	c := p.ctl
	return fmt.Sprint(c.value("mode"), "\x00", c.value("x"), "\x00", c.value("y"), "\x00", c.value("colour"), "\x00",
		c.value("centre"), "\x00", w, "\x00", h, "\x00", p.gen, "\x00", p.env.State.Sampling)
}

// size is the plot's size for the area (replot's w and h).
func (p *Pane) size() (int, int) { return max(20, p.areaW-1), max(8, p.areaH) }

// replot bins and draws the plot in the background (replot, plot_worker).
func (p *Pane) replot() tea.Cmd {
	if !p.visible {
		return nil // plots are computed lazily, when their tab is shown
	}
	x, y := p.ctl.value("x"), p.ctl.value("y")
	if x == "" || y == "" {
		p.body = []styled.Text{styled.New("no numeric columns to plot", p.env.Look.Style("dim"))}
		return nil
	}
	w, h := p.size()
	p.started = p.settings(w, h)
	st := p.env.State
	view, sample, gen := st.View, st.Sample(), p.gen
	mode, cmap := p.ctl.value("mode"), p.ctl.value("colour")
	center := 0.0
	if p.ctl.value("centre") == "180°" {
		center = 180
	}
	ds := p.env.DS
	look := p.env.Look
	dim, accent := look.Style("dim"), look.Style("accent")
	plook := analysis.PlotLook(look)
	scope := analysis.Scope(view)
	xs, ys := fmtx.Sanitize(x, false), fmtx.Sanitize(y, false)
	p.status = styled.Text{}
	p.status.Append(analysis.Spinner, accent)
	p.status.Append(" Binning "+xs+" × "+ys, styled.Style{})
	p.status.Append("   "+scope, dim)
	return p.env.Tasks.Run("plot", "binning "+xs+" × "+ys, true, func(ctx context.Context) tea.Msg {
		t0 := time.Now()
		r := &result{gen: gen}
		var n int64
		var detail string
		if mode == "sky" {
			g, err := ds.SkyCounts(ctx, view, x, y, SkyRes, sample)
			if err != nil {
				r.err = err
				return r
			}
			var nz, cells int64
			for _, row := range g.Counts {
				for _, c := range row {
					n += c
					if c > 0 {
						nz++
					}
				}
				cells += int64(len(row))
			}
			mw, _ := plots.SkyShape(w, h-3)
			r.body = plots.SkyMap(g.Counts, plots.SkyOpts{Width: mw, Height: h - 3, Cmap: cmap, Center: center}, plook)
			frac := 0.0
			if cells > 0 {
				frac = float64(nz) / float64(cells)
			}
			detail = "0.5° cells  ·  " + fmtx.Percent(frac, 1) + " of sky"
		} else {
			pw, ph := max(10, w-12), max(4, h-5)
			g, err := ds.XYCounts(ctx, view, x, y, 2*pw, 2*ph, sample, nil, nil)
			if err != nil {
				r.err = err
				return r
			}
			for _, row := range g.Counts {
				for _, c := range row {
					n += c
				}
			}
			r.body = plots.Density(g.Counts, g.X, g.Y, plots.DensityOpts{Cmap: cmap, XLabel: xs, YLabel: ys}, plook)
			detail = strconv.Itoa(2*pw) + "×" + strconv.Itoa(2*ph) + " bins"
		}
		rest := "   " + fmtx.HumanCount(float64(n)) + " rows  ·  " + detail + "  ·  " +
			strconv.FormatFloat(time.Since(t0).Seconds(), 'f', 2, 64) + " s  ·  " + scope
		if sample.Rows > 0 {
			r.status.Append("!", styled.Style{Fg: "yellow"})
			r.status.Append(" Binned "+xs+" × "+ys+", sampled", styled.Style{})
			r.status.Append(rest+"  ·  m scans everything", dim)
		} else {
			r.status.Append("✓", styled.Style{Fg: "green"})
			r.status.Append(" Binned "+xs+" × "+ys, styled.Style{})
			r.status.Append(rest, dim)
		}
		return r
	})
}

// Update implements kit.Pane.
func (p *Pane) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case kit.DoneMsg:
		if r, ok := m.Msg.(*result); ok && m.Tag == "plot" {
			if r.err != nil {
				p.started = ""
				return analysis.ShowError(p.env, r.err)
			}
			if r.gen == p.gen {
				p.status, p.body = r.status, r.body.Lines()
			}
		}
	case kit.CancelledMsg:
		for _, t := range m.Tags {
			if t == "plot" {
				p.started = ""
			}
		}
	case debounceMsg:
		if m.p == p && m.seq == p.seq {
			return p.replot()
		}
	case pickedMsg:
		if m.p == p && m.value != p.ctl.value(m.key) {
			p.ctl.set(m.key, m.value)
			return p.debounced(changeDelay)
		}
	case kit.ViewChangedMsg:
		p.gen++
		p.initControls(true)
		return p.replot()
	case kit.SamplingChangedMsg:
		return p.replot()
	case tea.WindowSizeMsg:
		if p.visible {
			return p.debounced(resizeDelay)
		}
	case tea.KeyPressMsg:
		return p.onKey(m)
	case tea.MouseClickMsg:
		if m.Button == tea.MouseLeft && m.Y == 0 {
			if key := p.ctl.at(m.X - padX); key != "" {
				for i, f := range p.ctl.fields() {
					if f == key {
						p.ctl.cur = i
					}
				}
				return p.open()
			}
		}
	case tea.MouseWheelMsg:
		if m.Button == tea.MouseWheelUp {
			p.scroll = max(0, p.scroll-3)
		} else {
			p.scroll += 3
		}
	}
	return nil
}

func (p *Pane) onKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "left":
		if p.ctl.step(-1) {
			return p.debounced(changeDelay)
		}
	case "right":
		if p.ctl.step(1) {
			return p.debounced(changeDelay)
		}
	case "tab":
		p.ctl.field(1)
	case "shift+tab":
		p.ctl.field(-1)
	case "enter", "space":
		return p.open()
	case "r":
		c := "0°"
		if p.ctl.value("centre") == "0°" {
			c = "180°"
		}
		p.ctl.set("centre", c)
		return p.debounced(changeDelay)
	case "m":
		return analysis.ToggleSampling(p.env)
	}
	return nil
}

// open opens a drop-down for the current field, right under its value.
func (p *Pane) open() tea.Cmd {
	key := p.ctl.fields()[p.ctl.cur]
	opts := p.ctl.options[key]
	if len(opts) == 0 {
		return nil
	}
	x := OriginX + padX + p.ctl.spanStart(key)
	y := OriginY + 1
	d := NewDropdown(p.env.Look, p.ctl.labels()[key], opts, p.ctl.value(key), x, y,
		func(v string) tea.Msg { return pickedMsg{p, key, v} })
	return kit.Send(kit.OpenDialogMsg{Dialog: d})
}

// setVisible is called when the tab is shown or hidden; shown, it plots
// unless the plot shown is already of these settings.
func (p *Pane) setVisible(v bool) tea.Cmd {
	p.visible = v
	if !v {
		return nil
	}
	if p.areaW == 0 {
		// not drawn yet: plot once the area's size is known
		return p.debounced(10 * time.Millisecond)
	}
	if w, h := p.size(); p.started == p.settings(w, h) {
		return nil
	}
	return p.replot()
}

// Focus implements kit.Focusable. Until the root tells panes which tab is
// shown, focus stands for it: the tab's body gets focus when it is shown.
func (p *Pane) Focus() tea.Cmd {
	p.focused = true
	return p.setVisible(true)
}

// Blur implements kit.Focusable.
func (p *Pane) Blur() {
	p.focused = false
	p.setVisible(false)
}

// Keys implements kit.Pane.
func (p *Pane) Keys() []kit.KeyHint {
	return []kit.KeyHint{{Key: "enter/click", Help: "pick"}, {Key: "tab", Help: "next field"},
		{Key: "← →", Help: "change"}, {Key: "r", Help: "rotate"}, {Key: "m", Help: "sampling"}, {Key: "e", Help: "export"}}
}

// View implements kit.Pane: the settings line, the plot centred below it,
// and the status line at the bottom.
func (p *Pane) View(w, h int) string {
	cw := max(1, w-2*padX)
	look := p.env.Look
	pad := strings.Repeat(" ", padX)
	p.areaW, p.areaH = cw, max(1, h-4)
	out := make([]string, 0, h)
	line := func(s string) { out = append(out, pad+cursorlist.Fit(s, cw)+pad) }
	line(look.Render(p.ctl.render(look, p.focused, cw)))
	line("")
	bw := 0
	for _, t := range p.body {
		bw = max(bw, ansi.StringWidth(t.Plain))
	}
	left := strings.Repeat(" ", max(0, (cw-bw)/2))
	p.scroll = max(0, min(p.scroll, len(p.body)-p.areaH))
	for i := 0; i < p.areaH; i++ {
		s := ""
		if j := p.scroll + i; j < len(p.body) {
			s = left + look.Render(p.body[j])
		}
		line(s)
	}
	line("")
	line(look.Render(p.status))
	return strings.Join(out[:min(len(out), h)], "\n")
}
