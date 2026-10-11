// Package plots is the port of Python pqx's plots.py: the Stats histogram,
// the Plot tab's density map and Mollweide sky map, colour bars and
// colormaps, drawn as styled text. Part of the contract
// (docs/design/go-port.md); WP4 implements it, with Python's outputs
// (go/testdata/golden/plots.json) as the spec.
package plots

import (
	"math"
	"strings"

	"github.com/mjuric/pqx/go/internal/styled"
)

// Colormaps are the colormaps, in the Plot tab's order; DefaultCmap is the first.
var Colormaps = []string{"magma", "viridis", "inferno", "plasma", "gray", "terminal"}

const DefaultCmap = "magma"

// Look is what plots need from the theme.
type Look struct {
	DarkBG bool         // the terminal's background is dark
	Accent styled.Color // the focus colour, for the "terminal" colormap
	Dim    styled.Style // secondary text (faint, or bright black)
}

// HistOpts configure Histogram.
type HistOpts struct {
	Width, Height int // default 70 × 12
	Color         styled.Color
	LogY, LogX    bool
	XLabel        string
	XFmt          func(float64) string // axis label text; nil for numbers
}

const eighths = " ▁▂▃▄▅▆▇█"

var eighthRunes = []rune(eighths)

// Histogram draws a vertical bar histogram with eighth-block resolution
// and labelled axes (render_histogram).
func Histogram(edges []float64, counts []int64, o HistOpts, l Look) styled.Text {
	dim := l.dim()
	var t builder
	if len(counts) == 0 {
		t.add("(no finite values)", dim.Plus(styled.Style{Italic: true}))
		return t.text()
	}
	width, height := o.Width, o.Height
	if width == 0 {
		width = 70
	}
	if height == 0 {
		height = 12
	}
	nb := len(counts)
	cf := make([]float64, nb)
	vals := make([]float64, nb)
	var cmax, total int64
	vmax := math.Inf(-1)
	for i, c := range counts {
		cf[i] = float64(c)
		vals[i] = cf[i]
		if o.LogY {
			vals[i] = math.Log10(cf[i] + 1)
		}
		vmax = max(vmax, vals[i])
		if i == 0 || c > cmax {
			cmax = c
		}
		total += c
	}
	if vmax == 0 {
		vmax = 1
	}
	ylab0, ylab1 := commas(cmax), commas(total)+" tot"
	yw := max(runeLen(ylab0), runeLen(ylab1)) + 1
	plotW := max(10, width-yw-1)
	colBin := make([]int, plotW)
	levels := make([]float64, plotW)
	for i := range plotW {
		colBin[i] = min(int(float64(i*nb)/float64(plotW)), nb-1)
		levels[i] = vals[colBin[i]] / vmax * float64(height) * 8
	}
	barSt := styled.Style{Fg: o.Color}
	row := make([]rune, plotW)
	for r := range height {
		base := float64((height - 1 - r) * 8)
		switch {
		case r == 0:
			t.add(rjust(ylab0, yw-1)+" ┤", dim)
		case r == height-1:
			t.add(strings.Repeat(" ", yw-1)+" ┤", dim)
		default:
			t.add(strings.Repeat(" ", yw-1)+" │", dim)
		}
		for i, lv := range levels {
			k := int(math.RoundToEven(pyMin(8, pyMax(0, lv-base))))
			if k == 0 && r == height-1 && cf[colBin[i]] > 0 {
				k = 1 // never let a populated bin vanish
			}
			row[i] = eighthRunes[k]
		}
		t.add(string(row), barSt)
		t.add("\n", styled.Style{})
	}
	t.add(strings.Repeat(" ", yw)+"└"+strings.Repeat("─", plotW)+"\n", dim)
	lo, hi := edges[0], edges[len(edges)-1]
	mid := (lo + hi) / 2
	var labs []string
	if o.XFmt == nil && o.LogX {
		labs = axisLabels([]float64{math.Pow(10, lo), math.Pow(10, mid), math.Pow(10, hi)}, nil)
	} else {
		labs = axisLabels([]float64{lo, mid, hi}, o.XFmt)
	}
	t.add(strings.Repeat(" ", yw+1)+string(placeLabels(plotW, labs))+"\n", dim)
	note := ""
	if o.LogY {
		note = "log₁₀(count+1) scale"
	}
	lab := o.XLabel
	if o.LogX {
		lab += " (log x)"
	}
	lab = strings.TrimSpace(lab)
	var parts []string
	for _, s := range []string{lab, note} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if footer := strings.Join(parts, "  "); footer != "" {
		t.add(strings.Repeat(" ", yw+1)+center(footer, plotW), dim.Plus(styled.Style{Italic: true}))
	}
	return t.text()
}

