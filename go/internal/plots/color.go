package plots

import (
	"fmt"
	"math"
	"strconv"

	"github.com/mjuric/pqx/go/internal/styled"
)

var cmapStops = map[string][][3]float64{
	"viridis": {{68, 1, 84}, {59, 82, 139}, {33, 145, 140}, {94, 201, 98}, {253, 231, 37}},
	"inferno": {{0, 0, 4}, {87, 16, 110}, {188, 55, 84}, {249, 142, 9}, {252, 255, 164}},
	"magma":   {{0, 0, 4}, {81, 18, 124}, {183, 55, 121}, {252, 137, 97}, {252, 253, 191}},
	"plasma":  {{13, 8, 135}, {126, 3, 168}, {204, 71, 120}, {248, 149, 64}, {240, 249, 33}},
}

// colorFloor lifts the low end of a colormap so it stays visible.
const colorFloor = 0.22

// cmapColor is the "#rrggbb" colour for frac in [0, 1] of cmap, its low end
// floored for visibility (cmap_color). An unknown cmap is magma.
func cmapColor(cmap string, frac float64, darkBG bool) string {
	if cmap == "terminal" {
		cmap = "gray"
	}
	f := colorFloor + (1.0-colorFloor)*pyMin(1.0, pyMax(0.0, frac))
	if cmap == "gray" {
		var g float64
		if darkBG {
			g = math.RoundToEven(120 + f*135)
		} else {
			g = math.RoundToEven(210 - f*190)
		}
		return fmt.Sprintf("#%02x%02x%02x", int(g), int(g), int(g))
	}
	stops, ok := cmapStops[cmap]
	if !ok {
		stops = cmapStops[DefaultCmap]
	}
	if !darkBG { // the dark end must stay visible on a light background: flip
		f = 1.0 - f*0.85
	}
	x := f * float64(len(stops)-1)
	i := int(x)
	var rgb [3]float64
	if i >= len(stops)-1 {
		rgb = stops[len(stops)-1]
	} else {
		t := x - float64(i)
		a, b := stops[i], stops[i+1]
		for k := range 3 {
			rgb[k] = math.RoundToEven(a[k] + (b[k]-a[k])*t)
		}
	}
	return fmt.Sprintf("#%02x%02x%02x", int(rgb[0]), int(rgb[1]), int(rgb[2]))
}

var cube = [6]int{0, 95, 135, 175, 215, 255}

// xterm256 is the nearest xterm-256 index (6×6×6 cube or 24-step gray ramp)
// to "#rrggbb" (xterm256).
func xterm256(hex string) int {
	ch := func(i int) int {
		v, _ := strconv.ParseInt(hex[i:i+2], 16, 0)
		return int(v)
	}
	r, g, b := ch(1), ch(3), ch(5)
	near := func(v int) int {
		best := 0
		for k := 1; k < 6; k++ {
			if abs(cube[k]-v) < abs(cube[best]-v) {
				best = k
			}
		}
		return best
	}
	ci := 16 + 36*near(r) + 6*near(g) + near(b)
	cr, cg, cb := cube[near(r)], cube[near(g)], cube[near(b)]
	gray := int(max(0, min(23, math.RoundToEven((float64(r+g+b)/3-8)/10))))
	gv := 8 + 10*gray
	dCube := sq(r-cr) + sq(g-cg) + sq(b-cb)
	dGray := sq(r-gv) + sq(g-gv) + sq(b-gv)
	if dGray < dCube {
		return 232 + gray
	}
	return ci
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func sq(v int) int { return v * v }

// densityStyle is the style for a density fraction in [0, 1]: a 256-colour
// index, or for the "terminal" colormap the theme's dim, accent and bold
// (density_style).
func densityStyle(cmap string, frac float64, darkBG bool, l Look) styled.Style {
	if cmap == "terminal" {
		if frac < 1.0/3 {
			return l.dim()
		}
		if frac < 2.0/3 {
			return styled.Style{Fg: l.accent()}
		}
		return styled.Style{Bold: true}
	}
	return styled.Style{Fg: styled.Color(fmt.Sprintf("color(%d)", xterm256(cmapColor(cmap, frac, darkBG))))}
}

// fmtDensity is a density value in a few characters (fmt_density).
func fmtDensity(v float64) string {
	switch {
	case v >= 1e6:
		return pyF(v/1e6, 1) + "M"
	case v >= 1e4:
		return pyF(v/1e3, 0) + "k"
	case v >= 1e3:
		return pyF(v/1e3, 1) + "k"
	case v >= 10:
		return pyF(v, 0)
	case v >= 1:
		return pyF(v, 1)
	case v > 0:
		return pyG(v, 2)
	}
	return "0"
}

// dim is the Look's secondary style; the zero Look gets Python's default, dim.
func (l Look) dim() styled.Style {
	if l.Dim == (styled.Style{}) {
		return styled.Style{Dim: true}
	}
	return l.Dim
}

// accent is the Look's accent colour; the zero Look gets Python's default, blue.
func (l Look) accent() styled.Color {
	if l.Accent == "" {
		return "blue"
	}
	return l.Accent
}
