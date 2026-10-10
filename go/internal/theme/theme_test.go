package theme

import (
	"encoding/json"
	"fmt"
	"image"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/mjuric/pqx/go/internal/styled"
)

func mustNew(t *testing.T, accent, dim, border, name string) *Theme {
	t.Helper()
	th, err := New(accent, dim, border, name)
	if err != nil {
		t.Fatal(err)
	}
	return th
}

func sgr(s string) string { return fmt.Sprintf("%q", s) }

func TestTerminalPalette(t *testing.T) {
	th := mustNew(t, "", "", "", "")
	if th.Named() || th.Foreground() != nil || th.Background() != nil {
		t.Error("the default look should be the terminal's own colours")
	}
	cases := []struct {
		st   styled.Style
		want string
	}{
		{styled.Style{}, "x"},
		{styled.Style{Fg: "accent"}, "\x1b[34mx\x1b[m"},
		{styled.Style{Fg: "red"}, "\x1b[31mx\x1b[m"},
		{styled.Style{Fg: "ansi_bright_black"}, "\x1b[90mx\x1b[m"},
		{styled.Style{Fg: "border"}, "\x1b[90mx\x1b[m"},
		{styled.Style{Dim: true}, "\x1b[2mx\x1b[m"},
		{styled.Style{Bold: true, Fg: "green"}, "\x1b[1;32mx\x1b[m"},
		{styled.Style{Fg: "color(200)"}, "\x1b[38;5;200mx\x1b[m"},
		{styled.Style{Fg: "color(9)"}, "\x1b[91mx\x1b[m"},
		{styled.Style{Fg: "#ff8000"}, "\x1b[38;2;255;128;0mx\x1b[m"},
		{styled.Style{Reverse: true}, "\x1b[7mx\x1b[m"},
		{styled.Style{Fg: "nonsense"}, "x"},
	}
	for _, c := range cases {
		if got := th.Render(styled.New("x", c.st)); got != c.want {
			t.Errorf("%+v -> %s, want %s", c.st, sgr(got), sgr(c.want))
		}
	}
}

func TestAccentDimBorder(t *testing.T) {
	for _, a := range Accents {
		th := mustNew(t, a, "", "", "")
		want := fmt.Sprintf("\x1b[%dmx\x1b[m", 30+ansiIndex(a))
		if got := th.Render(styled.New("x", styled.Style{Fg: "accent"})); got != want {
			t.Errorf("accent %s: %s", a, sgr(got))
		}
	}
	// an invalid value (from the environment) is the default, as Python's
	if th := mustNew(t, "red", "bold", "", ""); th.AccentName != "blue" || th.DimMode != "faint" {
		t.Errorf("invalid values: %s %s", th.AccentName, th.DimMode)
	}
	th := mustNew(t, "", "bright-black", "", "")
	if got := th.Render(styled.New("x", styled.Style{Dim: true})); got != "\x1b[90mx\x1b[m" {
		t.Errorf("bright-black dim: %s", sgr(got))
	}
	if got := th.Dim.Render("x"); got != "\x1b[90mx\x1b[m" {
		t.Errorf("Dim style: %s", sgr(got))
	}
	for border, want := range map[string]string{
		"white":             "\x1b[37mx\x1b[m",
		"ansi_white":        "\x1b[37mx\x1b[m",
		"bright_black":      "\x1b[90mx\x1b[m",
		"ansi_bright_black": "\x1b[90mx\x1b[m",
		"#123":              "\x1b[38;2;17;34;51mx\x1b[m",
		"#808080":           "\x1b[38;2;128;128;128mx\x1b[m",
		"default":           "x",
	} {
		th := mustNew(t, "", "", border, "")
		if got := th.Render(styled.New("x", styled.Style{Fg: "border"})); got != want {
			t.Errorf("border %s: %s, want %s", border, sgr(got), sgr(want))
		}
	}
	// as Textual parsed Python pqx's border: trailing spaces are fine, case
	// matters for names, an alpha is blended over black
	for border, want := range map[string]string{
		"red ": "#800000", "ansi_red  ": "#800000", "#FF000080": "#800000", "#ff000080": "#800000",
		"#f008": "#880000", "#123 ": "#112233", "red\t": "", "white\n": "", "#123\t": "#112233", "#ABCDEF": "#abcdef",
	} {
		c, err := ParseBorder(border)
		if err != nil {
			t.Errorf("%q: %v", border, err)
			continue
		}
		if r, g, b, _ := c.RGBA(); strings.HasPrefix(border, "#") && fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8) != want {
			t.Errorf("%q: %s, want %s", border, hexOf(c), want)
		}
	}
	for _, bad := range []string{"orange", "bogus", "ansi_orange", "#12", "#ggg", " red", "RED", "ansi_RED", "Red", " ", "\tred", "#12345678zz", "#1234567g"} {
		if _, err := New("", "", bad, ""); err == nil {
			t.Errorf("border %q accepted", bad)
		}
	}
}

