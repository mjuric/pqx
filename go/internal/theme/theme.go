// Package theme is pqx's look: the terminal's own palette by default (its
// default foreground and background, the faint attribute for secondary text,
// the 16 ANSI colours for meaning: ✓ green, ! yellow, ✗ red), an accent
// (focus) colour, how secondary text is dimmed, the colour of unfocused
// borders, and five named themes with fixed palettes (--theme). It turns
// styled.Text into terminal output.
//
// It follows Python pqx (app.py's pqx_theme and app.tcss) on Textual: with a
// named theme Textual draws everything in truecolor, ANSI colours named by
// pqx (the ✓ ! ✗ marks, the active tab, cyan keys) are mapped through
// Textual's dark ANSI palette (Monokai) and dim text is its colour blended
// two thirds of the way from the background.
package theme

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/styled"
)

// Accents are the --accent choices; the first is the default.
var Accents = []string{"blue", "cyan", "magenta", "green", "yellow"}

// DimModes are the --dim choices; the first is the default.
var DimModes = []string{"faint", "bright-black"}

// DefaultBorder is the colour of unfocused borders without --border.
const DefaultBorder = "ansi_bright_black"

// Names are the named themes (--theme), the ones the README names (D2 in
// docs/design/go-port.md).
var Names = []string{"tokyo-night", "dracula", "catppuccin-mocha", "nord", "gruvbox"}

// Palette is a named theme's colours, as Textual defines the theme.
type Palette struct {
	Background, Surface, Panel, Foreground string
	Primary, Secondary, Accent             string
	Warning, Error, Success                string
	// Scrollbar and ScrollbarBackground are Textual's $scrollbar (the
	// thumb) and $scrollbar-background (the track), which it derives from
	// the palette.
	Scrollbar, ScrollbarBackground string
}

// Palettes are the named themes, from Textual 8's theme.py.
var Palettes = map[string]Palette{
	"tokyo-night": {
		Background: "#1A1B26", Surface: "#24283B", Panel: "#414868", Foreground: "#a9b1d6",
		Primary: "#BB9AF7", Secondary: "#7AA2F7", Accent: "#FF9E64",
		Warning: "#E0AF68", Error: "#F7768E", Success: "#9ECE6A",
		Scrollbar: "#4F4270", ScrollbarBackground: "#070817",
	},
	"dracula": {
		Background: "#282A36", Surface: "#2B2E3B", Panel: "#313442", Foreground: "#F8F8F2",
		Primary: "#BD93F9", Secondary: "#6272A4", Accent: "#FF79C6",
		Warning: "#FFB86C", Error: "#FF5555", Success: "#50FA7B",
		Scrollbar: "#5A4A79", ScrollbarBackground: "#181A25",
	},
	"catppuccin-mocha": {
		Background: "#181825", Surface: "#313244", Panel: "#45475a", Foreground: "#cdd6f4",
		Primary: "#F5C2E7", Secondary: "#cba6f7", Accent: "#fab387",
		Warning: "#FAE3B0", Error: "#F28FAD", Success: "#ABE9B3",
		Scrollbar: "#644E69", ScrollbarBackground: "#040216",
	},
	"nord": {
		Background: "#2E3440", Surface: "#3B4252", Panel: "#434C5E", Foreground: "#D8DEE9",
		Primary: "#88C0D0", Secondary: "#81A1C1", Accent: "#B48EAD",
		Warning: "#EBCB8B", Error: "#BF616A", Success: "#A3BE8C",
		Scrollbar: "#48626F", ScrollbarBackground: "#1E242F",
	},
	"gruvbox": {
		Background: "#282828", Surface: "#3c3836", Panel: "#504945", Foreground: "#fbf1c7",
		Primary: "#85A598", Secondary: "#A89A85", Accent: "#fabd2f",
		Warning: "#fe8019", Error: "#fb4934", Success: "#b8bb26",
		Scrollbar: "#43504B", ScrollbarBackground: "#181818",
	},
}

// ansiNames are the 16 ANSI colours by index (Rich's and Textual's names).
var ansiNames = []string{
	"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white",
	"bright_black", "bright_red", "bright_green", "bright_yellow",
	"bright_blue", "bright_magenta", "bright_cyan", "bright_white",
}

// monokai is Textual's dark ANSI palette (textual._ansi_theme.MONOKAI), which
// a named theme draws the 16 ANSI colours with.
var monokai = [16]string{
	"#1a1a1a", "#f4005f", "#98e024", "#fd971f", "#9d65ff", "#f4005f", "#58d1eb", "#c4c5b5",
	"#625e4c", "#f4005f", "#98e024", "#e0d561", "#9d65ff", "#f4005f", "#58d1eb", "#f6f6ef",
}

