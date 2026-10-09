package dialogs

import (
	"math"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Author and Homepage are pqx's (pqx/__init__.py).
const (
	Author   = "Mario Juric"
	Homepage = "https://github.com/mjuric/pqx"
)

// helpDialog shows the help (Python's HelpScreen): a header with the
// version, author and home page, the help text scrolling, "Esc to close".
type helpDialog struct {
	env   *kit.Env
	top   int
	lines []string // the help rendered for width
	width int
	bodyH int
}

func newHelp(env *kit.Env) *helpDialog { return &helpDialog{env: env} }

// Size: #help-box is 96 wide and 90% high.
func (d *helpDialog) Size(w, h int) (int, int) { return min(96, w*95/100), max(8, h*90/100) }

func (d *helpDialog) Keys() []kit.KeyHint { return nil }

func (d *helpDialog) render(iw int) {
	if iw == d.width && d.lines != nil {
		return
	}
	d.width = iw
	d.lines = d.lines[:0]
	for _, t := range renderMarkdown(d.env.Look, Help, iw-scrollbarW) {
		d.lines = append(d.lines, fit(d.env.Look.Render(t), iw-scrollbarW))
	}
}

// scrollbarW is the vertical scroll bar's width (Textual's default).
const scrollbarW = 2

func (d *helpDialog) View(w, h int) string {
	look := d.env.Look
	iw := innerW(w)
	d.render(iw)
	dim := look.Style("dim")
	head := line(look, "pqx", styled.Style{Bold: true}, " "+d.env.Opts.Version, styled.Style{},
		"  ·  ", dim, "Written by "+Author, styled.Style{}, "  ·  ", dim, Homepage, dim)
	// header, margin, the text, margin, footer
	d.bodyH = max(1, h-2-2*padY-4)
	d.clamp()
	bar := scrollbar(look, d.bodyH, len(d.lines), d.top)
	content := []string{head, ""}
	for i := 0; i < d.bodyH; i++ {
		l := strings.Repeat(" ", iw-scrollbarW)
		if k := d.top + i; k < len(d.lines) {
			l = d.lines[k]
		}
		content = append(content, l+bar[i])
	}
	content = append(content, "", text(look, "Esc to close", dim))
	return box(look, content, w, h)
}

// scrollbar is Textual's vertical ScrollBarRender for a window of size rows
// over virtual rows at position: a thumb in bright black (reverse video)
// with eighth-block ends, as rows scrollbarW wide.
func scrollbar(look kit.Look, size, virtual, position int) []string {
	blank := strings.Repeat(" ", scrollbarW)
	out := make([]string, size)
	for i := range out {
		out[i] = blank
	}
	if virtual <= size || size <= 0 {
		return out
	}
	bars := []rune("▁▂▃▄▅▆▇ ")
	nb := len(bars)
	thumb := max(1, float64(size)/(float64(virtual)/float64(size)))
	pos := (float64(size) - thumb) * float64(position) / float64(virtual-size)
	start := int(pos * float64(nb))
	end := start + int(math.Ceil(thumb*float64(nb)))
	si, sb := start/nb, start%nb
	ei, eb := end/nb, end%nb
	thumbStyle := styled.Style{Fg: "bright_black", Reverse: true}
	for i := si; i < min(ei, size); i++ {
		out[i] = text(look, blank, thumbStyle)
	}
	if si < size {
		if c := bars[nb-1-sb]; c != ' ' {
			out[si] = text(look, strings.Repeat(string(c), scrollbarW), styled.Style{Fg: "bright_black"})
		}
	}
	if ei < size {
		if c := bars[nb-1-eb]; c != ' ' {
			out[ei] = text(look, strings.Repeat(string(c), scrollbarW), thumbStyle)
		}
	}
	return out
}

func (d *helpDialog) clamp() {
	d.top = max(0, min(d.top, len(d.lines)-d.bodyH))
}

func (d *helpDialog) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case tea.KeyPressMsg:
		switch m.String() {
		case "esc", "q", "?":
			return closeWith()
		case "up", "k":
			d.top--
		case "down", "j":
			d.top++
		case "pgup":
			d.top -= d.bodyH
		case "pgdown", "space":
			d.top += d.bodyH
		case "home":
			d.top = 0
		case "end":
			d.top = len(d.lines)
		}
		d.clamp()
	case tea.MouseWheelMsg:
		d.top += 3 * wheel(m)
		d.clamp()
	}
	return nil
}
