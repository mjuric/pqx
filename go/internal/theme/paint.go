package theme

import (
	"image/color"
	"strconv"
	"strings"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// Paint applies a named theme to a whole frame, as Textual applies one to
// the screen: every cell gets the theme's background and, where none is
// set, its foreground (lines are padded to width w); the 16 ANSI colours
// become the theme's (Monokai); faint text becomes its colour blended
// towards the background (Textual's dim); and colours are reduced for the
// terminal as the rest of the theme's are. Parts that write their own SGR
// sequences (the grid) are themed this way. Without a named theme the frame
// is returned as it is.
func (t *Theme) Paint(frame string, w int) string {
	if !t.named {
		return frame
	}
	var b strings.Builder
	b.Grow(len(frame) + len(frame)/4)
	var st pen
	lines := strings.Split(frame, "\n")
	for li, line := range lines {
		if li > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(t.sgr(st))
		i := 0
		for i < len(line) {
			j := strings.Index(line[i:], "\x1b[")
			if j < 0 {
				b.WriteString(line[i:])
				break
			}
			b.WriteString(line[i : i+j])
			i += j
			k := i + 2
			for k < len(line) && (line[k] >= '0' && line[k] <= '9' || line[k] == ';' || line[k] == ':') {
				k++
			}
			if k < len(line) && line[k] == 'm' {
				st.apply(line[i+2 : k])
				b.WriteString(t.sgr(st))
				i = k + 1
				continue
			}
			b.WriteString(line[i:min(k+1, len(line))]) // another sequence: as it is
			i = k + 1
		}
		if n := ansi.StringWidth(line); n < w {
			b.WriteString(t.sgr(pen{}))
			b.WriteString(strings.Repeat(" ", w-n))
			b.WriteString(t.sgr(st))
		}
	}
	b.WriteString("\x1b[m")
	return b.String()
}

// pen is the SGR state Paint tracks.
type pen struct {
	fg, bg                                  color.Color // nil: the theme's
	bold, faint, italic, underline, reverse bool
	blink, strike                           bool
}

// apply updates the pen with an SGR parameter string.
func (p *pen) apply(params string) {
	if params == "" {
		*p = pen{}
		return
	}
	ps := strings.Split(params, ";")
	for i := 0; i < len(ps); i++ {
		s := ps[i]
		if strings.HasPrefix(s, "4:") { // an underline style
			p.underline = s != "4:0"
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			continue
		}
		switch {
		case n == 0:
			*p = pen{}
		case n == 1:
			p.bold = true
		case n == 2:
			p.faint = true
		case n == 3:
			p.italic = true
		case n == 4:
			p.underline = true
		case n == 5:
			p.blink = true
		case n == 7:
			p.reverse = true
		case n == 9:
			p.strike = true
		case n == 22:
			p.bold, p.faint = false, false
		case n == 23:
			p.italic = false
		case n == 24:
			p.underline = false
		case n == 25:
			p.blink = false
		case n == 27:
			p.reverse = false
		case n == 29:
			p.strike = false
		case n >= 30 && n <= 37:
			p.fg = ansi.BasicColor(n - 30)
		case n >= 90 && n <= 97:
			p.fg = ansi.BasicColor(n - 90 + 8)
		case n == 39:
			p.fg = nil
		case n >= 40 && n <= 47:
			p.bg = ansi.BasicColor(n - 40)
		case n >= 100 && n <= 107:
			p.bg = ansi.BasicColor(n - 100 + 8)
		case n == 49:
			p.bg = nil
		case n == 38 || n == 48:
			var c color.Color
			if i+2 < len(ps) && ps[i+1] == "5" {
				v, _ := strconv.Atoi(ps[i+2])
				c, i = ansi.IndexedColor(v), i+2
			} else if i+4 < len(ps) && ps[i+1] == "2" {
				r, _ := strconv.Atoi(ps[i+2])
				g, _ := strconv.Atoi(ps[i+3])
				bl, _ := strconv.Atoi(ps[i+4])
				c, i = color.RGBA{uint8(r), uint8(g), uint8(bl), 0xff}, i+4
			}
			if n == 38 {
				p.fg = c
			} else {
				p.bg = c
			}
		}
	}
}

// sgr is the full SGR sequence for p under the theme.
func (t *Theme) sgr(p pen) string {
	var b strings.Builder
	b.WriteString("\x1b[0")
	for _, a := range []struct {
		on   bool
		code string
	}{{p.bold, ";1"}, {p.italic, ";3"}, {p.underline, ";4"}, {p.blink, ";5"}, {p.reverse, ";7"}, {p.strike, ";9"}} {
		if a.on {
			b.WriteString(a.code)
		}
	}
	bg := t.bg
	if c, ok := t.themed(p.bg); ok {
		bg = c
	}
	fg := t.fg
	if c, ok := t.themed(p.fg); ok {
		fg = c
	}
	if p.faint {
		fg = blend(bg, fg, dimFactor)
	}
	fgc, bgc := t.out(fg.color()), t.out(bg.color())
	// a colour the frame already has from the 256-colour palette was reduced
	// by Render (or asked for as such): reducing it again would move the
	// cube's greys (59, #5f5f5f) to the grey ramp (240, #585858)
	if c, ok := kept(t.profile, p.fg); ok && !p.faint {
		fgc = c
	}
	if c, ok := kept(t.profile, p.bg); ok {
		bgc = c
	}
	b.WriteString(sgrColor(fgc, "38", 30))
	b.WriteString(sgrColor(bgc, "48", 40))
	b.WriteByte('m')
	return b.String()
}

// kept is c as it is when the terminal has 256 colours and c is one of
// them beyond the 16 ANSI colours.
func kept(p colorprofile.Profile, c color.Color) (color.Color, bool) {
	if ic, ok := c.(ansi.IndexedColor); ok && ic >= 16 && p == colorprofile.ANSI256 {
		return ic, true
	}
	return nil, false
}

// themed is c in truecolor under the theme (ANSI colours are Monokai's);
// false for the default.
func (t *Theme) themed(c color.Color) (rgb, bool) {
	switch c := c.(type) {
	case nil:
		return rgb{}, false
	case ansi.BasicColor:
		return xterm(int(c)), true
	case ansi.IndexedColor:
		return xterm(int(c)), true
	}
	return toRGB(c)
}

// sgrColor is the SGR parameters for c as a foreground (base "38", basic
// 30) or background ("48", 40).
func sgrColor(c color.Color, ext string, basic int) string {
	switch c := c.(type) {
	case ansi.BasicColor:
		if c < 8 {
			return ";" + strconv.Itoa(basic+int(c))
		}
		return ";" + strconv.Itoa(basic+60+int(c)-8)
	case ansi.IndexedColor:
		return ";" + ext + ";5;" + strconv.Itoa(int(c))
	}
	r, g, b, _ := c.RGBA()
	return ";" + ext + ";2;" + strconv.Itoa(int(r>>8)) + ";" + strconv.Itoa(int(g>>8)) + ";" + strconv.Itoa(int(b>>8))
}
