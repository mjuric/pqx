package data

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Sanitize returns s with every character that could act on a terminal shown
// as a visible stand-in, so text from a file can be printed safely. It is a
// port of pqx's fmt.sanitize:
//
//   - C0 controls as the Unicode control pictures (ESC as ␛, NUL as ␀,
//     tab as ␉, newline as ␊), DEL as ␡;
//   - C1 controls (U+0080 to U+009F) as \x9b and the like;
//   - bidi controls and zero-width characters as ⟨U+202E⟩ and the like.
//
// Bytes that aren't valid UTF-8 are shown as \xff and the like (Python's
// strings can't hold them; a terminal might read a lone 0x9b as CSI).
// The usual string, printable ASCII, comes back as is, without a copy.
func Sanitize(s string) string { return sanitize(s, false) }

// SanitizeKeepWS is Sanitize, but keeps tab and newline (for text laid out on
// several lines).
func SanitizeKeepWS(s string) string { return sanitize(s, true) }

func sanitize(s string, keepWS bool) string {
	i := 0
	for i < len(s) {
		c := s[i]
		if c < 0x20 || c >= 0x7F {
			break
		}
		i++
	}
	if i == len(s) {
		return s
	}
	if !needsSanitize(s[i:], keepWS) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	b.WriteString(s[:i])
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			fmt.Fprintf(&b, "\\x%02x", s[i])
			i++
			continue
		}
		if rep, ok := controlRepr(r, keepWS); ok {
			b.WriteString(rep)
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// needsSanitize reports whether s holds anything sanitize would change.
func needsSanitize(s string, keepWS bool) bool {
	for i := 0; i < len(s); {
		c := s[i]
		if c >= 0x20 && c < 0x7F {
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			return true
		}
		if _, ok := controlRepr(r, keepWS); ok {
			return true
		}
		i += size
	}
	return false
}

// HasControls reports whether Sanitize would change s.
func HasControls(s string) bool { return needsSanitize(s, false) }

// controlRepr is the visible stand-in for r, if r needs one.
func controlRepr(r rune, keepWS bool) (string, bool) {
	switch {
	case r < 0x20:
		if keepWS && (r == '\t' || r == '\n') {
			return "", false
		}
		return string(rune(0x2400 + r)), true
	case r == 0x7F:
		return "␡", true
	case r >= 0x80 && r < 0xA0:
		return fmt.Sprintf("\\x%02x", r), true
	case isInvisible(r):
		return fmt.Sprintf("⟨U+%04X⟩", r), true
	}
	return "", false
}

// isInvisible: bidi controls (embeddings, overrides, isolates, marks), which
// reorder what's shown around them, and zero-width characters, which hide in it.
func isInvisible(r rune) bool {
	switch {
	case r >= 0x202A && r < 0x202F, r >= 0x2066 && r < 0x206A, r >= 0x200B && r < 0x2010:
		return true
	case r == 0x061C, r == 0x2060, r == 0xFEFF:
		return true
	}
	return false
}
