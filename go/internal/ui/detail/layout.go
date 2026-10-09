package detail

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mjuric/pqx/go/internal/cells"
	"github.com/mjuric/pqx/go/internal/styled"
)

// An entry is laid out as Python pqx's EntryGrid lays it out (Rich's
// Table.grid with padding (0, 2)): the name in a fixed-width column on its
// first line, cut with "…" if longer, then two spaces, then the value folded
// into the rest of the width, its lines padded to it. Wrapping follows
// Rich's (rich/_wrap.py divide_line, Text.wrap): words break at whitespace,
// a word wider than the column is folded, and whitespace past the end of a
// line is dropped.

// runeWidth is a character's cells (Rich's cell_len).
func runeWidth(r rune) int {
	if r < 0x80 {
		if r < 0x20 || r == 0x7F {
			return 0
		}
		return 1
	}
	var b [utf8.UTFMax]byte
	n := utf8.EncodeRune(b[:], r)
	return cells.Width(string(b[:n]))
}

// cellLen is the cells of s.
func cellLen(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

// words splits s as Rich's re_word (\s*\S+\s*) does: each word with the
// whitespace before (the first) and after it. Offsets are in runes.
func words(rs []rune) [][2]int {
	var out [][2]int
	i := 0
	for i < len(rs) {
		start := i
		for i < len(rs) && unicode.IsSpace(rs[i]) {
			i++
		}
		if i == len(rs) {
			break // only whitespace left: no word
		}
		for i < len(rs) && !unicode.IsSpace(rs[i]) {
			i++
		}
		for i < len(rs) && unicode.IsSpace(rs[i]) {
			i++
		}
		out = append(out, [2]int{start, i})
	}
	return out
}

// chopCells cuts rs into pieces of at most width cells (Rich's chop_cells),
// returning the pieces' lengths in runes.
func chopCells(rs []rune, width int) []int {
	var out []int
	size, start := 0, 0
	for i, r := range rs {
		w := runeWidth(r)
		if size+w > width { // (an empty piece if r is wider than width, as Rich)
			out = append(out, i-start)
			start, size = i, 0
		}
		size += w
	}
	if size > 0 || start < len(rs) {
		out = append(out, len(rs)-start)
	}
	return out
}

// divideLine is where Rich breaks a line of text to fit width (rune
// offsets), folding words wider than it.
func divideLine(rs []rune, width int) []int {
	var breaks []int
	offset := 0
	for _, w := range words(rs) {
		start, end := w[0], w[1]
		word := rs[start:end]
		trimmed := len(word)
		for trimmed > 0 && unicode.IsSpace(word[trimmed-1]) {
			trimmed--
		}
		wordLen := cellLen(string(word[:trimmed]))
		remaining := width - offset
		switch {
		case remaining >= wordLen:
			offset += cellLen(string(word))
		case wordLen > width:
			pieces := chopCells(word, width)
			for i, n := range pieces {
				if start > 0 {
					breaks = append(breaks, start)
				}
				if i == len(pieces)-1 {
					offset = cellLen(string(rs[start : start+n]))
				} else {
					start += n
				}
			}
		case offset > 0 && start > 0:
			breaks = append(breaks, start)
			offset = cellLen(string(word))
		}
	}
	return breaks
}

// wrapText folds t into lines of exactly width cells (Rich's Text.wrap
// with justify "left" and overflow "fold"), keeping its styles.
func wrapText(t styled.Text, width int) []styled.Text {
	var out []styled.Text
	for _, line := range t.Lines() {
		line = expandTabs(line)
		rs := []rune(line.Plain)
		breaks := divideLine(rs, width)
		prev := 0
		for _, b := range append(breaks, len(rs)) {
			out = append(out, fitLine(slice(line, prev, b), width))
			prev = b
		}
	}
	return out
}

// expandTabs turns the tabs of a line into spaces up to the next multiple
// of 8 cells (Rich's Text.expand_tabs: a tab takes at least one space).
func expandTabs(t styled.Text) styled.Text {
	if !strings.ContainsRune(t.Plain, '\t') {
		return t
	}
	rs := []rune(t.Plain)
	at := make([]int, len(rs)+1) // new rune offset of each old one
	var b strings.Builder
	pos, n := 0, 0
	for i, r := range rs {
		at[i] = n
		if r == '\t' {
			k := 8 - (pos+1)%8
			if k == 8 {
				k = 0
			}
			k++
			b.WriteString(strings.Repeat(" ", k))
			pos += k
			n += k
			continue
		}
		b.WriteRune(r)
		pos += runeWidth(r)
		n++
	}
	at[len(rs)] = n
	out := styled.Text{Plain: b.String(), Style: t.Style, Justify: t.Justify}
	for _, sp := range t.Spans {
		out.Spans = append(out.Spans, styled.Span{Start: at[max(0, min(sp.Start, len(rs)))], End: at[max(0, min(sp.End, len(rs)))], Style: sp.Style})
	}
	return out
}

// slice is runes [a, b) of t with their styles.
func slice(t styled.Text, a, b int) styled.Text {
	rs := []rune(t.Plain)
	out := styled.Text{Plain: string(rs[a:b]), Style: t.Style}
	for _, sp := range t.Spans {
		s, e := max(sp.Start, a), min(sp.End, b)
		if s < e {
			out.Spans = append(out.Spans, styled.Span{Start: s - a, End: e - a, Style: sp.Style})
		}
	}
	return out
}

// fitLine drops whitespace past width, then cuts or pads t to width cells
// (Rich's rstrip_end, then truncate with pad).
func fitLine(t styled.Text, width int) styled.Text {
	rs := []rune(t.Plain)
	if excess := len(rs) - width; excess > 0 {
		n := 0
		for n < len(rs) && unicode.IsSpace(rs[len(rs)-1-n]) {
			n++
		}
		if cut := min(n, excess); cut > 0 {
			t = slice(t, 0, len(rs)-cut)
		}
	}
	if w := cellLen(t.Plain); w > width {
		t = setCellSize(t, width)
	} else if w < width {
		t.Plain += strings.Repeat(" ", width-w)
	}
	return t
}

// setCellSize cuts t to width cells, padding with a space where a wide
// character would straddle the edge (Rich's set_cell_size).
func setCellSize(t styled.Text, width int) styled.Text {
	rs := []rune(t.Plain)
	w, i := 0, 0
	for ; i < len(rs); i++ {
		rw := runeWidth(rs[i])
		if w+rw > width {
			break
		}
		w += rw
	}
	t = slice(t, 0, i)
	if w < width {
		t.Plain += strings.Repeat(" ", width-w)
	}
	return t
}

// nameCell is the entry's name in nw cells: cut with "…" if it is wider
// (Rich's overflow "ellipsis"), padded otherwise, in style st.
func nameCell(name string, nw int, st styled.Style) styled.Text {
	t := styled.Text{Plain: name, Style: st}
	if cellLen(name) > nw {
		t = setCellSize(t, max(0, nw-1))
		t.Plain += "…"
	}
	return fitLine(t, nw)
}

// entryLines lays out one entry in width cells: name column nw wide, two
// spaces, the value in the rest. The name has style nameSt; whole adds
// reverse video to every cell of it (the focused selection).
func entryLines(name string, value styled.Text, nw, width int, nameSt styled.Style, whole bool) []styled.Text {
	vw := max(1, width-nw-2)
	vals := wrapText(value, vw)
	out := make([]styled.Text, len(vals))
	for i, v := range vals {
		var l styled.Text
		if i == 0 {
			l.AppendText(nameCell(name, nw, nameSt))
		} else {
			l.Plain = strings.Repeat(" ", nw)
		}
		l.Plain += "  "
		l.AppendText(v)
		if whole {
			l.Spans = append(l.Spans, styled.Span{Start: 0, End: utf8.RuneCountInString(l.Plain), Style: styled.Style{Reverse: true}})
		}
		out[i] = l
	}
	return out
}

// entryHeight is how many lines entryLines gives: a one-line value that
// fits takes the quick way (Python's _LazyEntry.one_line).
func entryHeight(value styled.Text, nw, width int) int {
	vw := max(1, width-nw-2)
	if !strings.ContainsAny(value.Plain, "\n\t") && cellLen(value.Plain) <= vw {
		return 1
	}
	return len(wrapText(value, vw))
}

// Textual's vertical scrollbar glyphs (ScrollBarRender.VERTICAL_BARS).
var bars = []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", " "}

// scrollbar is the vertical scrollbar of a view h rows high onto total
// lines scrolled to top, as Textual draws it (ScrollBarRender.render_bar):
// one cell per row, the thumb in reverse video of the bar colour, its ends
// in eighth blocks.
func scrollbar(h, total, top int, bar styled.Style) []styled.Text {
	out := make([]styled.Text, h)
	for i := range out {
		out[i] = styled.Text{Plain: " "}
	}
	if h <= 0 || total <= h {
		return out
	}
	const n = 8
	thumb := max(1, float64(h)/(float64(total)/float64(h)))
	pos := (float64(h) - thumb) * (float64(top) / float64(total-h))
	start := int(pos * n)
	end := start + int(math.Ceil(thumb*n))
	si, sb := max(0, start)/n, max(0, start)%n
	ei, eb := max(0, end)/n, max(0, end)%n
	rev := bar
	rev.Reverse = true
	for i := si; i < min(ei, h); i++ {
		out[i] = styled.Text{Plain: " ", Style: rev}
	}
	if si < h {
		if c := bars[n-1-sb]; c != " " {
			out[si] = styled.Text{Plain: c, Style: bar}
		}
	}
	if ei < h {
		if c := bars[n-1-eb]; c != " " {
			out[ei] = styled.Text{Plain: c, Style: rev}
		}
	}
	return out
}
