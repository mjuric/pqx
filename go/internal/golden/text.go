package golden

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Text is Rich Text: plain text, a base style and styled spans. Offsets count
// code points (runes), not bytes. Adjacent spans of one style are merged in the
// files, so compare CharStyles rather than span lists.
type Text struct {
	Text  string `json:"text"`
	Style string `json:"style"`
	Spans []Span `json:"spans"`
}

// Span styles runes [Start, End) of a Text.
type Span struct {
	Start, End int
	Style      string
}

// UnmarshalJSON reads a span, [start, end, "style"].
func (s *Span) UnmarshalJSON(b []byte) error {
	var a []json.RawMessage
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	if len(a) != 3 {
		return fmt.Errorf("span %s: want [start, end, style]", b)
	}
	if err := json.Unmarshal(a[0], &s.Start); err != nil {
		return err
	}
	if err := json.Unmarshal(a[1], &s.End); err != nil {
		return err
	}
	return json.Unmarshal(a[2], &s.Style)
}

// MarshalJSON writes a span as [start, end, "style"].
func (s Span) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{s.Start, s.End, s.Style})
}

// CharStyles is the style of each rune of the text: the base style and the
// styles of the spans covering it, in order, joined by spaces ("" for none).
func (t Text) CharStyles() []string {
	n := len([]rune(t.Text))
	parts := make([][]string, n)
	if t.Style != "" {
		for i := range parts {
			parts[i] = append(parts[i], t.Style)
		}
	}
	for _, s := range t.Spans {
		for i := max(s.Start, 0); i < min(s.End, n); i++ {
			if s.Style != "" {
				parts[i] = append(parts[i], s.Style)
			}
		}
	}
	out := make([]string, n)
	for i, p := range parts {
		out[i] = strings.Join(p, " ")
	}
	return out
}

// Lines is the text split on newlines.
func (t Text) Lines() []string {
	return strings.Split(t.Text, "\n")
}
