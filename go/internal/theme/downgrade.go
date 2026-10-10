package theme

import (
	"image/color"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// Colours are reduced for the terminal the way Python pqx's are (Rich's
// Color.downgrade), not Bubble Tea's way: the two pick different neighbours
// in the 256- and 16-colour palettes, and the parity checks compare them.

// DetectProfile is the colour system Python pqx draws with in a terminal
// with this environment (KEY=value entries): Textual's console, which is
// Rich's detection with the output taken as a terminal, falling back to
// truecolor where Rich would have no colour (TERM dumb or unknown).
//
//   - TEXTUAL_COLOR_SYSTEM standard, 256 or truecolor: that system ("auto",
//     unset or anything else: detected; Python pqx fails on a value Rich
//     doesn't know, and draws with Windows' palette for "windows", taken
//     here as standard);
//   - TERM dumb or unknown (any case, no spaces around): truecolor;
//   - COLORTERM truecolor or 24bit: truecolor;
//   - TERM's last "-" part (or all of it; any case, spaces around ignored)
//     256color or kitty: 256 colours;
//   - anything else, TERM unset or empty too: the 16 standard colours.
//
// NO_COLOR set (to anything, as Textual takes it) gives Ascii: no colours,
// attributes kept. Python pqx instead draws grey shades there (Textual's
// monochrome filter); see docs/design/go-port.md.
//
// colorprofile.Detect, Bubble Tea's own, differs: TERM=screen or tmux is
// 256 colours there, xterm-direct or kitty truecolor, TERM=dumb none.
func DetectProfile(environ []string) colorprofile.Profile {
	env := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			if _, seen := env[k]; !seen { // the first, as getenv takes it
				env[k] = v
			}
		}
	}
	if _, ok := env["NO_COLOR"]; ok {
		return colorprofile.Ascii
	}
	switch env["TEXTUAL_COLOR_SYSTEM"] {
	case "standard", "windows":
		return colorprofile.ANSI
	case "256":
		return colorprofile.ANSI256
	case "truecolor":
		return colorprofile.TrueColor
	}
	term := strings.ToLower(env["TERM"])
	if term == "dumb" || term == "unknown" {
		return colorprofile.TrueColor
	}
	term = strings.TrimSpace(term)
	switch strings.TrimSpace(strings.ToLower(env["COLORTERM"])) {
	case "truecolor", "24bit":
		return colorprofile.TrueColor
	}
	switch term[strings.LastIndexByte(term, '-')+1:] {
	case "256color", "kitty":
		return colorprofile.ANSI256
	}
	return colorprofile.ANSI
}

// Profile is the terminal's colour profile (SetProfile).
func (t *Theme) Profile() colorprofile.Profile { return t.profile }

// SetProfile sets the terminal's colour profile (DetectProfile): with fewer
// colours than truecolor, every colour pqx draws is reduced as Rich reduces
// it (the theme's as styles are made, the rest of the frame in Paint).
func (t *Theme) SetProfile(p colorprofile.Profile) {
	t.profile = p
	t.build()
}

// out is c as it is sent to a terminal of the theme's profile. With a
// named theme colours stay as they are until Paint, which reduces them.
func (t *Theme) out(c color.Color) color.Color {
	if t.named {
		return c
	}
	return t.reduce(c)
}

// reduce is c for a terminal of the theme's profile. A 256-colour index
// beyond the 16 ANSI colours is first its xterm truecolor value, as Textual
// takes it (textual.color.Color.from_rich_color), so on a 256-colour
// terminal the cube's greys move to the grey ramp (188 → 252) and on a
// truecolor one it is sent as truecolor, as Python pqx sends it.
func (t *Theme) reduce(c color.Color) color.Color {
	if ic, ok := c.(ansi.IndexedColor); ok && ic >= 16 && t.profile >= colorprofile.ANSI256 {
		c = xterm(int(ic)).color()
	}
	switch t.profile {
	case colorprofile.TrueColor:
		return c
	case colorprofile.ANSI256:
		if rgb, ok := c.(color.RGBA); ok {
			return ansi.IndexedColor(rich256(rgb.R, rgb.G, rgb.B))
		}
	case colorprofile.ANSI:
		switch c := c.(type) {
		case color.RGBA:
			return ansi.BasicColor(richStandard(c.R, c.G, c.B))
		case ansi.IndexedColor:
			if c >= 16 {
				x := xterm(int(c))
				return ansi.BasicColor(richStandard(x.r, x.g, x.b))
			}
		}
	}
	return c
}

