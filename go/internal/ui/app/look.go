package app

import (
	imgcolor "image/color"

	"strings"

	"charm.land/lipgloss/v2"

	"github.com/mjuric/pqx/go/internal/styled"
)

// BasicLook is the terminal-palette look until internal/theme (WP6) is
// wired in: the accent is blue, secondary text faint.
type BasicLook struct{}

// Style implements kit.Look.
func (BasicLook) Style(role string) styled.Style {
	switch role {
	case "accent", "border-focus":
		return styled.Style{Fg: "accent"}
	case "dim":
		return styled.Style{Dim: true}
	case "border":
		return styled.Style{Fg: "bright_black"}
	case "error":
		return styled.Style{Fg: "red"}
	case "warning":
		return styled.Style{Fg: "yellow"}
	case "success":
		return styled.Style{Fg: "green"}
	case "header":
		return styled.Style{Bold: true}
	case "cursor", "selection":
		return styled.Style{Reverse: true}
	}
	return styled.Style{}
}

// DarkBG implements kit.Look.
func (BasicLook) DarkBG() bool { return true }

// Render implements kit.Look.
func (l BasicLook) Render(t styled.Text) string {
	runes := []rune(t.Plain)
	if len(t.Spans) == 0 {
		return lg(t.Style).Render(t.Plain)
	}
	// the style of each rune: the base, then the spans in order
	st := make([]styled.Style, len(runes))
	for i := range st {
		st[i] = t.Style
	}
	for _, sp := range t.Spans {
		for i := max(0, sp.Start); i < min(sp.End, len(runes)); i++ {
			st[i] = st[i].Plus(sp.Style)
		}
	}
	var b strings.Builder
	for i := 0; i < len(runes); {
		j := i + 1
		for j < len(runes) && st[j] == st[i] {
			j++
		}
		b.WriteString(lg(st[i]).Render(string(runes[i:j])))
		i = j
	}
	return b.String()
}

var ansiNames = map[string]int{
	"black": 0, "red": 1, "green": 2, "yellow": 3, "blue": 4, "magenta": 5, "cyan": 6, "white": 7,
	"bright_black": 8, "bright_red": 9, "bright_green": 10, "bright_yellow": 11,
	"bright_blue": 12, "bright_magenta": 13, "bright_cyan": 14, "bright_white": 15,
	"accent": 4, "border": 8,
}

func color(c styled.Color) (imgcolor.Color, bool) {
	s := string(c)
	switch {
	case s == "":
		return nil, false
	case strings.HasPrefix(s, "#"):
		return lipgloss.Color(s), true
	case strings.HasPrefix(s, "color(") && strings.HasSuffix(s, ")"):
		return lipgloss.Color(s[6 : len(s)-1]), true
	}
	if n, ok := ansiNames[strings.TrimPrefix(s, "ansi_")]; ok {
		return lipgloss.ANSIColor(n), true
	}
	return nil, false
}

func lg(s styled.Style) lipgloss.Style {
	st := lipgloss.NewStyle().Bold(s.Bold).Faint(s.Dim).Italic(s.Italic).Reverse(s.Reverse).Underline(s.Underline)
	if c, ok := color(s.Fg); ok {
		st = st.Foreground(c)
	}
	if c, ok := color(s.Bg); ok {
		st = st.Background(c)
	}
	return st
}
