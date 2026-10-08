package plots

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mjuric/pqx/go/internal/styled"
)

var dimLook = Look{DarkBG: true, Accent: "blue", Dim: styled.Style{Dim: true}}

func zeros(nlat, nlon int) [][]int64 {
	g := make([][]int64, nlat)
	for j := range g {
		g[j] = make([]int64, nlon)
	}
	return g
}

// tests/test_fmt_plots.py::test_skymap_renders
func TestSkyMapRenders(t *testing.T) {
	g := zeros(360, 720)
	for j := 170; j < 190; j++ {
		for i := range 40 {
			g[j][i] = 10 // a patch at ra 0-20, dec -5..+5
		}
	}
	tx := SkyMap(g, SkyOpts{Width: 80}, dimLook)
	lines := strings.Split(tx.Plain, "\n")
	if len(lines) < 20 {
		t.Fatalf("%d lines", len(lines))
	}
	if !strings.Contains(tx.Plain, "deg⁻²") {
		t.Error("no colorbar unit")
	}
	row := ""
	for _, ln := range lines {
		if strings.Contains(ln, "█") {
			row = ln
			break
		}
	}
	if row == "" {
		t.Fatal("no full block")
	}
	// RA increases to the left: the patch (ra 0..20) sits just left of centre
	if i := strings.Index(string([]rune(row)[:40]), "█"); i < 0 {
		t.Errorf("patch not left of centre: %q", row)
	}
	if empty := SkyMap(zeros(180, 360), SkyOpts{Width: 40}, dimLook); strings.Contains(empty.Plain, "█") {
		t.Error("empty map has a block")
	}
}

// tests/test_fmt_plots.py::test_histogram_and_density_render
func TestHistogramAndDensityRender(t *testing.T) {
	var edges []float64
	for i := range 21 {
		edges = append(edges, float64(i)*0.5)
	}
	base := []int64{0, 1, 5, 10, 50, 100, 50, 10, 5, 1}
	counts := append(append([]int64{}, base...), base...)
	tx := Histogram(edges, counts, HistOpts{Width: 60, Height: 8, XLabel: "mag"}, dimLook)
	if !strings.Contains(tx.Plain, "100 ┤") || !strings.Contains(tx.Plain, "mag") {
		t.Errorf("histogram:\n%s", tx.Plain)
	}
	l := axisLabels([]float64{1.7e17, 1.7e17 + 5e5, 1.7e17 + 1e6}, nil)
	if l[0] == l[1] {
		t.Errorf("labels not distinct: %q", l)
	}
	grid := zeros(20, 40)
	for y := 5; y < 10; y++ {
		for x := 10; x < 30; x++ {
			grid[y][x] = 3
		}
	}
	d := Density(grid, [2]float64{0, 1}, [2]float64{0, 1}, DensityOpts{XLabel: "x", YLabel: "y"}, dimLook)
	if !strings.Contains(d.Plain, "count:") {
		t.Errorf("density:\n%s", d.Plain)
	}
	if s := Sparkline([]int64{0, 1, 2, 3}, 4); s != " ▃▅█" {
		t.Errorf("sparkline %q", s)
	}
}

// tests/test_fmt_plots.py::test_colormaps
func TestColormaps(t *testing.T) {
	for _, cm := range Colormaps {
		for _, dark := range []bool{true, false} {
			if c := cmapColor(cm, 0.5, dark); !strings.HasPrefix(c, "#") || len(c) != 7 {
				t.Errorf("%s %v: %q", cm, dark, c)
			}
		}
	}
	if DefaultCmap != "magma" || Colormaps[0] != DefaultCmap {
		t.Error("default cmap")
	}
	for hex, want := range map[string]int{"#000000": 16, "#ffffff": 231, "#d7af5f": 179, "#808080": 244} {
		if got := xterm256(hex); got != want {
			t.Errorf("xterm256(%s) = %d, want %d", hex, got, want)
		}
	}
	if st := densityStyle("magma", 0.5, true, dimLook); !strings.HasPrefix(string(st.Fg), "color(") {
		t.Errorf("magma style %+v", st)
	}
	if !densityStyle("terminal", 0.9, true, dimLook).Bold {
		t.Error("terminal 0.9 not bold")
	}
	if st := densityStyle("terminal", 0.5, true, Look{Accent: "cyan"}); st.Fg != "cyan" {
		t.Errorf("terminal 0.5 %+v", st)
	}
	if st := densityStyle("terminal", 0.1, true, Look{Dim: styled.Style{Fg: "bright_black"}}); st.Fg != "bright_black" {
		t.Errorf("terminal 0.1 uses Look.Dim: %+v", st)
	}
	sum := 0.0
	for j := range 180 {
		sum += cellAreaDeg2(180, 360, j)
	}
	if got := sum * 360; math.Abs(got/41252.96-1) > 1e-4 {
		t.Errorf("sky area %v", got)
	}
}

