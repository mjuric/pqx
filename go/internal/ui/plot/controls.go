package plot

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/plots"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// controls are Python pqx's PlotControls: one line of "label value ▾"
// fields. Tab moves between them, ← → step one, Enter or a click opens a
// drop-down.
type controls struct {
	values  map[string]string
	options map[string][]string
	cur     int
	spans   []span // where each "value ▾" was drawn last
}

type span struct {
	key  string
	a, b int // first and last cell
}

func newControls() controls {
	return controls{
		values: map[string]string{"mode": "sky", "x": "", "y": "", "centre": "0°", "colour": plots.DefaultCmap},
		options: map[string][]string{"mode": {"sky", "xy"}, "x": nil, "y": nil, "centre": {"0°", "180°"},
			"colour": append([]string(nil), plots.Colormaps...)},
	}
}

func (c *controls) fields() []string {
	if c.values["mode"] == "sky" {
		return []string{"mode", "x", "y", "centre", "colour"}
	}
	return []string{"mode", "x", "y", "colour"}
}

func (c *controls) value(key string) string { return c.values[key] }

// set sets a field; the caller replots.
func (c *controls) set(key, value string) {
	c.values[key] = value
	c.cur = min(c.cur, len(c.fields())-1)
}

func (c *controls) setColumns(numeric []string) {
	c.options["x"] = append([]string(nil), numeric...)
	c.options["y"] = append([]string(nil), numeric...)
}

// step moves the current field's value by d; it reports whether anything
// changed (the caller replots).
func (c *controls) step(d int) bool {
	key := c.fields()[c.cur]
	opts := c.options[key]
	if len(opts) == 0 {
		return false
	}
	i := 0
	for j, o := range opts {
		if o == c.values[key] {
			i = j
		}
	}
	c.set(key, opts[((i+d)%len(opts)+len(opts))%len(opts)])
	return true
}

func (c *controls) field(d int) {
	n := len(c.fields())
	c.cur = ((c.cur+d)%n + n) % n
}

func (c *controls) labels() map[string]string {
	sky := c.values["mode"] == "sky"
	l := map[string]string{"mode": "mode", "x": "x", "y": "y", "centre": "centre", "colour": "colour"}
	if sky {
		l["x"], l["y"] = "lon", "lat"
	}
	return l
}

// render draws the line for width w (cut with "…"), recording the spans.
func (c *controls) render(look kit.Look, focused bool, w int) styled.Text {
	dim := look.Style("dim")
	labels := c.labels()
	var t styled.Text
	c.spans = c.spans[:0]
	cells := 0
	add := func(s string, st styled.Style) {
		t.Append(s, st)
		cells += ansi.StringWidth(s)
	}
	for i, key := range c.fields() {
		if i > 0 {
			add("    ", styled.Style{})
		}
		add(labels[key]+" ", dim)
		st := styled.Style{Bold: true}
		if key == "x" || key == "y" {
			st.Fg = "cyan"
		}
		if focused && i == c.cur {
			st.Reverse = true
		}
		a := cells
		v := fmtx.Sanitize(c.values[key], false)
		if v == "" {
			v = "—"
		}
		add(v+" ▾", st)
		c.spans = append(c.spans, span{key, a, cells - 1})
	}
	if cells > w {
		cut := ansi.Truncate(t.Plain, w, "…")
		n := len([]rune(cut))
		t.Plain = cut
		for i := range t.Spans {
			t.Spans[i].End = min(t.Spans[i].End, n)
		}
	}
	return t
}

// at is the field whose "value ▾" is drawn at cell x, "" if none.
func (c *controls) at(x int) string {
	for _, s := range c.spans {
		if s.a <= x && x <= s.b {
			return s.key
		}
	}
	return ""
}

func (c *controls) spanStart(key string) int {
	for _, s := range c.spans {
		if s.key == key {
			return s.a
		}
	}
	return 0
}

var (
	raNames  = []string{"ra", "raj2000", "ra_j2000", "radeg", "ra_deg", "alpha", "coord_ra", "lon", "elon", "glon", "lambda", "ecl_lon", "eclon"}
	decNames = []string{"dec", "decl", "dej2000", "decj2000", "dec_j2000", "decdeg", "dec_deg", "delta", "coord_dec", "lat", "elat", "glat", "beta", "ecl_lat", "eclat"}
	raRe     = regexp.MustCompile(`(^|_)ra($|_)|^ra[A-Z]|Ra$`)
	decRe    = regexp.MustCompile(`(^|_)dec($|_)|^dec[A-Z]|Dec$`)
	notPos   = regexp.MustCompile(`(?i)err|sigma|cov|rate|dot`)
)

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// GuessSkyColumns picks the most plausible (longitude, latitude) pair of
// names, or "", "" (pqx.data.guess_sky_columns).
func GuessSkyColumns(names []string) (string, string) {
	low := map[string]string{}
	for _, n := range names {
		low[strings.ToLower(n)] = n // the last of names that differ only in case, as Python's dict
	}
	for i := range min(len(raNames), len(decNames)) {
		r, okr := low[raNames[i]]
		d, okd := low[decNames[i]]
		if okr && okd {
			return r, d
		}
	}
	var ras, decs []string
	for _, n := range names {
		if (raRe.MatchString(n) || contains(raNames, strings.ToLower(n))) && !notPos.MatchString(n) {
			ras = append(ras, n)
		}
		if (decRe.MatchString(n) || contains(decNames, strings.ToLower(n))) && !notPos.MatchString(n) {
			decs = append(decs, n)
		}
	}
	if len(ras) > 0 && len(decs) > 0 {
		return ras[0], decs[0]
	}
	return "", ""
}
