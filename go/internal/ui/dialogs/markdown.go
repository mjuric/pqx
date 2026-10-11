package dialogs

import (
	"math/big"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// renderMarkdown draws the Markdown the help uses as Textual's Markdown
// widget draws it (with the ANSI theme), as lines w cells wide: the blocks
// two cells in from each side; "#" centred, bold magenta, 2 rows above and
// 1 below; "##" bright blue and underlined, 2 above and 1 below;
// paragraphs reflowed with a row below; "* " lists with a bullet in the
// accent colour; tables in a grid of thin lines with the header in the
// accent colour; **bold**, *italic* and `code` (yellow) inline. Margins
// between blocks collapse (the larger counts). It is not a general
// Markdown renderer.
func renderMarkdown(look kit.Look, src string, w int) []styled.Text {
	bw := max(1, w-4) // the blocks' width
	accent := look.Style("accent").Fg
	type block struct {
		lines       []styled.Text
		top, bottom int
	}
	var blocks []block
	lines := strings.Split(strings.TrimRight(src, "\n"), "\n")
	for i := 0; i < len(lines); {
		l := lines[i]
		switch {
		case strings.TrimSpace(l) == "":
			i++
		case strings.HasPrefix(l, "# "):
			t := strings.TrimPrefix(l, "# ")
			pad := max(0, (bw-ansi.StringWidth(t))/2)
			h := styled.Text{Style: styled.Style{Bold: true, Fg: "magenta"}}
			h.Append(strings.Repeat(" ", pad)+t+strings.Repeat(" ", max(0, bw-pad-ansi.StringWidth(t))), styled.Style{})
			blocks = append(blocks, block{[]styled.Text{h}, 2, 1})
			i++
		case strings.HasPrefix(l, "## "):
			var h styled.Text
			h.Append(strings.TrimPrefix(l, "## "), styled.Style{Fg: "bright_blue", Underline: true})
			blocks = append(blocks, block{[]styled.Text{h}, 2, 1})
			i++
		case strings.HasPrefix(l, "|"):
			var rows [][]string
			for ; i < len(lines) && strings.HasPrefix(lines[i], "|"); i++ {
				cells := strings.Split(strings.Trim(strings.TrimSpace(lines[i]), "|"), "|")
				for k := range cells {
					cells[k] = strings.TrimSpace(cells[k])
				}
				if strings.Trim(strings.Join(cells, ""), "-: ") == "" {
					continue // the header's rule
				}
				rows = append(rows, cells)
			}
			blocks = append(blocks, block{table(look, rows, bw), 0, 1})
		case strings.HasPrefix(l, "* "):
			var out []styled.Text
			for i < len(lines) && strings.HasPrefix(lines[i], "* ") {
				item := strings.TrimPrefix(lines[i], "* ")
				for i++; i < len(lines) && strings.HasPrefix(lines[i], "  ") && strings.TrimSpace(lines[i]) != ""; i++ {
					item += " " + strings.TrimSpace(lines[i])
				}
				for k, t := range wrapText(inline(look, item), bw-2) {
					var line styled.Text
					bullet := "• "
					if k > 0 {
						bullet = "  " // (the bullet's column is styled all the way down)
					}
					line.Append(bullet, styled.Style{Fg: accent})
					line.AppendText(t)
					out = append(out, line)
				}
			}
			blocks = append(blocks, block{out, 0, 1})
		default:
			para := strings.TrimSpace(l)
			for i++; i < len(lines) && isText(lines[i]); i++ {
				para += " " + strings.TrimSpace(lines[i])
			}
			blocks = append(blocks, block{wrapText(inline(look, para), bw), 0, 1})
		}
	}
	var out []styled.Text
	prevBottom := 0
	for k, b := range blocks {
		gap := b.top
		if k > 0 {
			gap = max(prevBottom, b.top)
		}
		for j := 0; j < gap; j++ {
			out = append(out, styled.Text{})
		}
		for _, l := range b.lines {
			var line styled.Text
			line.Append("  ", styled.Style{})
			line.AppendText(l)
			out = append(out, line)
		}
		prevBottom = b.bottom
	}
	for j := 0; j < prevBottom; j++ {
		out = append(out, styled.Text{})
	}
	return out
}

// isText reports whether a line continues a paragraph.
func isText(l string) bool {
	return strings.TrimSpace(l) != "" && !strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "|") &&
		!strings.HasPrefix(l, "* ")
}