// Textual's blend factors: a border is the primary colour at 45% over the
// background ("$primary 45%"); dim text is blended to 66% (DIM_FACTOR).
const (
	borderAlpha = 0.45
	dimFactor   = 0.66
)

// Theme is a resolved look. The zero value isn't useful; use New.
type Theme struct {
	Name       string // the named theme, or "" for the terminal's palette
	AccentName string // one of Accents
	DimMode    string // one of DimModes

	named   bool
	profile colorprofile.Profile // the terminal's; TrueColor (or Unknown) leaves colours as they are
	fg, bg  rgb                  // a named theme's foreground and background
	primary color.Color
	border  color.Color

	// Styles for the roles pqx uses. With a named theme each carries the
	// theme's foreground and background.
	Base        lipgloss.Style // ordinary text
	Accent      lipgloss.Style // the accent colour: active tab title, spinner
	Dim         lipgloss.Style // secondary text
	Border      lipgloss.Style // unfocused panel borders (BorderForeground)
	FocusBorder lipgloss.Style // focused panel borders, dialogs (the theme's primary)
	Prompt      lipgloss.Style // the filter box's "›" (primary)
	Error       lipgloss.Style // ✗, error borders: ANSI red
	Warning     lipgloss.Style // !: ANSI yellow
	Success     lipgloss.Style // ✓: ANSI green
	Cursor      lipgloss.Style // the grid and list cursor: reverse
	Header      lipgloss.Style // column headers: bold
	Selection   lipgloss.Style // selected text in inputs: reverse
}

// New resolves a look from the option values (already resolved against the
// environment by the CLI): accent one of Accents, dim one of DimModes,
// border a colour as ParseBorder takes it, name "" or one of Names. It
// returns an error for an unknown theme or border colour.
func New(accent, dim, border, name string) (*Theme, error) {
	t := &Theme{AccentName: "blue", DimMode: "faint"}
	if contains(Accents, accent) {
		t.AccentName = accent
	}
	if contains(DimModes, dim) {
		t.DimMode = dim
	}
	if border == "" {
		border = DefaultBorder
	}
	bc, err := ParseBorder(border)
	if err != nil {
		return nil, err
	}
	t.border = bc
	t.primary = ansi.BasicColor(ansiIndex(t.AccentName))
	if name != "" {
		p, ok := Palettes[name]
		if !ok {
			return nil, fmt.Errorf("unknown theme %q (choose from %s)", name, strings.Join(Names, ", "))
		}
		t.Name, t.named = name, true
		t.fg, t.bg = mustHex(p.Foreground), mustHex(p.Background)
		prim := mustHex(p.Primary)
		t.primary = prim.color()
		t.border = blend(t.bg, prim, borderAlpha).color()
	}
	t.build()
	return t, nil
}

// Style is the styled.Style for a role (kit.Look): "accent", "dim",
// "border", "border-focus", "error", "warning", "success", "header",
// "cursor" (reverse video), "selection", "scrollbar" (Fg the thumb, Bg
// the track: the unfocused border colour on the terminal's background, as
// pqx's own theme sets them, or a named theme's Textual scrollbar colours)
// or "focus-background" (Bg: a named theme's background tinted for a
// focused table; nothing for pqx's own theme).
func (t *Theme) Style(role string) styled.Style {
	switch role {
	case "accent":
		return styled.Style{Fg: "accent"}
	case "border-focus":
		if t.named {
			c, _ := toRGB(t.primary)
			return styled.Style{Fg: styled.Color(fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b))}
		}
		return styled.Style{Fg: "accent"}
	case "dim":
		return styled.Style{Dim: true}
	case "border":
		return styled.Style{Fg: "border"}
	case "error":
		return styled.Style{Fg: "red"}
	case "warning":
		return styled.Style{Fg: "yellow"}
	case "success":
		return styled.Style{Fg: "green"}
	case "header":
		return styled.Style{Bold: true}
	case "cursor", "selection":
		return styled.Style{Reverse: true}
	case "focus-background":
		// the background Textual tints a focused table with
		if t.named {
			c := blend(t.bg, t.fg, focusTint)
			return styled.Style{Bg: styled.Color(fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b))}
		}
		return styled.Style{}
	case "scrollbar":
		if t.named {
			p := Palettes[t.Name]
			return styled.Style{Fg: styled.Color(p.Scrollbar), Bg: styled.Color(p.ScrollbarBackground)}
		}
		return styled.Style{Fg: "border"}
	}
	return styled.Style{}
}

