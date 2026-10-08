package plots

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mjuric/pqx/go/internal/golden"
	"github.com/mjuric/pqx/go/internal/styled"
)

// The golden tests: every record of plots.json (Python pqx's outputs) must
// come out the same, text and the style of every character.

// richStyle reads Rich's str(Style) ("dim italic", "color(53)", "blue").
func richStyle(s string) styled.Style {
	var st styled.Style
	for _, w := range strings.Fields(s) {
		switch w {
		case "dim":
			st.Dim = true
		case "bold":
			st.Bold = true
		case "italic":
			st.Italic = true
		case "reverse":
			st.Reverse = true
		case "underline":
			st.Underline = true
		default:
			st.Fg = styled.Color(w)
		}
	}
	return st
}

// charStyles is the style of each rune of t: its base style plus the spans
// covering it, in order.
func charStyles(t styled.Text) []styled.Style {
	n := len([]rune(t.Plain))
	out := make([]styled.Style, n)
	for i := range out {
		out[i] = t.Style
	}
	for _, sp := range t.Spans {
		for i := max(sp.Start, 0); i < min(sp.End, n); i++ {
			out[i] = out[i].Plus(sp.Style)
		}
	}
	return out
}

// sameText reports how got differs from want ("" if it doesn't).
func sameText(got styled.Text, want golden.Text) string {
	if got.Plain != want.Text {
		return fmt.Sprintf("text differs:\n got:\n%s\nwant:\n%s", got.Plain, want.Text)
	}
	gs, ws := charStyles(got), want.CharStyles()
	rs := []rune(want.Text)
	for i := range ws {
		if w := richStyle(ws[i]); gs[i] != w {
			line := strings.Count(string(rs[:i]), "\n")
			return fmt.Sprintf("style of rune %d (%q, line %d) is %+v, want %q", i, rs[i], line, gs[i], ws[i])
		}
	}
	return ""
}

func look(t *testing.T, r golden.Record) Look {
	l := Look{DarkBG: true, Accent: "blue", Dim: styled.Style{Dim: true}}
	if r.Has("dark_bg") {
		l.DarkBG = r.Bool(t, "dark_bg")
	}
	if r.Has("accent") {
		l.Accent = styled.Color(r.String(t, "accent"))
	}
	return l
}

func TestGoldenColors(t *testing.T) {
	f := golden.Load(t, "plots.json")
	for _, r := range f.Section(t, "cmap_color") {
		if got, want := cmapColor(r.String(t, "cmap"), r.Float(t, "frac"), r.Bool(t, "dark_bg")), r.String(t, "out"); got != want {
			t.Errorf("%s: %s, want %s", r.ID(), got, want)
		}
	}
	for _, r := range f.Section(t, "xterm256") {
		if got, want := xterm256(r.String(t, "hex")), r.Int(t, "out"); got != want {
			t.Errorf("%s: %d, want %d", r.ID(), got, want)
		}
	}
	for _, r := range f.Section(t, "density_style") {
		got := densityStyle(r.String(t, "cmap"), r.Float(t, "frac"), r.Bool(t, "dark_bg"), look(t, r))
		if want := r.String(t, "out"); got != richStyle(want) {
			t.Errorf("%s: %+v, want %s", r.ID(), got, want)
		}
	}
	for _, r := range f.Section(t, "fmt_density") {
		if got, want := fmtDensity(r.Float(t, "v")), r.String(t, "out"); got != want {
			t.Errorf("%s: %s, want %s", r.ID(), got, want)
		}
	}
}