// painted is the SGR sequence the frame painted with th has before "x"
// drawn in style st.
func painted(th *Theme, st styled.Style) string {
	out := th.Paint(th.Render(styled.New("x", st)), 1)
	i := strings.Index(out, "x")
	j := strings.LastIndex(out[:i], "\x1b[")
	return out[j:i]
}

// colours of a painted SGR sequence (truecolor profile): fg and bg as #hex.
func paintedColours(t *testing.T, th *Theme, st styled.Style) (fg, bg string) {
	t.Helper()
	ps := strings.Split(strings.TrimSuffix(strings.TrimPrefix(painted(th, st), "\x1b["), "m"), ";")
	for i := 0; i+4 < len(ps); i++ {
		if (ps[i] == "38" || ps[i] == "48") && ps[i+1] == "2" {
			r, _ := strconv.Atoi(ps[i+2])
			g, _ := strconv.Atoi(ps[i+3])
			b, _ := strconv.Atoi(ps[i+4])
			h := fmt.Sprintf("#%02x%02x%02x", r, g, b)
			if ps[i] == "38" {
				fg = h
			} else {
				bg = h
			}
		}
	}
	return fg, bg
}

func TestNamedThemes(t *testing.T) {
	// Python pqx on Textual: the unfocused border is "$primary 45%" over the
	// background; dim text is blended to 66%; ANSI red is Monokai's.
	want := map[string][3]string{
		"tokyo-night":      {"#625484", "#787e9a", "#a9094b"},
		"dracula":          {"#6B598D", "#b1b1b2", "#ae0e51"},
		"catppuccin-mocha": {"#7B647C", "#8f95ad", "#a9084b"},
		"nord":             {"#567380", "#9ea4af", "#b01154"},
		"gruvbox":          {"#51605A", "#b3ac90", "#ae0d4c"},
	}
	for _, name := range Names {
		th := mustNew(t, "", "", "", name)
		if !th.Named() || th.Name != name {
			t.Fatalf("%s: not named", name)
		}
		w := want[name]
		p := Palettes[name]
		if got := hexOf(th.BorderColor()); !strings.EqualFold(got, w[0]) {
			t.Errorf("%s border %s, want %s", name, got, w[0])
		}
		if fg, _ := paintedColours(t, th, styled.Style{Fg: "border"}); !strings.EqualFold(fg, w[0]) {
			t.Errorf("%s painted border %s, want %s", name, fg, w[0])
		}
		if fg, bg := paintedColours(t, th, styled.Style{Dim: true}); fg != w[1] || !strings.EqualFold(bg, p.Background) {
			t.Errorf("%s dim %s on %s, want %s", name, fg, bg, w[1])
		}
		if fg, _ := paintedColours(t, th, styled.Style{Dim: true, Fg: "red"}); fg != w[2] {
			t.Errorf("%s dim red %s, want %s", name, fg, w[2])
		}
		if got := hexOf(th.Primary()); !strings.EqualFold(got, p.Primary) {
			t.Errorf("%s primary %s", name, got)
		}
		if fg, bg := paintedColours(t, th, styled.Style{}); !strings.EqualFold(fg, p.Foreground) || !strings.EqualFold(bg, p.Background) {
			t.Errorf("%s plain %s on %s", name, fg, bg)
		}
		if fg, _ := paintedColours(t, th, styled.Style{Fg: "color(200)"}); fg != "#ff00d7" {
			t.Errorf("%s color(200) %s", name, fg)
		}
		if fg, _ := paintedColours(t, th, styled.Style{Fg: "accent"}); fg != "#9d65ff" { // ansi blue, as Python's tab titles
			t.Errorf("%s accent %s", name, fg)
		}
		// --border and --accent don't apply to a named theme (as in Python)
		if got := hexOf(mustNew(t, "green", "", "white", name).BorderColor()); !strings.EqualFold(got, w[0]) {
			t.Errorf("%s with --border: %s", name, got)
		}
	}
	_, err := New("", "", "", "monokai")
	if err == nil || !strings.Contains(err.Error(), "tokyo-night, dracula, catppuccin-mocha, nord, gruvbox") {
		t.Errorf("unknown theme: %v", err)
	}
}