// reduceFrame is frame with every colour its SGR sequences set reduced for
// the terminal's profile (reduce), the rest as it is: the colours of a frame
// without a named theme that didn't come from the theme's styles (a part's
// own SGR, a colourmap) are reduced here, so none is left to Bubble Tea,
// which reduces them its own way. On a 256-colour terminal a 256-colour
// index is left as it is: it comes from Render, already reduced.
func (t *Theme) reduceFrame(frame string) string {
	switch t.profile {
	case colorprofile.ANSI, colorprofile.ANSI256, colorprofile.TrueColor:
	default:
		return frame
	}
	switch {
	case t.profile == colorprofile.ANSI256 && !strings.Contains(frame, "8;2;"),
		t.profile == colorprofile.TrueColor && !strings.Contains(frame, "8;5;"),
		!strings.Contains(frame, "8;2;") && !strings.Contains(frame, "8;5;"):
		return frame
	}
	var b strings.Builder
	b.Grow(len(frame))
	for {
		i := strings.Index(frame, "\x1b[")
		if i < 0 {
			b.WriteString(frame)
			return b.String()
		}
		k := i + 2
		for k < len(frame) && (frame[k] >= '0' && frame[k] <= '9' || frame[k] == ';' || frame[k] == ':') {
			k++
		}
		b.WriteString(frame[:i+2])
		if k < len(frame) && frame[k] == 'm' {
			b.WriteString(t.reduceSGR(frame[i+2 : k]))
		} else {
			b.WriteString(frame[i+2 : k])
		}
		frame = frame[k:]
	}
}

// reduceSGR is an SGR parameter string with its 38 and 48 colours reduced.
func (t *Theme) reduceSGR(params string) string {
	if !strings.Contains(params, "8;") {
		return params
	}
	ps := strings.Split(params, ";")
	out := make([]string, 0, len(ps))
	for i := 0; i < len(ps); i++ {
		if (ps[i] != "38" && ps[i] != "48") || i+1 >= len(ps) {
			out = append(out, ps[i])
			continue
		}
		var c color.Color
		n := 0
		switch {
		case ps[i+1] == "5" && i+2 < len(ps) && t.profile != colorprofile.ANSI256:
			// (on a 256-colour terminal an index is one Render has already
			// reduced: reducing it again moves greys a step down the ramp)
			v, err := strconv.Atoi(ps[i+2])
			if err != nil || v < 0 || v > 255 {
				break
			}
			c, n = ansi.IndexedColor(v), 3
			if v < 16 {
				c = ansi.BasicColor(v)
			}
		case ps[i+1] == "2" && i+4 < len(ps):
			var v [3]int
			ok := true
			for j := range v {
				x, err := strconv.Atoi(ps[i+2+j])
				ok = ok && err == nil && x >= 0 && x <= 255
				v[j] = x
			}
			if ok {
				c, n = color.RGBA{uint8(v[0]), uint8(v[1]), uint8(v[2]), 0xff}, 5
			}
		}
		if n == 0 {
			out = append(out, ps[i])
			continue
		}
		basic := 30
		if ps[i] == "48" {
			basic = 40
		}
		out = append(out, sgrColor(t.reduce(c), ps[i], basic)[1:])
		i += n - 1
	}
	return strings.Join(out, ";")
}

// rich256 is Rich's downgrade of a truecolor colour to the 256-colour
// palette: greys (saturation under 15%) to the grey ramp, the rest to the
// 6×6×6 cube, rounding half to even as Python does.
func rich256(r, g, b uint8) uint8 {
	_, l, s := rgbToHLS(float64(r)/255, float64(g)/255, float64(b)/255)
	if s < 0.15 {
		gray := int(math.RoundToEven(l * 25))
		switch gray {
		case 0:
			return 16
		case 25:
			return 231
		}
		return uint8(231 + gray)
	}
	six := func(v uint8) int {
		x := float64(v) / 95
		if v >= 95 {
			x = 1 + (float64(v)-95)/40
		}
		return int(math.RoundToEven(x))
	}
	return uint8(16 + 36*six(r) + 6*six(g) + six(b))
}

// rgbToHLS is Python's colorsys.rgb_to_hls (lightness and saturation).
func rgbToHLS(r, g, b float64) (h, l, s float64) {
	maxc := math.Max(r, math.Max(g, b))
	minc := math.Min(r, math.Min(g, b))
	sumc := maxc + minc
	rangec := maxc - minc
	l = sumc / 2
	if minc == maxc {
		return 0, l, 0
	}
	if l <= 0.5 {
		s = rangec / sumc
	} else {
		s = rangec / (2 - maxc - minc)
	}
	return 0, l, s
}

// standardPalette is Rich's STANDARD_PALETTE, which it matches 16-colour
// terminals against.
var standardPalette = [16][3]int{
	{0, 0, 0}, {170, 0, 0}, {0, 170, 0}, {170, 85, 0}, {0, 0, 170}, {170, 0, 170}, {0, 170, 170}, {170, 170, 170},
	{85, 85, 85}, {255, 85, 85}, {85, 255, 85}, {255, 255, 85}, {85, 85, 255}, {255, 85, 255}, {85, 255, 255}, {255, 255, 255},
}

// richStandard is Rich's Palette.match against standardPalette.
func richStandard(r, g, b uint8) int {
	best, bestD := 0, math.Inf(1)
	for i, c := range standardPalette {
		rm := (int(r) + c[0]) / 2
		dr, dg, db := int(r)-c[0], int(g)-c[1], int(b)-c[2]
		d := math.Sqrt(float64(((512+rm)*dr*dr)>>8 + 4*dg*dg + ((767-rm)*db*db)>>8))
		if d < bestD {
			best, bestD = i, d
		}
	}
	return best
}
