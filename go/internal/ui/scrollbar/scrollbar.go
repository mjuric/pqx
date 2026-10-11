// Package scrollbar draws Textual's scrollbars (ScrollBarRender.render_bar)
// for the parts that scroll: lists, tables, the details pane and the help.
// The colours come from the Look role "scrollbar" (Fg the thumb, Bg the
// track); a focused widget whose background Textual tints puts the thumb on
// that background ("focus-background").
package scrollbar

import (
	"math"
	"strings"

	"github.com/mjuric/pqx/go/internal/styled"
)

var (
	verticalBars   = []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", " "}
	horizontalBars = []string{"▉", "▊", "▋", "▌", "▍", "▎", "▏", " "}
)

// Bar is ScrollBarRender.render_bar: size cells of a bar for a window of
// window cells at position over virtual cells, the thumb in st's Fg (in
// reverse video, on thumbBg) on a track in st's Bg, the thumb's ends in
// eighths of a cell. With nothing to scroll it is all track.
func Bar(size, virtual, window, position int, vertical bool, st styled.Style, thumbBg styled.Color) []styled.Text {
	bar, back := st.Fg, st.Bg
	bars := horizontalBars
	if vertical {
		bars = verticalBars
	}
	out := make([]styled.Text, max(0, size))
	for i := range out {
		out[i] = styled.New(" ", styled.Style{Bg: back})
	}
	if window >= virtual {
		window = 0
	}
	if window == 0 || size <= 0 || virtual == 0 || size == virtual {
		return out
	}
	n := len(bars)
	thumb := math.Max(1, float64(window)/(float64(virtual)/float64(size)))
	pos := (float64(size) - thumb) * (float64(position) / float64(virtual-window))
	start := int(pos * float64(n))
	end := start + int(math.Ceil(thumb*float64(n)))
	si, sb := max(0, start)/n, max(0, start)%n
	ei, eb := max(0, end)/n, max(0, end)%n
	for i := si; i < min(ei, size); i++ {
		out[i] = styled.New(" ", styled.Style{Fg: bar, Bg: thumbBg, Reverse: true})
	}
	if si < size {
		if c := bars[n-1-sb]; c != " " {
			out[si] = styled.New(c, styled.Style{Fg: bar, Bg: back, Reverse: !vertical})
		}
	}
	if ei < size {
		if c := bars[n-1-eb]; c != " " {
			out[ei] = styled.New(c, styled.Style{Fg: bar, Bg: back, Reverse: vertical})
		}
	}
	return out
}

// Vertical is the vertical bar of a view size rows high over virtual rows,
// scrolled to position.
func Vertical(size, virtual, position int, st styled.Style, thumbBg styled.Color) []styled.Text {
	return Bar(size, virtual, size, position, true, st, thumbBg)
}

// Widen makes each cell of a vertical bar width cells wide (a scrollbar
// thicker than one cell repeats its glyph).
func Widen(cells []styled.Text, width int) []styled.Text {
	out := make([]styled.Text, len(cells))
	for i, c := range cells {
		c.Plain = strings.Repeat(c.Plain, width)
		out[i] = c
	}
	return out
}