// inline turns **bold**, *italic* and `code` into styles.
func inline(look kit.Look, s string) styled.Text {
	var t styled.Text
	var bold, italic bool
	code := false
	var cur strings.Builder
	flush := func() {
		st := styled.Style{Bold: bold, Italic: italic}
		if code {
			st = styled.Style{Fg: "yellow"}
		}
		t.Append(cur.String(), st)
		cur.Reset()
	}
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		switch {
		case r[i] == '`':
			flush()
			code = !code
		case !code && r[i] == '*' && i+1 < len(r) && r[i+1] == '*':
			flush()
			bold = !bold
			i++
		case !code && r[i] == '*' && (italic || (i+1 < len(r) && r[i+1] != ' ')):
			flush()
			italic = !italic
		default:
			cur.WriteRune(r[i])
		}
	}
	flush()
	return t
}

// table lays out rows (the first is the header, in the accent colour) as
// Textual's MarkdownTable: a grid of thin lines round and between every
// cell, cells padded a cell each side and wrapped. The columns start at
// their widest cell (unwrapped) and are then fitted to w as Textual's grid
// layout fits them (_resolve.resolve with expand and shrink): when too wide,
// each column in turn gives up the whole excess down to its longest word.
func table(look kit.Look, rows [][]string, w int) []styled.Text {
	if len(rows) == 0 {
		return nil
	}
	ncol := len(rows[0])
	cells := make([][]styled.Text, len(rows))
	auto := make([]int, ncol)
	minimum := make([]int, ncol)
	for c := range minimum {
		minimum[c] = 1
	}
	for r, row := range rows {
		cells[r] = make([]styled.Text, ncol)
		for c := 0; c < ncol && c < len(row); c++ {
			t := inline(look, row[c])
			if r == 0 {
				t.Style = styled.Style{Fg: look.Style("accent").Fg}
			}
			cells[r][c] = t
			auto[c] = max(auto[c], ansi.StringWidth(t.Plain)+2)
			for _, word := range strings.Fields(t.Plain) {
				minimum[c] = max(minimum[c], ansi.StringWidth(word)+2)
			}
		}
	}
	widths := resolveWidths(auto, minimum, max(ncol, w-2), 1)
	var out []styled.Text
	rule := func(l, m, r string) styled.Text {
		var b strings.Builder
		b.WriteString(l)
		for c, cw := range widths {
			if c > 0 {
				b.WriteString(m)
			}
			b.WriteString(strings.Repeat("─", cw))
		}
		b.WriteString(r)
		return styled.Text{Plain: b.String()}
	}
	out = append(out, rule("┌", "┬", "┐"))
	for r := range rows {
		if r > 0 {
			out = append(out, rule("├", "┼", "┤"))
		}
		wrapped := make([][]styled.Text, ncol)
		height := 1
		for c := 0; c < ncol; c++ {
			wrapped[c] = wrapText(cells[r][c], max(1, widths[c]-2))
			height = max(height, len(wrapped[c]))
		}
		for k := 0; k < height; k++ {
			var line styled.Text
			for c := 0; c < ncol; c++ {
				line.Append("│ ", styled.Style{})
				var part styled.Text
				if k < len(wrapped[c]) {
					part = wrapped[c][k]
				}
				pad := strings.Repeat(" ", max(0, widths[c]-2-ansi.StringWidth(part.Plain)))
				if r == 0 { // the header's colour fills its cell
					part.Plain += pad
					pad = ""
				}
				line.AppendText(part)
				line.Append(pad+" ", styled.Style{})
			}
			line.Append("│", styled.Style{})
			out = append(out, line)
		}
	}
	out = append(out, rule("└", "┴", "┘"))
	return out
}

