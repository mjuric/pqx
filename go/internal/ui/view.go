package ui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/data"
)

var border = lipgloss.RoundedBorder()

// View implements tea.Model: the whole screen, in the alternate screen with
// cell-motion mouse reporting.
func (m *Model) View() tea.View {
	v := tea.NewView(m.Render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "pqx " + sanitizeTitle(m.name)
	switch m.focus {
	case focusFilter:
		if c := m.filter.Cursor(); c != nil {
			c.Position.X += 2 + len("where") + 1
			c.Position.Y += filterTop + 1
			c.Color = nil
			v.Cursor = c
		}
	case focusGoto:
		if c := m.gotoIn.Cursor(); c != nil {
			c.Position.X += textWidth(gotoPrompt)
			c.Position.Y += m.h - 2
			c.Color = nil
			v.Cursor = c
		}
	}
	return v
}

// sanitizeTitle drops control characters from the window title (the file
// name can hold anything).
func sanitizeTitle(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

const gotoPrompt = "go to row: "

// Render draws the screen as text with SGR sequences, one line per row.
func (m *Model) Render() string {
	if m.w <= 0 || m.h <= 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(m.w * m.h * 3)
	m.renderFilter(&b)
	m.renderGrid(&b)
	b.WriteString(m.statusLine())
	b.WriteByte('\n')
	b.WriteString(m.keyBar())
	return b.String()
}

// boxTop writes a top or bottom border with a title, in colour c.
func (m *Model) boxEdge(b *strings.Builder, c, left, right, title string, titleW int) {
	inner := m.w - 2
	b.WriteString(c)
	b.WriteString(left)
	if title != "" && inner >= titleW+3 {
		b.WriteString(border.Top)
		b.WriteString(sgrFgOff)
		b.WriteString(" ")
		b.WriteString(title)
		b.WriteString(" ")
		b.WriteString(c)
		b.WriteString(strings.Repeat(border.Top, inner-titleW-3))
	} else {
		b.WriteString(strings.Repeat(border.Top, max(0, inner)))
	}
	b.WriteString(right)
	b.WriteString(sgrFgOff)
	b.WriteByte('\n')
}

func (m *Model) renderFilter(b *strings.Builder) {
	c := unfocusedBorder
	if m.focus == focusFilter {
		c = accent
	}
	if m.filterErr != "" {
		c = fgRed
	}
	m.boxEdge(b, c, border.TopLeft, border.TopRight, "", 0)
	// the line inside: "where" in the accent colour, then the input or the
	// current filter or a faint hint
	inner := m.w - 4
	label := "where"
	var body string
	if m.focus == focusFilter {
		body = m.filter.View()
	} else if w := m.v.view.Where; w != "" {
		body = fit(sanitizeTitle(w), max(0, inner-len(label)-1))
	} else {
		body = faint(fit("/ to filter: "+m.hint, max(0, inner-len(label)-1)))
	}
	line := fg(accent, label) + " " + body
	lw := ansi.StringWidth(line)
	if lw > inner {
		line = ansi.Truncate(line, inner, "…")
		lw = inner
	}
	b.WriteString(fg(c, border.Left))
	b.WriteString(" ")
	b.WriteString(line)
	b.WriteString(spaces(inner - lw))
	b.WriteString(" ")
	b.WriteString(fg(c, border.Right))
	b.WriteByte('\n')
	if m.filterErr != "" {
		t := "✗ " + sanitizeTitle(m.filterErr)
		t = fit(t, max(0, m.w-6))
		m.boxEdge(b, c, border.BottomLeft, border.BottomRight, fg(fgRed, t), textWidth(t))
	} else {
		m.boxEdge(b, c, border.BottomLeft, border.BottomRight, "", 0)
	}
}

func (m *Model) renderGrid(b *strings.Builder) {
	c := unfocusedBorder
	if m.focus == focusGrid {
		c = accent
	}
	name := fit(sanitizeTitle(m.name), max(1, m.w-6))
	m.boxEdge(b, c, border.TopLeft, border.TopRight, boldFg(fgCyan, name), textWidth(name))

	inner := m.innerW()
	slots := m.layout()
	left, right := fg(c, border.Left), fg(c, border.Right)
	lw := m.labelW()
	d := m.v

	moreLeft := m.left > 0
	moreRight := false
	if len(slots) > 0 {
		last := slots[len(slots)-1]
		moreRight = last.clipped || last.col < len(m.cols)-1
	}

	// header: names (bold) and types (faint)
	for line := 0; line < headerRows; line++ {
		b.WriteString(left)
		if line == 0 && moreLeft {
			b.WriteString(bold("‹"))
		} else {
			b.WriteString(" ")
		}
		b.WriteString(spaces(lw + 1))
		x := lw + 1
		for _, s := range slots {
			col := m.cols[s.col]
			text := col.Name
			if line == 1 {
				text = col.Type
			}
			t := fit(sanitizeTitle(text), s.w)
			tw := textWidth(t)
			if line == 0 {
				t = bold(t)
			} else {
				t = faint(t)
			}
			m.writeSlot(b, s, t, tw, false)
			x += s.sw
		}
		b.WriteString(spaces(inner - x))
		if line == 0 && moreRight {
			b.WriteString(bold("›"))
		} else {
			b.WriteString(" ")
		}
		b.WriteString(right)
		b.WriteByte('\n')
	}

	n := m.bodyH()
	lim := d.limit()
	for i := 0; i < n; i++ {
		r := m.top + int64(i)
		b.WriteString(left)
		b.WriteString(" ")
		if lim >= 0 && r >= lim {
			b.WriteString(spaces(inner))
			b.WriteString(" ")
			b.WriteString(right)
			b.WriteByte('\n')
			continue
		}
		if fr, ok := d.fileRow[r]; ok {
			s := commas(fr)
			b.WriteString(spaces(lw - len(s)))
			b.WriteString(faint(s))
		} else {
			b.WriteString(spaces(lw - 1))
			b.WriteString(faint("·"))
		}
		b.WriteString(" ")
		x := lw + 1
		for _, s := range slots {
			name := m.cols[s.col].Name
			v, ok := d.cells[name][r]
			cur := r == m.curRow && s.col == m.curCol
			var t string
			var tw int
			switch {
			case !ok:
				t, tw = faint("·"), 1
			case v == data.Null:
				t, tw = faint(data.Null), 1
			default:
				t = fit(v, s.w)
				tw = textWidth(t)
			}
			m.writeSlot(b, s, t, tw, cur)
			x += s.sw
		}
		b.WriteString(spaces(inner - x))
		b.WriteString(" ")
		b.WriteString(right)
		b.WriteByte('\n')
	}
	m.boxEdge(b, c, border.BottomLeft, border.BottomRight, "", 0)
}

// writeSlot writes one column slot: a space, the text padded to the column
// width (right aligned for numbers), a space; reverse video for the cursor.
func (m *Model) writeSlot(b *strings.Builder, s slot, t string, tw int, cur bool) {
	if s.sw < 2 {
		b.WriteString(spaces(s.sw))
		return
	}
	if cur {
		b.WriteString(sgrReverse)
	}
	b.WriteString(" ")
	pad(b, t, tw, s.w, m.right[s.col])
	b.WriteString(" ")
	if cur {
		b.WriteString(sgrNoRev)
	}
}

func (m *Model) statusLine() string {
	if m.focus == focusGoto {
		return fg(accent, gotoPrompt) + m.gotoIn.View()
	}
	sep := faint("  ·  ")
	var b strings.Builder
	b.WriteString(boldFg(fgCyan, sanitizeTitle(m.name)))
	b.WriteString(sep)
	total := "?"
	if m.v.total >= 0 {
		total = commas(m.v.total)
	}
	if m.v.total == 0 {
		b.WriteString("no rows")
	} else {
		fmt.Fprintf(&b, "row %s of %s", commas(m.curRow+1), total)
	}
	if w := m.v.view.Where; w != "" {
		b.WriteString(sep)
		b.WriteString(faint("where "))
		b.WriteString(sanitizeTitle(w))
	}
	if m.counting {
		b.WriteString("   ")
		b.WriteString(fg(accent, spinner[m.spin]))
		b.WriteString(" counting…")
		if el := m.now().Sub(m.countStart); el >= time.Second {
			s := int(el.Seconds())
			b.WriteString(faint(fmt.Sprintf("  %02d:%02d elapsed", s/60, s%60)))
		}
	} else if m.inflight != nil && m.now().Sub(m.fetchStart) >= 150*time.Millisecond {
		b.WriteString("   ")
		b.WriteString(fg(accent, spinner[m.spin]))
		b.WriteString(" loading…")
	}
	switch m.msgKind {
	case msgOK:
		b.WriteString("   " + fg(fgGreen, "✓") + " " + m.msg)
	case msgWarn:
		b.WriteString("   " + fg(fgYellow, "!") + " " + m.msg)
	case msgErr:
		b.WriteString("   " + fg(fgRed, "✗") + " " + sanitizeTitle(m.msg))
	}
	s := b.String()
	if ansi.StringWidth(s) > m.w {
		s = ansi.Truncate(s, m.w, "…")
	}
	return s
}

var keySets = map[focusKind][][2]string{
	focusGrid: {{"/", "filter"}, {"x", "clear"}, {"g", "go to"}, {"esc", "cancel"}, {"q", "quit"},
		{"^Home ^End", "first/last row"}, {"Home End", "first/last column"}},
	focusFilter: {{"enter", "apply"}, {"esc", "back"}, {"↑↓", "history"}, {"ctrl+x", "clear"}},
	focusGoto:   {{"enter", "go"}, {"esc", "back"}},
}

func (m *Model) keyBar() string {
	var b strings.Builder
	for i, kl := range keySets[m.focus] {
		if i > 0 {
			b.WriteString("   ")
		}
		b.WriteString(bold(kl[0]))
		b.WriteString(faint(" " + kl[1]))
	}
	s := b.String()
	if ansi.StringWidth(s) > m.w {
		s = ansi.Truncate(s, m.w, "…")
	}
	return s
}

// filterHint is the filter bar's placeholder, with an example made from the
// file's first row as in Python pqx: the first numeric column with a plain
// name ("> its value") and the first text column ("= its value").
func filterHint(cols []data.Column, d *viewData, right []bool) string {
	var num, text string
	for i, c := range cols {
		v, ok := d.cells[c.Name][0]
		if !ok || v == data.Null || !plainIdent(c.Name) {
			continue
		}
		if num == "" && right[i] && isNumber(v) {
			num = c.Name + " > " + v
		} else if text == "" && strings.HasPrefix(strings.ToUpper(c.Type), "VARCHAR") {
			val := strings.TrimSpace(v)
			if r := []rune(val); len(r) > 20 {
				val = strings.TrimSpace(string(r[:20]))
			}
			if val != "" && isASCII(val) {
				text = c.Name + " = '" + strings.ReplaceAll(val, "'", "''") + "'"
			}
		}
		if num != "" && text != "" {
			break
		}
	}
	ex := "x > 0"
	switch {
	case num != "" && text != "":
		ex = num + " and " + text
	case num != "":
		ex = num
	case text != "":
		ex = text
	}
	return "SQL WHERE expression, e.g. " + ex
}

func isNumber(s string) bool {
	f, err := strconv.ParseFloat(s, 64)
	return err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
}

func plainIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}
