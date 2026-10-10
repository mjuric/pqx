package detail

import (
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

// cellLen is the cells of s, a line without tabs (Rich's cell_len).
func cellLen(s string) int { return cells.Width(s) }

// span is one of Rich's graphemes: runes [a, b) taking w cells.
type span struct{ a, b, w int }

// graphemes splits rs as Rich's split_graphemes does: a zero-width
// character goes with the one before it, a zero-width joiner with the
// characters on both sides, and a variation selector 16 widens the
// character before it if that turns it wide.
func graphemes(rs []rune) []span {
	out := make([]span, 0, len(rs))
	var last rune
	haveLast := false
	for i := 0; i < len(rs); {
		r := rs[i]
		if r == 0x200D || r == 0xFE0F {
			if len(out) == 0 {
				out = append(out, span{i, i + 1, 0})
				i++
				continue
			}
			sp := &out[len(out)-1]
			if r == 0x200D {
				i += 2
				i = min(i, len(rs))
			} else {
				i++
				if haveLast && cellLen(string(last)+"\ufe0f") > cellLen(string(last)) {
					sp.w++
				}
				haveLast = false
			}
			sp.b = i
			continue
		}
		if w := cellLen(string(r)); w > 0 {
			last, haveLast = r, true
			out = append(out, span{i, i + 1, w})
		} else if len(out) > 0 {
			out[len(out)-1].b = i + 1
		} else {
			out = append(out, span{i, i + 1, 0})
		}
		i++
	}
	return out
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

// chopCells cuts rs into pieces of at most width cells at grapheme
// boundaries (Rich's chop_cells), returning the pieces' lengths in runes.
// A grapheme wider than width makes an empty piece before it, as in Rich.
func chopCells(rs []rune, width int) []int {
	var out []int
	size, start := 0, 0
	for _, g := range graphemes(rs) {
		if size+g.w > width {
			out = append(out, g.a-start)
			start, size = g.a, 0
		}
		size += g.w
	}
	if size > 0 {
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

// lineHeight is how many lines wrapText makes of one line.
func lineHeight(line styled.Text, width int) int {
	if !strings.ContainsRune(line.Plain, '\t') && cellLen(line.Plain) <= width {
		return 1
	}
	return len(wrapText(line, width))
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
	pos, n, part := 0, 0, 0
	for i, r := range rs {
		at[i] = n
		if r == '\t' {
			pos += cellLen(string(rs[part:i])) + 1
			part = i + 1
			k := 1
			if rem := pos % 8; rem != 0 {
				k += 8 - rem
				pos += 8 - rem
			}
			b.WriteString(strings.Repeat(" ", k))
			n += k
			continue
		}
		b.WriteRune(r)
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

// setCellSize cuts t to width cells at a grapheme boundary, a space taking
// the place of a wide character the edge would split (Rich's
// set_cell_size), and pads it to width.
func setCellSize(t styled.Text, width int) styled.Text {
	if width <= 0 {
		return slice(t, 0, 0)
	}
	rs := []rune(t.Plain)
	w, end := 0, len(rs)
	for _, g := range graphemes(rs) {
		if w+g.w > width {
			end = g.a
			break
		}
		w += g.w
	}
	t = slice(t, 0, end)
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
	n := 0
	for _, l := range value.Lines() {
		n += lineHeight(l, vw)
	}
	return n
}
