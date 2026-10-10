package theme

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/mjuric/pqx/go/internal/styled"
)

// richColours is testdata/rich_colours.json (gen_rich_colours.py): the colour
// system Python pqx picks for an environment, and Rich's SGR parameters for
// the colours pqx draws on a 256- and a 16-colour terminal.
type richColours struct {
	Detect []struct {
		Env    map[string]string
		System string
	}
	Colours []struct {
		Spec string
		C256 string `json:"256"`
		C16  string `json:"16"`
	}
}

func loadRichColours(t *testing.T) richColours {
	t.Helper()
	b, err := os.ReadFile("testdata/rich_colours.json")
	if err != nil {
		t.Fatal(err)
	}
	var rc richColours
	if err := json.Unmarshal(b, &rc); err != nil {
		t.Fatal(err)
	}
	if len(rc.Detect) == 0 || len(rc.Colours) == 0 {
		t.Fatal("rich_colours.json is empty")
	}
	return rc
}

func TestDetectProfileAsTextual(t *testing.T) {
	want := map[string]colorprofile.Profile{
		"standard": colorprofile.ANSI, "256": colorprofile.ANSI256, "truecolor": colorprofile.TrueColor,
	}
	for _, c := range loadRichColours(t).Detect {
		var env []string
		for k, v := range c.Env {
			env = append(env, k+"="+v)
		}
		if got := DetectProfile(env); got != want[c.System] {
			t.Errorf("%v: %v, want %s (%v)", c.Env, got, c.System, want[c.System])
		}
	}
	// NO_COLOR, set to anything: no colours (Python pqx draws greys instead)
	for _, env := range [][]string{{"NO_COLOR=", "TERM=xterm-256color"}, {"TERM=screen", "NO_COLOR=1"}} {
		if got := DetectProfile(env); got != colorprofile.Ascii {
			t.Errorf("%v: %v, want Ascii", env, got)
		}
	}
	// the first of a repeated variable counts, as getenv takes it
	if got := DetectProfile([]string{"TERM=screen", "TERM=xterm-256color"}); got != colorprofile.ANSI {
		t.Errorf("repeated TERM: %v", got)
	}
}

// background is Rich's SGR parameters for a foreground colour, as a
// background.
func background(fg string) string {
	switch {
	case strings.HasPrefix(fg, "38;"):
		return "48;" + fg[3:]
	case strings.HasPrefix(fg, "3"):
		return "4" + fg[1:]
	case strings.HasPrefix(fg, "9"):
		return "10" + fg[1:]
	}
	return fg
}

// firstSGR is the parameters of the first SGR sequence in s.
func firstSGR(s string) string {
	i := strings.Index(s, "\x1b[")
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(s[i:], 'm')
	if j < 0 {
		return ""
	}
	return s[i+2 : i+j]
}

// Every colour pqx draws (the colourmaps' colours and indices, every
// 256-colour index, the themes' palettes) is reduced as Rich reduces it,
// whether it comes through the theme's styles (Render) or a frame's own SGR
// (Paint without a named theme).
func TestReduceAsRich(t *testing.T) {
	rc := loadRichColours(t)
	for _, sys := range []struct {
		p    colorprofile.Profile
		name string
	}{{colorprofile.ANSI256, "256"}, {colorprofile.ANSI, "16"}} {
		th := mustNew(t, "", "", "", "")
		th.SetProfile(sys.p)
		bad := 0
		fail := func(format string, a ...any) {
			if bad++; bad <= 10 {
				t.Errorf(sys.name+" colours: "+format, a...)
			}
		}
		for _, c := range rc.Colours {
			want := c.C256
			if sys.p == colorprofile.ANSI {
				want = c.C16
			}
			if got := firstSGR(th.Render(styled.New("x", styled.Style{Fg: styled.Color(c.Spec)}))); got != want {
				fail("Render fg %s: %q, want %q", c.Spec, got, want)
			}
			if got := firstSGR(th.Render(styled.New("x", styled.Style{Bg: styled.Color(c.Spec)}))); got != background(want) {
				fail("Render bg %s: %q, want %q", c.Spec, got, background(want))
			}
			var raw string
			if strings.HasPrefix(c.Spec, "#") {
				h, _ := parseHex(c.Spec)
				raw = "38;2;" + strconv.Itoa(int(h.r)) + ";" + strconv.Itoa(int(h.g)) + ";" + strconv.Itoa(int(h.b))
			} else {
				n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(c.Spec, "color("), ")"))
				if n < 16 && sys.p == colorprofile.ANSI256 {
					continue // kept as 38;5;N, the same colour as Rich's 3x/9x
				}
				raw = "38;5;" + strconv.Itoa(n)
			}
			frame := "a\x1b[1;" + raw + "mx\x1b[m"
			if got, w := th.Paint(frame, 2), "a\x1b[1;"+want+"mx\x1b[m"; got != w {
				fail("Paint %q: %q, want %q", frame, got, w)
			}
			frame = "\x1b[" + strings.Replace(raw, "38;", "48;", 1) + ";4mx"
			if got, w := th.Paint(frame, 1), "\x1b["+background(want)+";4mx"; got != w {
				fail("Paint %q: %q, want %q", frame, got, w)
			}
		}
	}
}

// A named theme on a 16-colour terminal: its foreground and background as
// Rich reduces them.
func TestNamedTheme16(t *testing.T) {
	rc := loadRichColours(t)
	std := map[string]string{}
	for _, c := range rc.Colours {
		std[c.Spec] = c.C16
	}
	th := mustNew(t, "", "", "", "tokyo-night")
	th.SetProfile(colorprofile.ANSI)
	p := Palettes["tokyo-night"]
	fg, bg := std[strings.ToLower(p.Foreground)], std[strings.ToLower(p.Background)]
	if fg == "" || bg == "" {
		t.Fatal("tokyo-night's colours are missing from rich_colours.json")
	}
	if got, want := painted(th, styled.Style{}), "\x1b[0;"+fg+";"+background(bg)+"m"; got != want {
		t.Errorf("tokyo-night: %s, want %s", sgr(got), sgr(want))
	}
}

// Paint leaves a frame alone where nothing needs reducing: truecolor, the
// 16 colours, a 256-colour index on a 256-colour terminal, other sequences.
func TestReduceFrameKeeps(t *testing.T) {
	th := mustNew(t, "", "", "", "")
	frame := "\x1b[38;2;1;2;3m\x1b[38;5;200;48;5;17mx\x1b[31;2m\x1b[?25ly\x1b[m"
	if got := th.Paint(frame, 2); got != frame {
		t.Errorf("truecolor: %q", got)
	}
	th.SetProfile(colorprofile.ANSI256)
	if got, want := th.Paint(frame, 2), "\x1b[38;5;16m\x1b[38;5;200;48;5;17mx\x1b[31;2m\x1b[?25ly\x1b[m"; got != want {
		t.Errorf("256: %q, want %q", got, want)
	}
	th.SetProfile(colorprofile.ANSI)
	if got, want := th.Paint(frame, 2), "\x1b[30m\x1b[35;44mx\x1b[31;2m\x1b[?25ly\x1b[m"; got != want {
		t.Errorf("16: %q, want %q", got, want)
	}
	// a cut-off or malformed sequence passes as it is
	for _, f := range []string{"x\x1b[38;2;1;2", "\x1b[38;2;1;2;999mx", "\x1b[38;5mx"} {
		if got := th.Paint(f, 1); got != f {
			t.Errorf("%q: %q", f, got)
		}
	}
}