// resolveWidths is Textual's _resolve.resolve for columns of fixed widths
// with expand and shrink: the widths fill total (less a gutter between
// columns), the offsets rounded down.
func resolveWidths(widths, minimums []int, total, gutter int) []int {
	n := len(widths)
	fr := make([]*big.Rat, n)
	used := new(big.Rat)
	for i, w := range widths {
		fr[i] = big.NewRat(int64(w), 1)
		used.Add(used, fr[i])
	}
	space := big.NewRat(int64(total-gutter*(n-1)), 1)
	if used.Sign() > 0 && used.Cmp(space) < 0 { // expand
		rem := new(big.Rat).Sub(space, used)
		for i := range fr {
			add := new(big.Rat).Quo(fr[i], used)
			add.Mul(add, rem)
			fr[i] = new(big.Rat).Add(fr[i], add)
		}
	} else if used.Cmp(space) > 0 { // shrink
		excess := new(big.Rat).Sub(used, space)
		for i := range fr {
			// max(width/used, 1) * excess: the whole excess
			nw := new(big.Rat).Sub(fr[i], excess)
			if m := big.NewRat(int64(minimums[i]), 1); nw.Cmp(m) < 0 {
				nw = m
			}
			used.Sub(used, fr[i])
			used.Add(used, nw)
			fr[i] = nw
			excess = new(big.Rat).Sub(used, space)
			if excess.Sign() <= 0 {
				break
			}
		}
		if excess.Sign() > 0 {
			for i := range fr {
				cut := new(big.Rat).Quo(fr[i], used)
				cut.Mul(cut, excess)
				fr[i] = new(big.Rat).Sub(fr[i], cut)
			}
		}
	}
	// offsets: the running sums of width, gutter, width, … rounded down
	out := make([]int, n)
	acc := new(big.Rat)
	prev := 0
	for i := range fr {
		acc.Add(acc, fr[i])
		end := floorRat(acc)
		out[i] = end - prev
		acc.Add(acc, big.NewRat(int64(gutter), 1))
		prev = floorRat(acc)
	}
	return out
}

func floorRat(r *big.Rat) int {
	return int(new(big.Int).Div(r.Num(), r.Denom()).Int64())
}

// wrapText word-wraps t to w cells, keeping its styles; words longer than
// a line are broken.
func wrapText(t styled.Text, w int) []styled.Text {
	if t.Plain == "" {
		return []styled.Text{t}
	}
	w = max(1, w)
	r := []rune(t.Plain)
	var out []styled.Text
	start := 0
	for start < len(r) {
		// skip the spaces a line would start with
		for start < len(r) && r[start] == ' ' {
			start++
		}
		if start >= len(r) {
			break
		}
		width, end, lastSpace := 0, start, -1
		for end < len(r) {
			cw := ansi.StringWidth(string(r[end]))
			if width+cw > w {
				break
			}
			if r[end] == ' ' {
				lastSpace = end
			}
			width += cw
			end++
		}
		if end < len(r) && r[end] != ' ' && lastSpace > start {
			end = lastSpace
		}
		out = append(out, slice(t, start, end))
		start = end
	}
	return out
}

// slice is runes [a, b) of t, with its styles.
func slice(t styled.Text, a, b int) styled.Text {
	r := []rune(t.Plain)
	s := styled.Text{Plain: strings.TrimRight(string(r[a:b]), " "), Style: t.Style}
	n := len([]rune(s.Plain))
	for _, sp := range t.Spans {
		x, y := max(sp.Start, a)-a, min(sp.End, a+n)-a
		if x < y {
			s.Spans = append(s.Spans, styled.Span{Start: x, End: y, Style: sp.Style})
		}
	}
	return s
}