// DarkBG reports whether the background is dark (kit.Look): the named
// themes are; for the terminal's own background pqx assumes so, as Python
// pqx's theme does (dark=True).
func (t *Theme) DarkBG() bool { return true }

// build makes the role styles.
func (t *Theme) build() {
	t.Base = t.Lip(styled.Style{})
	t.Accent = t.Lip(styled.Style{Fg: "accent"})
	t.Dim = t.Lip(styled.Style{Dim: true})
	t.Border = t.Base.BorderForeground(t.out(t.border))
	t.FocusBorder = t.Base.BorderForeground(t.out(t.primary))
	if t.named {
		t.Border = t.Border.BorderBackground(t.out(t.bg.color()))
		t.FocusBorder = t.FocusBorder.BorderBackground(t.out(t.bg.color()))
	}
	t.Prompt = t.Base.Foreground(t.out(t.primary))
	t.Error = t.Lip(styled.Style{Fg: "red"})
	t.Warning = t.Lip(styled.Style{Fg: "yellow"})
	t.Success = t.Lip(styled.Style{Fg: "green"})
	t.Cursor = t.Lip(styled.Style{Reverse: true})
	t.Header = t.Lip(styled.Style{Bold: true})
	t.Selection = t.Lip(styled.Style{Reverse: true})
}

// Named reports whether a named theme (fixed palette) is in use.
func (t *Theme) Named() bool { return t.named }

// Foreground and Background are a named theme's colours; nil (the
// terminal's own) without one.
func (t *Theme) Foreground() color.Color {
	if !t.named {
		return nil
	}
	return t.out(t.fg.color())
}

// Background: see Foreground.
func (t *Theme) Background() color.Color {
	if !t.named {
		return nil
	}
	return t.out(t.bg.color())
}

// Primary is the focus colour: focused borders, dialog borders, the filter
// prompt. Without a named theme it is the accent.
func (t *Theme) Primary() color.Color { return t.out(t.primary) }

// BorderColor is the colour of unfocused borders.
func (t *Theme) BorderColor() color.Color { return t.out(t.border) }

// ParseBorder parses a --border colour as Textual parsed Python pqx's
// border variable: an ANSI name in lower case with or without the "ansi_"
// prefix (bright_black, ansi_white, default), or #rgb, #rgba, #rrggbb or
// #rrggbbaa in either case (an alpha is blended over black, as Textual draws
// it). Trailing whitespace is ignored; anything else is an error.
func ParseBorder(s string) (color.Color, error) {
	s = strings.TrimRight(s, " \t\n\r\f")
	if strings.HasPrefix(s, "#") {
		if c, ok := parseHexAlpha(s); ok {
			return c.color(), nil
		}
		return nil, fmt.Errorf("invalid colour %q (use #rgb or #rrggbb)", s)
	}
	name := strings.TrimPrefix(s, "ansi_")
	if name == "default" {
		return nil, nil
	}
	if i := ansiIndex(name); i >= 0 {
		return ansi.BasicColor(i), nil
	}
	return nil, fmt.Errorf("unknown colour %q (use an ANSI name such as bright_black or white, or #rrggbb)", s)
}

// parseHexAlpha is parseHex with the alpha blended over black.
func parseHexAlpha(s string) (rgb, bool) {
	c, ok := parseHex(s)
	h := s[1:]
	if !ok || (len(h) != 4 && len(h) != 8) {
		return c, ok
	}
	var a uint64
	var err error
	if len(h) == 4 {
		a, err = strconv.ParseUint(h[3:4]+h[3:4], 16, 8)
	} else {
		a, err = strconv.ParseUint(h[6:8], 16, 8)
	}
	if err != nil {
		return rgb{}, false
	}
	return blend(rgb{}, c, float64(a)/255), true
}

// Lip is the lipgloss style for a styled.Style. Colours are "" (the
// default), ANSI names, "color(N)", "#rrggbb", or the roles "accent" and
// "border".
func (t *Theme) Lip(s styled.Style) lipgloss.Style {
	st := lipgloss.NewStyle().TabWidth(lipgloss.NoTabConversion)
	if s.Dim && t.DimMode == "bright-black" {
		s.Fg, s.Dim = "bright_black", false
	}
	fg, bg := t.color(s.Fg), t.color(s.Bg)
	if t.named {
		// colours as given and faint as faint: Paint (the root applies it
		// to every frame) gives them the theme as Textual does, the theme's
		// colours where none is set, faint as a blend over the background
		// actually under the text (a focused table's is tinted)
		if fg != nil {
			st = st.Foreground(fg)
		}
		if bg != nil {
			st = st.Background(bg)
		}
		return st.Bold(s.Bold).Faint(s.Dim).Italic(s.Italic).Reverse(s.Reverse).Underline(s.Underline)
	}
	if fg != nil {
		st = st.Foreground(t.out(fg))
	}
	if bg != nil {
		st = st.Background(t.out(bg))
	}
	return st.Bold(s.Bold).Faint(s.Dim).Italic(s.Italic).Reverse(s.Reverse).Underline(s.Underline)
}