// placeLabels puts three labels at the left end, the middle and the right
// end of a w-wide axis (a label wider than the axis widens it, as in Python).
func placeLabels(w int, labs []string) []rune {
	axis := []rune(strings.Repeat(" ", w))
	a, m, b := labs[0], labs[1], labs[2]
	for _, sp := range []struct {
		s string
		p int
	}{{a, 0}, {m, w/2 - runeLen(m)/2}, {b, w - runeLen(b)}} {
		p := max(0, min(w-runeLen(sp.s), sp.p))
		axis = splice(axis, p, sp.s)
	}
	return axis
}

// niceNum is a tick value in digits significant digits (_nice_num).
func niceNum(v float64, digits int) string {
	if v == 0 {
		return "0"
	}
	a := math.Abs(v)
	if a >= 1e6 || a < 1e-3 {
		return pyG(v, digits)
	}
	if a >= 100 && digits <= 3 {
		return pyF(v, 0)
	}
	return pyG(v, digits)
}

// axisLabels formats tick values with just enough precision that they are
// all distinct, or with fmt when it isn't nil (axis_labels).
func axisLabels(values []float64, fmt func(float64) string) []string {
	labs := make([]string, len(values))
	if fmt != nil {
		for i, v := range values {
			labs[i] = fmt(v)
		}
		return labs
	}
	want := distinct(values)
	for d := 3; d < 18; d++ {
		seen := map[string]bool{}
		for i, v := range values {
			labs[i] = niceNum(v, d)
			seen[labs[i]] = true
		}
		if len(seen) == want {
			return labs
		}
	}
	for i, v := range values {
		labs[i] = pyRepr(v)
	}
	return labs
}

// distinct is len(set(values)) for floats (0.0 and -0.0 are one value).
func distinct(values []float64) int {
	n := 0
	for i, v := range values {
		dup := false
		for _, u := range values[:i] {
			if u == v {
				dup = true
				break
			}
		}
		if !dup {
			n++
		}
	}
	return n
}

// Sparkline is counts as a one-line bar of eighth blocks (sparkline).
func Sparkline(counts []int64, width int) string {
	nb := len(counts)
	if nb == 0 {
		return ""
	}
	v := make([]float64, max(width, 0))
	m := 0.0
	for i := range v {
		v[i] = float64(counts[min(int(float64(i*nb)/float64(width)), nb-1)])
		if i == 0 || v[i] > m {
			m = v[i]
		}
	}
	if m == 0 {
		m = 1
	}
	out := make([]rune, len(v))
	for i, x := range v {
		out[i] = ' '
		if x > 0 {
			out[i] = eighthRunes[int(math.RoundToEven(x/m*8))]
		}
	}
	return string(out)
}