func TestHistogramEdges(t *testing.T) {
	tx := Histogram(nil, nil, HistOpts{}, dimLook)
	if tx.Plain != "(no finite values)" || len(tx.Spans) != 1 || tx.Spans[0].Style != (styled.Style{Dim: true, Italic: true}) {
		t.Errorf("empty: %+v", tx)
	}
	// epoch seconds on the x axis, formatted by XFmt
	epoch := func(v float64) string { return time.Unix(int64(v), 0).UTC().Format("2006-01-02 15:04") }
	tx = Histogram([]float64{1.6e9, 1.65e9, 1.7e9}, []int64{3, 4}, HistOpts{Width: 70, Height: 4, XFmt: epoch}, dimLook)
	for _, s := range []string{"2020-09-13 12:26", "2022-04-15 05:20", "2023-11-14 22:13"} {
		if !strings.Contains(tx.Plain, s) {
			t.Errorf("no %s in\n%s", s, tx.Plain)
		}
	}
	// defaults: 70 × 12
	tx = Histogram([]float64{0, 1}, []int64{5}, HistOpts{}, Look{})
	lines := strings.Split(tx.Plain, "\n")
	if len(lines) != 15 || len([]rune(lines[0])) != 70 {
		t.Errorf("defaults: %d lines, first %d wide", len(lines), len([]rune(lines[0])))
	}
}

func TestPyFormat(t *testing.T) {
	for _, c := range []struct {
		v    float64
		p    int
		want string
	}{{0.0123, 2, "0.012"}, {1e-5, 3, "1e-05"}, {1.5e6, 3, "1.5e+06"}, {123456, 3, "1.23e+05"},
		{9.995, 3, "9.99"}, {99.95, 3, "100"}, {math.Copysign(0, -1), 3, "-0"}, {math.Inf(1), 3, "inf"}, {math.NaN(), 3, "nan"}} {
		if got := pyG(c.v, c.p); got != c.want {
			t.Errorf("pyG(%v, %d) = %s, want %s", c.v, c.p, got, c.want)
		}
	}
	for v, want := range map[float64]string{1.0: "1.0", 1e16: "1e+16", 1e15: "1000000000000000.0", 1.5e-5: "1.5e-05", 0.0001: "0.0001"} {
		if got := pyRepr(v); got != want {
			t.Errorf("pyRepr(%v) = %s, want %s", v, got, want)
		}
	}
	for _, c := range []struct {
		s    string
		w    int
		want string
	}{{"ab", 6, "  ab  "}, {"abc", 6, " abc  "}, {"a", 6, "  a   "}, {"abc", 7, "  abc  "}, {"ab", 5, "  ab "}, {"a", 5, "  a  "}, {"abcdef", 3, "abcdef"}} {
		if got := center(c.s, c.w); got != c.want {
			t.Errorf("center(%q, %d) = %q, want %q", c.s, c.w, got, c.want)
		}
	}
	if got := commas(-1234567); got != "-1,234,567" {
		t.Errorf("commas %s", got)
	}
	if got := string(splice([]rune("....."), 3, "abcd")); got != "...abcd" {
		t.Errorf("splice %q", got)
	}
}

func BenchmarkSkyMap(b *testing.B) {
	g := zeros(360, 720)
	for j := range g {
		for i := range g[j] {
			g[j][i] = int64((i*7 + j*13) % 50)
		}
	}
	for b.Loop() {
		SkyMap(g, SkyOpts{Width: 72, Height: 36}, dimLook)
	}
}

func BenchmarkDensity(b *testing.B) {
	g := zeros(80, 300)
	for j := range g {
		for i := range g[j] {
			g[j][i] = int64((i * j) % 17)
		}
	}
	for b.Loop() {
		Density(g, [2]float64{0, 1}, [2]float64{0, 1}, DensityOpts{XLabel: "x", YLabel: "y"}, dimLook)
	}
}

func BenchmarkHistogram(b *testing.B) {
	edges := make([]float64, 101)
	counts := make([]int64, 100)
	for i := range edges {
		edges[i] = float64(i)
	}
	for i := range counts {
		counts[i] = int64(i * i)
	}
	for b.Loop() {
		Histogram(edges, counts, HistOpts{Width: 70, Height: 12}, dimLook)
	}
}
