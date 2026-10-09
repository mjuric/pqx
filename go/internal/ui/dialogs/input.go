package dialogs

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// field is a one-line text input as Textual's Input draws it with pqx's
// CSS: its own box (3 rows, the border dim, the accent colour when focused),
// padding 0 2, the placeholder dim, its own block cursor (reverse video),
// and all of the text selected when it gains focus, so typing replaces it.
type field struct {
	value       []rune
	pos         int  // the cursor, 0…len(value)
	selected    bool // all of value is selected
	off         int  // the first rune shown
	placeholder string
	hasFocus    bool
}

func newField(value, placeholder string) *field {
	f := &field{placeholder: placeholder}
	f.SetValue(value)
	return f
}

func (f *field) Value() string { return string(f.value) }

// SetValue replaces the text, the cursor at its end.
func (f *field) SetValue(v string) {
	f.value = []rune(v)
	f.pos = len(f.value)
	f.selected = false
}

func (f *field) focus() {
	f.hasFocus = true
	f.selected = len(f.value) > 0 // Textual's select_on_focus
}

func (f *field) blur() { f.hasFocus = false; f.selected = false }

// deleteSelection removes the selected text, if any.
func (f *field) deleteSelection() bool {
	if !f.selected {
		return false
	}
	f.value, f.pos, f.selected = nil, 0, false
	return true
}

func (f *field) insert(s string) {
	var rs []rune
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		if unicode.IsControl(r) {
			continue
		}
		rs = append(rs, r)
	}
	if len(rs) == 0 {
		return
	}
	f.deleteSelection()
	f.value = append(f.value[:f.pos], append(rs, f.value[f.pos:]...)...)
	f.pos += len(rs)
}

// update edits the text for a key or a paste (Textual's Input bindings).
func (f *field) update(msg tea.Msg) tea.Cmd {
	if !f.hasFocus {
		return nil
	}
	switch m := msg.(type) {
	case tea.PasteMsg:
		f.insert(m.Content)
	case tea.KeyPressMsg:
		switch m.String() {
		case "left":
			if f.selected {
				f.pos, f.selected = 0, false
			} else if f.pos > 0 {
				f.pos--
			}
		case "right":
			f.selected = false
			if f.pos < len(f.value) {
				f.pos++
			}
		case "home", "ctrl+a":
			f.pos, f.selected = 0, false
		case "end", "ctrl+e":
			f.pos, f.selected = len(f.value), false
		case "backspace", "ctrl+h":
			if !f.deleteSelection() && f.pos > 0 {
				f.value = append(f.value[:f.pos-1], f.value[f.pos:]...)
				f.pos--
			}
		case "delete", "ctrl+d":
			if !f.deleteSelection() && f.pos < len(f.value) {
				f.value = append(f.value[:f.pos], f.value[f.pos+1:]...)
			}
		case "ctrl+u":
			if !f.deleteSelection() {
				f.value = append([]rune(nil), f.value[f.pos:]...)
				f.pos = 0
			}
		case "ctrl+k":
			if !f.deleteSelection() {
				f.value = f.value[:f.pos]
			}
		case "ctrl+w", "alt+backspace":
			if !f.deleteSelection() {
				i := f.pos
				for i > 0 && f.value[i-1] == ' ' {
					i--
				}
				for i > 0 && f.value[i-1] != ' ' {
					i--
				}
				f.value = append(f.value[:i], f.value[f.pos:]...)
				f.pos = i
			}
		default:
			if m.Text != "" && m.Mod&^tea.ModShift == 0 {
				f.insert(m.Text)
			}
		}
	}
	return nil
}

// lines draws the field w cells wide: its border, the text and its border.
func (f *field) lines(look kit.Look, w int) []string {
	bs := look.Style("border")
	if f.hasFocus {
		bs = look.Style("border-focus")
	}
	edge := func(s string) string { return text(look, s, bs) }
	iw := max(1, w-6) // border and padding 2
	return []string{
		edge("┌" + strings.Repeat("─", max(0, w-2)) + "┐"),
		edge("│") + "  " + fit(f.content(look, iw), iw) + "  " + edge("│"),
		edge("└" + strings.Repeat("─", max(0, w-2)) + "┘"),
	}
}

// content is the visible text, iw cells: the cursor's cell always shows
// (the text scrolls to keep it in view).
func (f *field) content(look kit.Look, iw int) string {
	dim := look.Style("dim")
	cursor := styled.Style{Reverse: true}
	var t styled.Text
	if len(f.value) == 0 {
		p := []rune(ansi.Truncate(f.placeholder, iw, ""))
		if f.hasFocus {
			if len(p) == 0 {
				t.Append(" ", cursor)
			} else {
				t.Append(string(p[:1]), dim.Plus(cursor))
				t.Append(string(p[1:]), dim)
			}
		} else {
			t.Append(string(p), dim)
		}
		return look.Render(t)
	}
	if f.pos < f.off {
		f.off = f.pos
	}
	if f.pos-f.off >= iw {
		f.off = f.pos - iw + 1
	}
	f.off = max(0, min(f.off, len(f.value)))
	end := min(len(f.value), f.off+iw)
	for i := f.off; i < end; i++ {
		st := styled.Style{}
		if f.hasFocus && (f.selected || i == f.pos) {
			st = cursor
		}
		t.Append(string(f.value[i]), st)
	}
	if f.hasFocus && f.pos == len(f.value) && f.pos-f.off < iw {
		t.Append(" ", cursor)
	}
	return look.Render(t)
}

// cursor: the field draws its own cursor, as Textual does; the terminal's
// stays hidden.
func (f *field) cursor(x, y int) *tea.Cursor { return nil }
