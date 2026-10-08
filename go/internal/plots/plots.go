// Package plots is the port of Python pqx's plots.py: the Stats histogram,
// the Plot tab's density map and Mollweide sky map, colour bars and
// colormaps, drawn as styled text. Part of the contract
// (docs/design/go-port.md); WP4 implements it, with Python's outputs
// (go/testdata/golden/plots.json) as the spec.
package plots

import "github.com/mjuric/pqx/go/internal/styled"

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

// Histogram draws a vertical bar histogram with eighth-block resolution
// and labelled axes (render_histogram).
func Histogram(edges []float64, counts []int64, o HistOpts, l Look) styled.Text {
	return styled.Text{Plain: "(histogram: WP4)"}
}

// SkyOpts configure SkyMap.
type SkyOpts struct {
	Width, Height int     // default 72 wide; Height 0 for the map's natural height
	Cmap          string  // default magma
	Center        float64 // RA at the centre, 0 or 180
	Caption       string
}

// SkyMap draws counts[lat][lon] (an equirectangular grid, row 0 at −90°)
// as a Mollweide map (render_skymap).
func SkyMap(counts [][]int64, o SkyOpts, l Look) styled.Text {
	return styled.Text{Plain: "(sky map: WP4)"}
}

// SkyShape is the map's size in cells for a width and height (sky_shape).
func SkyShape(width, height int) (int, int) { return width, height }

// DensityOpts configure Density.
type DensityOpts struct {
	Cmap           string
	XLabel, YLabel string
}

// Density draws counts[y][x] (2·h rows × 2·w columns, row 0 the lowest y)
// as a quadrant-glyph density plot over xlim × ylim (render_density).
func Density(counts [][]int64, xlim, ylim [2]float64, o DensityOpts, l Look) styled.Text {
	return styled.Text{Plain: "(density: WP4)"}
}

// Colorbar is a log-scale colour legend (colorbar).
func Colorbar(cmap string, vmin, vmax float64, unit, label string, width int, l Look) styled.Text {
	return styled.Text{}
}

// Sparkline is counts as a one-line bar of eighth blocks (sparkline).
func Sparkline(counts []int64, width int) string { return "" }
