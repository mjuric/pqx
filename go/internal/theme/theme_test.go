package theme

import (
	"fmt"
	"strings"
	"testing"

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
	for _, bad := range []string{"orange", "bogus", "ansi_orange", "#12", "#ggg"} {
		if _, err := New("", "", bad, ""); err == nil {
			t.Errorf("border %q accepted", bad)
		}
	}
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
		if got := hexOf(th.BorderColor()); !strings.EqualFold(got, w[0]) {
			t.Errorf("%s border %s, want %s", name, got, w[0])
		}
		dim := th.Lip(styled.Style{Dim: true})
		if got := hexOf(dim.GetForeground()); got != w[1] {
			t.Errorf("%s dim %s, want %s", name, got, w[1])
		}
		dimRed := th.Lip(styled.Style{Dim: true, Fg: "red"})
		if got := hexOf(dimRed.GetForeground()); got != w[2] {
			t.Errorf("%s dim red %s, want %s", name, got, w[2])
		}
		p := Palettes[name]
		if got := hexOf(th.Primary()); !strings.EqualFold(got, p.Primary) {
			t.Errorf("%s primary %s", name, got)
		}
		if got := hexOf(th.Base.GetBackground()); !strings.EqualFold(got, p.Background) {
			t.Errorf("%s background %s", name, got)
		}
		if got := hexOf(th.Base.GetForeground()); !strings.EqualFold(got, p.Foreground) {
			t.Errorf("%s foreground %s", name, got)
		}
		if got := hexOf(th.Lip(styled.Style{Fg: "color(200)"}).GetForeground()); got != "#ff00d7" {
			t.Errorf("%s color(200) %s", name, got)
		}
		if got := hexOf(th.Accent.GetForeground()); got != "#9d65ff" { // ansi blue, as Python's tab titles
			t.Errorf("%s accent %s", name, got)
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