// Colorbar is a log-scale colour legend (colorbar).
func Colorbar(cmap string, vmin, vmax float64, unit, label string, width int, l Look) styled.Text {
	var t builder
	if !(vmax > 0) {
		return t.text()
	}
	dim := l.dim()
	lo := math.Log(pyMax(vmin, 1e-300))
	hi := math.Log(vmax)
	var vals []float64
	if vmax <= vmin {
		vals = []float64{vmax}
	} else {
		for i := range 6 {
			vals = append(vals, math.Exp(lo+(hi-lo)*float64(i)/5))
		}
	}
	type part struct {
		v float64
		s string
	}
	var parts []part
	seen := map[string]bool{}
	for _, v := range vals {
		s := fmtDensity(v)
		if !seen[s] {
			seen[s] = true
			parts = append(parts, part{v, s})
		}
	}
	vis := label + ": "
	for k, p := range parts {
		if k > 0 {
			vis += "  "
		}
		vis += "█ " + p.s
	}
	if unit != "" {
		vis += "  " + unit
	}
	pad := max(0, floorDiv(width-runeLen(vis), 2))
	t.add(strings.Repeat(" ", pad)+label+": ", dim)
	for k, p := range parts {
		f := 1.0
		if !(vmax <= vmin) {
			f = (math.Log(p.v) - lo) / (hi - lo)
		}
		t.add("█", densityStyle(cmap, f, l.DarkBG, l))
		s := " " + p.s
		if k < len(parts)-1 {
			s += "  "
		}
		t.add(s, styled.Style{})
	}
	if unit != "" {
		t.add("  "+unit, dim)
	}
	return t.text()
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// DensityOpts configure Density.
type DensityOpts struct {
	Cmap           string
	XLabel, YLabel string
}

// Density draws counts[y][x] (2·h rows × 2·w columns, row 0 the lowest y)
// as a quadrant-glyph density plot over xlim × ylim (render_density).
func Density(counts [][]int64, xlim, ylim [2]float64, o DensityOpts, l Look) styled.Text {
	cmap := o.Cmap
	if cmap == "" {
		cmap = DefaultCmap
	}
	dim := l.dim()
	sh := len(counts)
	sw := 0
	if sh > 0 {
		sw = len(counts[0])
	}
	h, w := sh/2, sw/2
	g := func(y, x int) int64 { return counts[sh-1-y][x] } // row 0 = top
	cell := make([]int64, h*w)
	var vmin, vmax int64
	any := false
	for r := range h {
		for c := range w {
			v := max(g(2*r, 2*c), g(2*r, 2*c+1), g(2*r+1, 2*c), g(2*r+1, 2*c+1))
			cell[r*w+c] = v
			if v > 0 {
				if !any || v < vmin {
					vmin = v
				}
				if !any || v > vmax {
					vmax = v
				}
				any = true
			}
		}
	}
	ylabs := axisLabels([]float64{ylim[1], (ylim[0] + ylim[1]) / 2, ylim[0]}, nil)
	yw := max(runeLen(ylabs[0]), runeLen(ylabs[1]), runeLen(ylabs[2]))
	lmin, lmax := math.Log(float64(vmin)), math.Log(float64(vmax))
	var t builder
	styles := map[int64]styled.Style{} // by count
	for r := range h {
		lab := ""
		switch {
		case r == 0:
			lab = ylabs[0]
		case r == h-1:
			lab = ylabs[2]
		case r == h/2:
			lab = ylabs[1]
		}
		if lab != "" {
			t.add(rjust(lab, yw)+" ┤", dim)
		} else {
			t.add(strings.Repeat(" ", yw)+" │", dim)
		}
		for c := range w {
			y0, x0 := 2*r, 2*c
			ch := quadrant(g(y0, x0) > 0, g(y0, x0+1) > 0, g(y0+1, x0) > 0, g(y0+1, x0+1) > 0)
			if ch == ' ' {
				t.add(" ", styled.Style{})
				continue
			}
			v := cell[r*w+c]
			st, ok := styles[v]
			if !ok {
				f := 1.0
				if vmax > vmin {
					f = (math.Log(float64(v)) - lmin) / (lmax - lmin)
				}
				st = densityStyle(cmap, f, l.DarkBG, l)
				styles[v] = st
			}
			t.add(string(ch), st)
		}
		t.add("\n", styled.Style{})
	}
	t.add(strings.Repeat(" ", yw)+" └"+strings.Repeat("─", w)+"\n", dim)
	xl := axisLabels([]float64{xlim[0], (xlim[0] + xlim[1]) / 2, xlim[1]}, nil)
	t.add(strings.Repeat(" ", yw+2)+string(placeLabels(w, xl))+"\n", dim)
	if o.XLabel != "" || o.YLabel != "" {
		t.add(strings.Repeat(" ", yw+2)+center("x: "+o.XLabel+"   y: "+o.YLabel, w)+"\n",
			dim.Plus(styled.Style{Italic: true}))
	}
	if vmax > 0 {
		t.addText(Colorbar(cmap, float64(vmin), float64(vmax), "rows/cell", "count", w+yw+2, l))
	}
	return t.text()
}

// quadrantGlyphs is the 2×2 quadrant glyph for (top-left, top-right,
// bottom-left, bottom-right) as the bits 8, 4, 2, 1.
var quadrantGlyphs = [16]rune{' ', '▗', '▖', '▄', '▝', '▐', '▞', '▟', '▘', '▚', '▌', '▙', '▀', '▜', '▛', '█'}

func quadrant(tl, tr, bl, br bool) rune {
	return quadrantGlyphs[b2i(tl)<<3|b2i(tr)<<2|b2i(bl)<<1|b2i(br)]
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
