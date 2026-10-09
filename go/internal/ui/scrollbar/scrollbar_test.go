package scrollbar

import (
	"strings"
	"testing"

	"github.com/mjuric/pqx/go/internal/styled"
)

// render shows a bar as text: # a reversed blank, . a blank, a glyph
// followed by r when reversed or n when not.
func render(c []styled.Text) string {
	var b strings.Builder
	for _, x := range c {
		switch {
		case x.Plain == " " && x.Style.Reverse:
			b.WriteString("#")
		case x.Plain == " ":
			b.WriteString(".")
		case x.Style.Reverse:
			b.WriteString(x.Plain + "r")
		default:
			b.WriteString(x.Plain + "n")
		}
	}
	return b.String()
}

// Textual's ScrollBarRender.render_bar for a few cases (textual 8.2.8).
func TestBar(t *testing.T) {
	for _, c := range []struct {
		size, virtual, window, pos int
		vertical                   bool
		want                       string
	}{
		{3, 6, 3, 0, true, "#▄r."},
		{10, 10, 10, 0, true, ".........."},
		{10, 20, 10, 10, false, ".....#####"},
		{10, 30, 10, 5, false, ".▋r###....."},
		{116, 135, 116, 0, false, strings.Repeat("#", 99) + "▊n" + strings.Repeat(".", 16)},
		{31, 61, 31, 0, true, "###############▁r..............."},
		{31, 61, 31, 30, true, "...............▇n###############"},
		{31, 61, 31, 13, true, "......▄n###############▅r........"},
		{3, 6, 3, 1, true, "▄n#."},
		{10, 200, 10, 57, true, "..▃n▃r......"},
	} {
		if got := render(Bar(c.size, c.virtual, c.window, c.pos, c.vertical, styled.Style{Fg: "k"}, "")); got != c.want {
			t.Errorf("%+v: got %q", c, got)
		}
	}
}

// The colours: the thumb in Fg on thumbBg, the track and the ends' backs in Bg.
func TestColours(t *testing.T) {
	st := styled.Style{Fg: "thumb", Bg: "track"}
	b := Vertical(10, 200, 57, st, "focus")
	if b[0].Style != (styled.Style{Bg: "track"}) {
		t.Errorf("track %+v", b[0].Style)
	}
	if b[2].Style != (styled.Style{Fg: "thumb", Bg: "track"}) || b[3].Style != (styled.Style{Fg: "thumb", Bg: "track", Reverse: true}) {
		t.Errorf("ends %+v %+v", b[2].Style, b[3].Style)
	}
	full := Vertical(4, 8, 0, st, "focus")
	if full[0].Style != (styled.Style{Fg: "thumb", Bg: "focus", Reverse: true}) {
		t.Errorf("thumb %+v", full[0].Style)
	}
	if w := Widen(b, 2); w[2].Plain != "▃▃" || w[0].Plain != "  " || w[2].Style != b[2].Style {
		t.Errorf("widen %+v", w[2])
	}
}
