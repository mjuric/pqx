package dialogs

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// renderMarkdown draws the Markdown the help uses as styled lines w cells
// wide: "#" and "##" headings, paragraphs (reflowed), "* " lists, tables
// (columns aligned, the last one wrapped), and **bold**, *italic* and
// `code` inline. It is not a general Markdown renderer.
func renderMarkdown(look kit.Look, src string, w int) []styled.Text {
	var out []styled.Text
	blank := func() {
		if len(out) > 0 && out[len(out)-1].Plain != "" {
			out = append(out, styled.Text{})
		}
	}
	lines := strings.Split(strings.TrimRight(src, "\n"), "\n")
	for i := 0; i < len(lines); {
		l := lines[i]
		switch {
		case strings.TrimSpace(l) == "":
			i++
		case strings.HasPrefix(l, "# "):
			blank()
			out = append(out, styled.New(strings.TrimPrefix(l, "# "), styled.Style{Bold: true, Fg: look.Style("accent").Fg}))
			out = append(out, styled.Text{})
			i++
		case strings.HasPrefix(l, "## "):
			blank()
			out = append(out, styled.New(strings.TrimPrefix(l, "## "), styled.Style{Bold: true, Underline: true}))
			out = append(out, styled.Text{})
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
			out = append(out, table(look, rows, w)...)
			out = append(out, styled.Text{})
		case strings.HasPrefix(l, "* "):
			item := strings.TrimPrefix(l, "* ")
			for i++; i < len(lines) && strings.HasPrefix(lines[i], "  ") && strings.TrimSpace(lines[i]) != ""; i++ {
				item += " " + strings.TrimSpace(lines[i])
			}
			for k, t := range wrapText(inline(look, item), w-2) {
				lead := "• "
				if k > 0 {
					lead = "  "
				}
				line := styled.New(lead, look.Style("dim"))
				line.AppendText(t)
				out = append(out, line)
			}
			if i >= len(lines) || !strings.HasPrefix(lines[i], "* ") {
				out = append(out, styled.Text{})
			}
		default:
			para := strings.TrimSpace(l)
			for i++; i < len(lines) && isText(lines[i]); i++ {
				para += " " + strings.TrimSpace(lines[i])
			}
			out = append(out, wrapText(inline(look, para), w)...)
			out = append(out, styled.Text{})
		}
	}
	for len(out) > 0 && out[len(out)-1].Plain == "" {
		out = out[:len(out)-1]
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
			st = styled.Style{Fg: look.Style("accent").Fg}
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

// table lays out rows (the first is the header, bold, with a rule under
// it): two cells between columns, every column but the last as wide as its
// widest cell (at most half the width), the last wrapped in what is left.
func table(look kit.Look, rows [][]string, w int) []styled.Text {
	if len(rows) == 0 {
		return nil
	}
	ncol := len(rows[0])
	cells := make([][]styled.Text, len(rows))
	widths := make([]int, ncol)
	for r, row := range rows {
		cells[r] = make([]styled.Text, ncol)
		for c := 0; c < ncol && c < len(row); c++ {
			t := inline(look, row[c])
			if r == 0 {
				t.Style = styled.Style{Bold: true}
			}
			cells[r][c] = t
			if c < ncol-1 {
				widths[c] = max(widths[c], min(w/2, ansi.StringWidth(t.Plain)))
			}
		}
	}
	used := 0
	for c := 0; c < ncol-1; c++ {
		used += widths[c] + 2
	}
	widths[ncol-1] = max(10, w-used)
	var out []styled.Text
	for r := range rows {
		wrapped := make([][]styled.Text, ncol)
		height := 1
		for c := 0; c < ncol; c++ {
			wrapped[c] = wrapText(cells[r][c], widths[c])
			height = max(height, len(wrapped[c]))
		}
		for k := 0; k < height; k++ {
			var line styled.Text
			for c := 0; c < ncol; c++ {
				var part styled.Text
				if k < len(wrapped[c]) {
					part = wrapped[c][k]
				}
				line.AppendText(part)
				if c < ncol-1 {
					line.Append(strings.Repeat(" ", widths[c]-ansi.StringWidth(part.Plain)+2), styled.Style{})
				}
			}
			out = append(out, line)
		}
		if r == 0 {
			out = append(out, styled.New(strings.Repeat("─", min(w, used+widths[ncol-1])), look.Style("dim")))
		}
	}
	return out
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