func TestGoldenSmall(t *testing.T) {
	f := golden.Load(t, "plots.json")
	for _, r := range f.Section(t, "sparkline") {
		var counts []int64
		r.Decode(t, "counts", &counts)
		if got, want := Sparkline(counts, r.Int(t, "width")), r.String(t, "out"); got != want {
			t.Errorf("%s: %q, want %q", r.ID(), got, want)
		}
	}
	for _, r := range f.Section(t, "axis_labels") {
		var vals []float64
		var want []string
		r.Decode(t, "values", &vals)
		r.Decode(t, "out", &want)
		if got := axisLabels(vals, nil); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: %q, want %q", r.ID(), got, want)
		}
	}
	for _, r := range f.Section(t, "sky_shape") {
		var want [2]int
		r.Decode(t, "out", &want)
		h := 0
		if !r.IsNull("height") {
			h = r.Int(t, "height")
		}
		if w, hh := SkyShape(r.Int(t, "width"), h); w != want[0] || hh != want[1] {
			t.Errorf("%s: %d×%d, want %v", r.ID(), w, hh, want)
		}
	}
	for _, r := range f.Section(t, "colorbar") {
		got := Colorbar(r.String(t, "cmap"), r.Float(t, "vmin"), r.Float(t, "vmax"), r.String(t, "unit"),
			r.String(t, "label"), r.Int(t, "width"), look(t, r))
		if d := sameText(got, r.Text(t, "out")); d != "" {
			t.Errorf("%s: %s", r.ID(), d)
		}
	}
}

func TestGoldenHistogram(t *testing.T) {
	f := golden.Load(t, "plots.json")
	n := 0
	for _, r := range f.Section(t, "render_histogram") {
		var edges []float64
		var counts []int64
		r.Decode(t, "edges", &edges)
		r.Decode(t, "counts", &counts)
		o := HistOpts{Width: r.Int(t, "width"), Height: r.Int(t, "height"), LogY: r.Bool(t, "log_y"),
			LogX: r.Bool(t, "log_x"), XLabel: r.String(t, "xlabel")}
		if !r.IsNull("color") {
			o.Color = styled.Color(r.String(t, "color"))
		}
		got := Histogram(edges, counts, o, look(t, r))
		if e, ok := r.Err(); ok {
			// Python overflows computing 10 ** 1.6e9; Go draws the plot with "inf" labels.
			if !strings.Contains(got.Plain, "inf") {
				t.Errorf("%s: Python raised %s; got %q", r.ID(), e, got.Plain)
			}
			continue
		}
		n++
		if d := sameText(got, r.Text(t, "out")); d != "" {
			t.Errorf("%s: %s", r.ID(), d)
		}
	}
	if n < 100 {
		t.Errorf("only %d histograms", n)
	}
}

func TestGoldenDensity(t *testing.T) {
	f := golden.Load(t, "plots.json")
	for _, r := range f.Section(t, "render_density") {
		var grid [][]int64
		var xl, yl [2]float64
		r.Decode(t, "grid", &grid)
		r.Decode(t, "xlim", &xl)
		r.Decode(t, "ylim", &yl)
		o := DensityOpts{Cmap: r.String(t, "cmap"), XLabel: r.String(t, "xlabel"), YLabel: r.String(t, "ylabel")}
		got := Density(grid, xl, yl, o, look(t, r))
		if d := sameText(got, r.Text(t, "out")); d != "" {
			t.Errorf("%s: %s", r.ID(), d)
		}
	}
}

func TestGoldenSkyMap(t *testing.T) {
	f := golden.Load(t, "plots.json")
	grids := map[string][][]int64{}
	for _, r := range f.Section(t, "skymap_grids") {
		var g [][]int64
		r.Decode(t, "grid", &g)
		grids[r.ID()] = g
	}
	for _, r := range f.Section(t, "render_skymap") {
		g, ok := grids[r.String(t, "grid")]
		if !ok {
			t.Fatalf("%s: no grid %s", r.ID(), r.String(t, "grid"))
		}
		o := SkyOpts{Width: r.Int(t, "width"), Cmap: r.String(t, "cmap"), Center: r.Float(t, "center")}
		if !r.IsNull("height") {
			o.Height = r.Int(t, "height")
		}
		if !r.IsNull("caption") {
			o.Caption = r.String(t, "caption")
		}
		got := SkyMap(g, o, look(t, r))
		if d := sameText(got, r.Text(t, "out")); d != "" {
			t.Errorf("%s: %s", r.ID(), d)
		}
	}
}