// A focused table's background is tinted 5% towards the foreground, and
// its faint text blends over that (Textual's background-tint).
func TestFocusTint(t *testing.T) {
	th := mustNew(t, "", "", "", "tokyo-night")
	frame := th.Render(styled.New("ab", styled.Style{Dim: true}))
	out := th.PaintTinted(frame, 2, image.Rect(1, 0, 2, 1))
	if !strings.Contains(out, "38;2;120;126;154;48;2;26;27;38ma") || !strings.Contains(out, "38;2;122;128;156;48;2;33;34;46mb") {
		t.Errorf("tinted: %q", out)
	}
	th.SetProfile(colorprofile.ANSI256)
	out = th.PaintTinted(frame, 2, image.Rect(0, 0, 2, 1))
	if !strings.Contains(out, "38;5;245;48;5;16m") { // what Python shows (#7a809c)
		t.Errorf("tinted, 256 colours: %q", out)
	}
}

func hexOf(c interface{ RGBA() (r, g, b, a uint32) }) string {
	if c == nil {
		return "nil"
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

func TestRenderSpans(t *testing.T) {
	th := mustNew(t, "", "", "", "")
	x := styled.New("ab cd\nef", styled.Style{})
	x.Spans = []styled.Span{
		{Start: 0, End: 4, Style: styled.Style{Fg: "red"}},
		{Start: 3, End: 7, Style: styled.Style{Bold: true}},
	}
	got := th.Render(x)
	want := "\x1b[31mab \x1b[m" + "\x1b[1;31mc\x1b[m" + "\x1b[1md\x1b[m\n\x1b[1me\x1b[m" + "f"
	if got != want {
		t.Errorf("got  %s\nwant %s", sgr(got), sgr(want))
	}
	var y styled.Text
	y.Append("✓", styled.Style{Fg: "green"})
	y.Append(" done ", styled.Style{})
	y.Append("✗", styled.Style{Fg: "red"})
	y.Append("\ttab", styled.Style{Dim: true})
	if got := th.Render(y); got != "\x1b[32m✓\x1b[m done \x1b[31m✗\x1b[m\x1b[2m\ttab\x1b[m" {
		t.Errorf("got %s", sgr(got))
	}
	if th.Render(styled.Text{}) != "" {
		t.Error("empty text")
	}
}

func TestRoles(t *testing.T) {
	th := mustNew(t, "cyan", "", "", "")
	for role, want := range map[string]string{
		"accent": "\x1b[36mx\x1b[m", "border-focus": "\x1b[36mx\x1b[m", "dim": "\x1b[2mx\x1b[m",
		"border": "\x1b[90mx\x1b[m", "error": "\x1b[31mx\x1b[m", "warning": "\x1b[33mx\x1b[m",
		"success": "\x1b[32mx\x1b[m", "header": "\x1b[1mx\x1b[m", "cursor": "\x1b[7mx\x1b[m",
		"selection": "\x1b[7mx\x1b[m",
	} {
		if got := th.Render(styled.New("x", th.Style(role))); got != want {
			t.Errorf("%s: %s, want %s", role, sgr(got), sgr(want))
		}
	}
	nord := mustNew(t, "", "", "", "nord")
	if got := hexOf(nord.Lip(nord.Style("border-focus")).GetForeground()); got != "#88c0d0" {
		t.Errorf("nord border-focus %s", got)
	}
	if got := hexOf(nord.Lip(nord.Style("border")).GetForeground()); got != "#567380" {
		t.Errorf("nord border %s", got)
	}
}

// testdata/rich_downgrade.json: Rich's Color.downgrade of 2,262 colours to
// 256 and to 16 colours (from Python, rich 14).
func TestRichDowngrade(t *testing.T) {
	b, err := os.ReadFile("testdata/rich_downgrade.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases [][5]int
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, c := range cases {
		r, g, bl := uint8(c[0]), uint8(c[1]), uint8(c[2])
		if got := rich256(r, g, bl); int(got) != c[3] && bad < 10 {
			bad++
			t.Errorf("rich256(%d,%d,%d) = %d, want %d", r, g, bl, got, c[3])
		}
		if got := richStandard(r, g, bl); got != c[4] && bad < 10 {
			bad++
			t.Errorf("richStandard(%d,%d,%d) = %d, want %d", r, g, bl, got, c[4])
		}
	}
}

// With a 256-colour terminal a named theme's colours are Rich's choices
// (what Python pqx shows).
func TestProfile256(t *testing.T) {
	th := mustNew(t, "", "", "", "tokyo-night")
	th.SetProfile(colorprofile.ANSI256)
	for _, c := range []struct {
		st   styled.Style
		want string
	}{
		{styled.Style{}, "\x1b[0;38;5;146;48;5;16m"},                           // #a9b1d6 on #1a1b26
		{styled.Style{Dim: true}, "\x1b[0;38;5;244;48;5;16m"},                  // #787e9a
		{styled.Style{Fg: "border"}, "\x1b[0;38;5;60;48;5;16m"},                // #625484
		{styled.Style{Fg: "accent", Bold: true}, "\x1b[0;1;38;5;135;48;5;16m"}, // Monokai blue #9d65ff
		{styled.Style{Fg: "cyan"}, "\x1b[0;38;5;80;48;5;16m"},                  // #58d1eb
	} {
		if got := painted(th, c.st); got != c.want {
			t.Errorf("%+v: %s, want %s", c.st, sgr(got), sgr(c.want))
		}
	}
	th = mustNew(t, "", "", "#123456", "")
	th.SetProfile(colorprofile.ANSI)
	if got := th.Render(styled.New("x", styled.Style{Fg: "border"})); got != "\x1b[90mx\x1b[m" { // as Rich: bright black
		t.Errorf("16 colours: %s", sgr(got))
	}
}

// Paint gives every cell of a frame the named theme's colours, as Textual
// does: default colours become the theme's, ANSI colours Monokai's, faint
// a blend, short lines are padded; without a named theme nothing changes.
func TestPaint(t *testing.T) {
	plain := mustNew(t, "", "", "", "")
	if got := plain.Paint("a\x1b[31mb\x1b[m", 5); got != "a\x1b[31mb\x1b[m" {
		t.Errorf("terminal palette: %q", got)
	}
	th := mustNew(t, "", "", "", "tokyo-night")
	th.SetProfile(colorprofile.ANSI256)
	base := "\x1b[0;38;5;146;48;5;16m"
	cases := []struct{ in, want string }{
		{"ab", base + "ab" + "\x1b[m"},
		{"a", base + "a" + base + "   " + base + "\x1b[m"},                                         // padded to 4
		{"\x1b[31mab\x1b[m", base + "\x1b[0;38;5;197;48;5;16m" + "ab" + base + "\x1b[m"},           // red: Monokai #f4005f
		{"\x1b[2mab\x1b[22m", base + "\x1b[0;38;5;244;48;5;16m" + "ab" + base + "\x1b[m"},          // faint: #787e9a
		{"\x1b[1;7mab\x1b[0m", base + "\x1b[0;1;7;38;5;146;48;5;16m" + "ab" + base + "\x1b[m"},     // bold reverse
		{"\x1b[38;5;200mab\x1b[39m", base + "\x1b[0;38;5;200;48;5;16m" + "ab" + base + "\x1b[m"},   // 256-colour
		{"\x1b[38;2;255;0;0mab\x1b[m", base + "\x1b[0;38;5;196;48;5;16m" + "ab" + base + "\x1b[m"}, // truecolor, reduced
		{"\x1b[?25lab", base + "\x1b[?25lab" + "\x1b[m"},                                           // another sequence
	}
	for _, c := range cases {
		w := 2
		if c.in == "a" {
			w = 4
		}
		if got := th.Paint(c.in, w); got != c.want {
			t.Errorf("Paint(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
	// the pen carries across lines
	got := th.Paint("\x1b[31ma\nb", 1)
	red := "\x1b[0;38;5;197;48;5;16m"
	if got != base+red+"a\n"+red+"b\x1b[m" {
		t.Errorf("two lines: %q", got)
	}
}

// The scrollbar role: pqx's own theme's (the border colour on the
// terminal's background), or a named theme's Textual $scrollbar and
// $scrollbar-background (textual 8.2.8's ColorSystem.generate).
func TestScrollbarRole(t *testing.T) {
	plain, _ := New("blue", "faint", "", "")
	if s := plain.Style("scrollbar"); s.Fg != "border" || s.Bg != "" {
		t.Fatalf("plain %+v", s)
	}
	tn, _ := New("blue", "faint", "", "tokyo-night")
	if s := tn.Style("scrollbar"); s.Fg != "#4F4270" || s.Bg != "#070817" {
		t.Fatalf("tokyo-night %+v", s)
	}
	for _, n := range Names {
		if p := Palettes[n]; p.Scrollbar == "" || p.ScrollbarBackground == "" {
			t.Errorf("%s has no scrollbar colours", n)
		}
	}
}

// Paint reduces each colour once (#4F4270 → 59), and a 256-colour index in
// the frame as Textual does: its truecolor value, reduced by Rich (59 →
// 240, 188 → 252 at 256 colours; Python pqx sends these).
func TestPaintReducesOnce(t *testing.T) {
	th, _ := New("blue", "faint", "", "tokyo-night")
	th.SetProfile(colorprofile.ANSI256)
	out := th.Paint(th.Render(styled.New("x", th.Style("scrollbar"))), 1)
	if !strings.Contains(out, "38;5;59") || strings.Contains(out, "38;5;240") {
		t.Fatalf("%q", out)
	}
	out = th.Paint("\x1b[38;5;59mx\x1b[38;5;188my\x1b[48;5;145mz", 3)
	for _, want := range []string{"38;5;240;", "38;5;252;", "48;5;248m"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q lacks %q", out, want)
		}
	}
}

// focus-background: a named theme's background tinted 5% towards its
// foreground (#1A1B26 → #21222e for tokyo-night, as Textual draws a
// focused table); nothing for pqx's own theme.
func TestFocusBackgroundRole(t *testing.T) {
	tn, _ := New("blue", "faint", "", "tokyo-night")
	if s := tn.Style("focus-background"); s.Bg != "#21222e" {
		t.Fatalf("%+v", s)
	}
	plain, _ := New("blue", "faint", "", "")
	if s := plain.Style("focus-background"); s != (styled.Style{}) {
		t.Fatalf("%+v", s)
	}
}