// color resolves a styled colour; nil is the default.
func (t *Theme) color(c styled.Color) color.Color {
	name := strings.TrimPrefix(strings.ToLower(string(c)), "ansi_")
	switch name {
	case "", "default":
		return nil
	case "accent":
		name = t.AccentName
	case "border":
		return t.border
	}
	if strings.HasPrefix(name, "#") {
		if h, ok := parseHex(name); ok {
			return h.color()
		}
		return nil
	}
	i := ansiIndex(name)
	if strings.HasPrefix(name, "color(") && strings.HasSuffix(name, ")") {
		n, err := strconv.Atoi(name[len("color(") : len(name)-1])
		if err != nil || n < 0 || n > 255 {
			return nil
		}
		i = n
	}
	switch {
	case i < 0:
		return nil
	case t.named:
		return xterm(i).color()
	case i < 16:
		return ansi.BasicColor(i)
	default:
		return ansi.IndexedColor(i)
	}
}

// Render turns styled text into terminal output. Spans later in the list
// win where they overlap, adding to the styles before them (as Rich does).
// Lines are separated by "\n"; Justify is left to the caller, who knows the
// width.
func (t *Theme) Render(x styled.Text) string {
	runes := []rune(x.Plain)
	if len(runes) == 0 {
		return ""
	}
	var b strings.Builder
	cur := t.at(x, 0)
	start := 0
	flush := func(end int) {
		if end <= start {
			return
		}
		st := t.Lip(cur)
		for i, line := range strings.Split(string(runes[start:end]), "\n") {
			if i > 0 {
				b.WriteByte('\n')
			}
			if line != "" {
				b.WriteString(st.Render(line))
			}
		}
	}
	for i := 1; i < len(runes); i++ {
		if s := t.at(x, i); s != cur {
			flush(i)
			cur, start = s, i
		}
	}
	flush(len(runes))
	return b.String()
}

// at is the style of rune i of x.
func (t *Theme) at(x styled.Text, i int) styled.Style {
	s := x.Style
	for _, sp := range x.Spans {
		if sp.Start <= i && i < sp.End {
			s = s.Plus(sp.Style)
		}
	}
	return s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func ansiIndex(name string) int {
	for i, n := range ansiNames {
		if n == name {
			return i
		}
	}
	return -1
}

// rgb is a truecolor colour.
type rgb struct{ r, g, b uint8 }

func (c rgb) color() color.Color { return color.RGBA{R: c.r, G: c.g, B: c.b, A: 0xff} }

func toRGB(c color.Color) (rgb, bool) {
	if c == nil {
		return rgb{}, false
	}
	r, g, b, _ := c.RGBA()
	return rgb{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}, true
}

// parseHex parses #rgb, #rgba, #rrggbb or #rrggbbaa (alpha ignored).
func parseHex(s string) (rgb, bool) {
	h := strings.TrimPrefix(s, "#")
	switch len(h) {
	case 3, 4:
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	case 6, 8:
		h = h[:6]
	default:
		return rgb{}, false
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return rgb{}, false
	}
	return rgb{uint8(v >> 16), uint8(v >> 8), uint8(v)}, true
}

func mustHex(s string) rgb {
	c, ok := parseHex(s)
	if !ok {
		panic("theme: bad colour " + s)
	}
	return c
}

// blend is Textual's Color.blend: from a towards b by f, truncated.
func blend(a, b rgb, f float64) rgb {
	mix := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*f) }
	return rgb{mix(a.r, b.r), mix(a.g, b.g), mix(a.b, b.b)}
}

// xterm is colour i of the 256-colour palette as a named theme draws it: the
// first 16 from Monokai, the rest the standard xterm cube and greys (Rich's
// EIGHT_BIT_PALETTE).
func xterm(i int) rgb {
	switch {
	case i < 16:
		return mustHex(monokai[i])
	case i < 232:
		i -= 16
		level := func(v int) uint8 {
			if v == 0 {
				return 0
			}
			return uint8(55 + 40*v)
		}
		return rgb{level(i / 36), level(i / 6 % 6), level(i % 6)}
	default:
		g := uint8(8 + 10*(i-232))
		return rgb{g, g, g}
	}
}
