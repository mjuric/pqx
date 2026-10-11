// Package styled is text with styles, pqx's stand-in for Rich's Text:
// fmtx and plots return it, and the theme turns it into terminal output
// (internal/theme). Part of the contract (docs/design/go-port.md).
package styled

import "strings"

// Color is a terminal colour: "" for the default, an ANSI name ("red",
// "bright_black", …), "color(N)" for one of the 256 colours, or "#rrggbb".
// Two names are roles the theme resolves: "accent" and "border".
type Color string

// Style is a set of attributes. The zero Style is plain text.
type Style struct {
	Fg, Bg                                Color
	Bold, Dim, Italic, Reverse, Underline bool
}

// Plus is s with t's attributes added (t's colours win when set).
func (s Style) Plus(t Style) Style {
	if t.Fg != "" {
		s.Fg = t.Fg
	}
	if t.Bg != "" {
		s.Bg = t.Bg
	}
	s.Bold = s.Bold || t.Bold
	s.Dim = s.Dim || t.Dim
	s.Italic = s.Italic || t.Italic
	s.Reverse = s.Reverse || t.Reverse
	s.Underline = s.Underline || t.Underline
	return s
}

// Justify is how a cell's text sits in its column.
type Justify int

const (
	Left Justify = iota
	Right
	Center
)

// Span styles runes [Start, End) of a Text (rune offsets, not bytes).
type Span struct {
	Start, End int
	Style      Style
}

// Text is a string with a base style and styled spans; spans later in the
// list win where they overlap. Lines are separated by "\n".
type Text struct {
	Plain   string
	Style   Style
	Spans   []Span
	Justify Justify
}

// New is plain text with a base style.
func New(s string, st Style) Text { return Text{Plain: s, Style: st} }

// Append adds s in style st.
func (t *Text) Append(s string, st Style) {
	n := len([]rune(t.Plain))
	t.Plain += s
	if st != (Style{}) {
		t.Spans = append(t.Spans, Span{n, n + len([]rune(s)), st})
	}
}

// AppendText adds u, keeping its styles (u's base style becomes a span).
func (t *Text) AppendText(u Text) {
	n := len([]rune(t.Plain))
	t.Plain += u.Plain
	if u.Style != (Style{}) {
		t.Spans = append(t.Spans, Span{n, n + len([]rune(u.Plain)), u.Style})
	}
	for _, sp := range u.Spans {
		t.Spans = append(t.Spans, Span{sp.Start + n, sp.End + n, sp.Style})
	}
}

// Lines splits t at "\n", each line keeping its styles.
func (t Text) Lines() []Text {
	parts := strings.Split(t.Plain, "\n")
	out := make([]Text, len(parts))
	pos := 0
	for i, p := range parts {
		n := len([]rune(p))
		l := Text{Plain: p, Style: t.Style, Justify: t.Justify}
		for _, sp := range t.Spans {
			a, b := max(sp.Start, pos), min(sp.End, pos+n)
			if a < b {
				l.Spans = append(l.Spans, Span{a - pos, b - pos, sp.Style})
			}
		}
		out[i] = l
		pos += n + 1
	}
	return out
}
